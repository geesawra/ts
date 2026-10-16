package omdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBody = 1 << 20
	cacheCapacity   = 256
	cacheTTL        = 15 * time.Minute
)

var imdbID = regexp.MustCompile(`^tt[0-9]{7,}$`)

type Series struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Year         string `json:"year"`
	TotalSeasons int    `json:"total_seasons"`
}

type Episode struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Released string `json:"released"`
}

type Client struct {
	apiKey   string
	endpoint string
	http     *http.Client
	timeout  time.Duration
	now      func() time.Time
	mu       sync.Mutex
	cache    map[string]cacheEntry
}

type cacheEntry struct {
	series   Series
	matches  []Series
	episodes []Episode
	expires  time.Time
}

func New(apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		apiKey: strings.TrimSpace(apiKey), endpoint: "https://www.omdbapi.com/",
		timeout: timeout, now: time.Now, cache: make(map[string]cacheEntry),
		http: &http.Client{
			Timeout: timeout,
			// Never forward the API key to a redirected endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Search returns OMDb's first page of series candidates. A candidate's season
// count may be unavailable until Series resolves its full metadata.
func (c *Client) Search(ctx context.Context, q string) ([]Series, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("OMDb search requires a title")
	}
	key := "search:" + q
	if cached, ok := c.cached(key); ok {
		return cached.matches, nil
	}
	var response struct {
		Response string       `json:"Response"`
		Search   []wireSeries `json:"Search"`
	}
	if err := c.request(ctx, url.Values{"s": {q}, "type": {"series"}}, &response); err != nil {
		return nil, err
	}
	if response.Response != "True" || len(response.Search) == 0 {
		return nil, errors.New("OMDb did not return series candidates")
	}
	matches := make([]Series, 0, len(response.Search))
	seen := make(map[string]bool, len(response.Search))
	for _, item := range response.Search {
		series, err := item.resolve(false)
		if err != nil || seen[series.ID] {
			return nil, errors.New("OMDb returned invalid series candidates")
		}
		seen[series.ID] = true
		matches = append(matches, series)
	}
	c.store(key, cacheEntry{matches: matches})
	return matches, nil
}

func (c *Client) Series(ctx context.Context, id string) (Series, error) {
	if err := c.ready(ctx); err != nil {
		return Series{}, err
	}
	if !imdbID.MatchString(id) {
		return Series{}, errors.New("invalid OMDb series ID")
	}
	key := "series:" + id
	if cached, ok := c.cached(key); ok {
		return cached.series, nil
	}
	var response struct {
		Response string `json:"Response"`
		wireSeries
	}
	if err := c.request(ctx, url.Values{"i": {id}, "type": {"series"}}, &response); err != nil {
		return Series{}, err
	}
	series, err := response.wireSeries.resolve(true)
	if response.Response != "True" || err != nil || series.ID != id {
		return Series{}, errors.New("OMDb returned invalid series metadata")
	}
	c.store(key, cacheEntry{series: series})
	return series, nil
}

// Season requires a nonempty, contiguous episode inventory, not merely a
// successful response. Future episodes with an unknown release date (N/A) are
// retained: coverage of this inventory does not mean a season has finished airing.
func (c *Client) Season(ctx context.Context, id string, number int) ([]Episode, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	if !imdbID.MatchString(id) || number <= 0 {
		return nil, errors.New("invalid OMDb season request")
	}
	key := "season:" + id + ":" + strconv.Itoa(number)
	if cached, ok := c.cached(key); ok {
		return cached.episodes, nil
	}
	var response struct {
		Response     string `json:"Response"`
		Title        string `json:"Title"`
		Season       string `json:"Season"`
		TotalSeasons string `json:"totalSeasons"`
		Episodes     []struct {
			Number   string `json:"Episode"`
			Title    string `json:"Title"`
			Released string `json:"Released"`
			ID       string `json:"imdbID"`
		} `json:"Episodes"`
	}
	if err := c.request(ctx, url.Values{"i": {id}, "Season": {strconv.Itoa(number)}}, &response); err != nil {
		return nil, err
	}
	season, seasonErr := positiveNumber(response.Season)
	total, totalErr := positiveNumber(response.TotalSeasons)
	if response.Response != "True" || !validText(response.Title) || seasonErr != nil || season != number || totalErr != nil || total < number || len(response.Episodes) == 0 {
		return nil, errors.New("OMDb returned invalid season metadata")
	}
	episodes := make([]Episode, 0, len(response.Episodes))
	seenIDs := make(map[string]bool, len(response.Episodes))
	for _, item := range response.Episodes {
		n, err := positiveNumber(item.Number)
		if err != nil || !validText(item.Title) || !imdbID.MatchString(item.ID) || seenIDs[item.ID] {
			return nil, errors.New("OMDb returned invalid episode metadata")
		}
		if item.Released != "N/A" {
			if _, err := time.Parse("2006-01-02", item.Released); err != nil {
				return nil, errors.New("OMDb returned an invalid episode release date")
			}
		}
		seenIDs[item.ID] = true
		episodes = append(episodes, Episode{Number: n, Title: item.Title, Released: item.Released})
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].Number < episodes[j].Number })
	for i, episode := range episodes {
		if episode.Number != i+1 {
			return nil, errors.New("OMDb returned an incomplete or duplicate episode inventory")
		}
	}
	c.store(key, cacheEntry{episodes: episodes})
	return episodes, nil
}

type wireSeries struct {
	ID           string `json:"imdbID"`
	Title        string `json:"Title"`
	Year         string `json:"Year"`
	Type         string `json:"Type"`
	TotalSeasons string `json:"totalSeasons"`
}

func (s wireSeries) resolve(requireSeasons bool) (Series, error) {
	if !imdbID.MatchString(s.ID) || s.Type != "series" || !validText(s.Title) || !validText(s.Year) {
		return Series{}, errors.New("invalid series metadata")
	}
	var seasons int
	if requireSeasons || (s.TotalSeasons != "" && s.TotalSeasons != "N/A") {
		var err error
		seasons, err = positiveNumber(s.TotalSeasons)
		if err != nil {
			return Series{}, errors.New("invalid series season count")
		}
	}
	return Series{ID: s.ID, Title: s.Title, Year: s.Year, TotalSeasons: seasons}, nil
}

func validText(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) != "N/A"
}

func positiveNumber(value string) (int, error) {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, errors.New("invalid positive integer")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid positive integer")
	}
	return n, nil
}

func (c *Client) ready(ctx context.Context) error {
	if c.apiKey == "" {
		return errors.New("OMDb metadata is disabled")
	}
	return ctx.Err()
}

func (c *Client) request(ctx context.Context, values url.Values, result any) error {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return errors.New("invalid OMDb endpoint")
	}
	values.Set("apikey", c.apiKey)
	endpoint.RawQuery = values.Encode()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errors.New("cannot create OMDb request")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Transport errors can contain the full request URL and its API key.
		return errors.New("OMDb request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OMDb request failed (HTTP %d)", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot read OMDb response")
	}
	if len(body) > maxResponseBody {
		return errors.New("OMDb response is too large")
	}
	if err := json.Unmarshal(body, result); err != nil {
		return errors.New("invalid OMDb response")
	}
	return nil
}

func (c *Client) cached(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok {
		return cacheEntry{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.cache, key)
		return cacheEntry{}, false
	}
	entry.matches = slices.Clone(entry.matches)
	entry.episodes = slices.Clone(entry.episodes)
	return entry, true
}

func (c *Client) store(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for key, existing := range c.cache {
		if !now.Before(existing.expires) {
			delete(c.cache, key)
		}
	}
	if _, exists := c.cache[key]; !exists && len(c.cache) >= cacheCapacity {
		var oldestKey string
		var oldest time.Time
		for key, existing := range c.cache {
			if oldestKey == "" || existing.expires.Before(oldest) {
				oldestKey, oldest = key, existing.expires
			}
		}
		delete(c.cache, oldestKey)
	}
	entry.expires = now.Add(cacheTTL)
	entry.matches = slices.Clone(entry.matches)
	entry.episodes = slices.Clone(entry.episodes)
	c.cache[key] = entry
}
