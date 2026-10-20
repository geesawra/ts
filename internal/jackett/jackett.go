// Package jackett adapts go-jackett to the application's search and download API.
package jackett

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/webtor-io/go-jackett"
)

const (
	maxResponseBytes = 16 << 20
	maxConcurrency   = 4
)

type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Indexer struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Type            string              `json:"type"`
	Language        string              `json:"language"`
	Categories      []Category          `json:"categories"`
	Modes           map[string][]string `json:"modes"`
	CapabilityError string              `json:"capability_error,omitempty"`
}

type Result struct {
	ID                   string   `json:"id"`
	Title                string   `json:"title"`
	Indexer              string   `json:"indexer"`
	IndexerID            string   `json:"indexer_id"`
	GUID                 string   `json:"guid"`
	Link                 string   `json:"link"`
	Magnet               string   `json:"magnet"`
	Details              string   `json:"details"`
	Published            string   `json:"published"`
	Size                 int64    `json:"size"`
	Seeders              int      `json:"seeders"`
	Peers                int      `json:"peers"`
	Categories           []string `json:"categories"`
	DownloadVolumeFactor float64  `json:"download_volume_factor"`
	UploadVolumeFactor   float64  `json:"upload_volume_factor"`
}

// SearchPage retains pagination metadata that the upstream SDK drops. Count is
// the raw feed item count, before any SDK deduplication.
type SearchPage struct {
	Count  int
	Offset *int
	Total  *int
}

type SearchResponse struct {
	Results    []Result              `json:"results"`
	Errors     []string              `json:"errors"`
	Pagination map[string]SearchPage `json:"-"`
}

type Client struct {
	base     *url.URL
	apiKey   string
	http     *http.Client
	upstream *sdk.Client
}

func New(baseURL, apiKey string, timeout time.Duration) *Client {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return &Client{}
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	libraryHTTP := *httpClient
	libraryHTTP.Transport = &libraryTransport{base: base, apiKey: apiKey}
	upstream, err := sdk.New(sdk.Settings{ApiURL: base.String(), ApiKey: apiKey, Client: &libraryHTTP})
	if err != nil {
		return &Client{}
	}
	return &Client{base: base, apiKey: apiKey, http: httpClient, upstream: upstream}
}

// ListIndexers uses Jackett's XML t=indexers discovery, whose embedded caps
// retain string category IDs regardless of JSON DTO number/string differences.
func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	if c.upstream == nil {
		return nil, errors.New("invalid Jackett base URL")
	}
	records, err := c.upstream.ListIndexers(ctx)
	if err != nil {
		return nil, safeError(ctx, err)
	}
	indexers := make([]Indexer, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(record.Configured, "false") {
			continue
		}
		if !validIndexerID(record.ID) {
			return nil, errors.New("Jackett returned an invalid indexer ID")
		}
		indexer := Indexer{ID: record.ID, Name: record.Title, Description: record.Description, Type: record.Type, Language: record.Language}
		applyCaps(&indexer, record.Caps)
		indexers = append(indexers, indexer)
	}
	parallel(len(indexers), func(i int) {
		// Some versions omit embedded search capabilities. Do not let one
		// unavailable tracker hide other configured trackers or invent modes.
		if len(indexers[i].Modes) != 0 {
			return
		}
		caps, err := c.upstream.Caps(ctx, indexers[i].ID)
		if err != nil {
			indexers[i].CapabilityError = safeError(ctx, err).Error()
			return
		}
		applyCaps(&indexers[i], caps)
		if len(indexers[i].Modes) == 0 {
			indexers[i].CapabilityError = "Indexer advertises no supported search modes"
		}
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return indexers, nil
}

// IsPublicIndexer reports whether a configured Jackett indexer is explicitly
// marked public. Unknown or disabled indexers are not considered public.
func (c *Client) IsPublicIndexer(ctx context.Context, id string) (bool, error) {
	if c.upstream == nil {
		return false, errors.New("invalid Jackett base URL")
	}
	if !validIndexerID(id) {
		return false, nil
	}
	indexers, err := c.upstream.ListIndexers(ctx)
	if err != nil {
		return false, safeError(ctx, err)
	}
	for _, indexer := range indexers {
		if indexer.ID == id && !strings.EqualFold(indexer.Configured, "false") {
			return strings.EqualFold(strings.TrimSpace(indexer.Type), "public"), nil
		}
	}
	return false, nil
}

func applyCaps(indexer *Indexer, caps sdk.IndexerCaps) {
	categories := []Category{}
	for _, category := range caps.Categories.Categories {
		categories = append(categories, Category{ID: category.ID, Name: category.Name})
		for _, child := range category.Subcats {
			categories = append(categories, Category{ID: child.ID, Name: child.Name})
		}
	}
	if len(categories) != 0 || indexer.Categories == nil {
		indexer.Categories = categories
	}
	indexer.Modes = map[string][]string{}
	addMode := func(name, available, params string) {
		if !strings.EqualFold(available, "yes") && !strings.EqualFold(available, "true") {
			return
		}
		values := []string{}
		for _, value := range strings.Split(params, ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
		indexer.Modes[name] = values
	}
	addMode("search", caps.Searching.Search.Available, caps.Searching.Search.SupportedParams)
	addMode("tvsearch", caps.Searching.TVSearch.Available, caps.Searching.TVSearch.SupportedParams)
	addMode("movie", caps.Searching.MovieSearch.Available, caps.Searching.MovieSearch.SupportedParams)
	addMode("music", caps.Searching.MusicSearch.Available, caps.Searching.MusicSearch.SupportedParams)
	addMode("book", caps.Searching.BookSearch.Available, caps.Searching.BookSearch.SupportedParams)
}

func (c *Client) Search(ctx context.Context, indexers []string, params url.Values) (SearchResponse, error) {
	response := SearchResponse{Results: []Result{}, Errors: []string{}}
	if c.upstream == nil {
		return response, errors.New("invalid Jackett base URL")
	}
	if len(indexers) == 0 {
		configured, err := c.Indexers(ctx)
		if err != nil {
			return response, err
		}
		for _, indexer := range configured {
			indexers = append(indexers, indexer.ID)
		}
	}
	ids := make([]string, 0, len(indexers))
	seen := make(map[string]bool, len(indexers))
	for _, id := range indexers {
		if !validIndexerID(id) {
			return response, errors.New("invalid indexer ID")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	query := cloneValues(params)
	query.Del("apikey")
	query.Del("indexer")
	if query.Get("t") == "" {
		query.Set("t", "search")
	}
	if categories := query["cat"]; len(categories) > 1 {
		query.Set("cat", strings.Join(categories, ","))
	}
	results := make([][]Result, len(ids))
	errs := make([]error, len(ids))
	pages := make([]SearchPage, len(ids))
	parallel(len(ids), func(i int) {
		data := &searchData{params: query}
		requestContext := context.WithValue(ctx, searchKey{}, data)
		// Isolating each target preserves every partial failure (Fetch returns
		// only one aggregate error). The outer pool bounds total concurrency.
		request := sdk.NewRawSearch().WithTrackers(ids[i]).Build()
		items, err := c.upstream.Fetch(requestContext, request, sdk.WithoutCapsValidation(), sdk.WithMaxConcurrency(1))
		if err != nil {
			errs[i] = safeError(ctx, err)
			return
		}
		for n, item := range items {
			results[i] = append(results[i], adaptResult(item, ids[i], data, n))
		}
		pages[i] = data.page
	})
	for i, items := range results {
		if errs[i] != nil {
			response.Errors = append(response.Errors, ids[i]+": "+errs[i].Error())
			continue
		}
		response.Results = append(response.Results, items...)
		if response.Pagination == nil {
			response.Pagination = make(map[string]SearchPage, len(ids))
		}
		response.Pagination[ids[i]] = pages[i]
	}
	if ctx.Err() != nil {
		return response, ctx.Err()
	}
	return response, nil
}

func cloneValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, values := range values {
		out[key] = append([]string(nil), values...)
	}
	return out
}

// go-jackett currently drops details/comments, attribute categories, leechers,
// and the distinction between absent and explicit-zero volume factors. Capture
// only those gaps; the dependency owns feed parsing and all other result fields.
type itemMetadata struct {
	Comments   string `xml:"comments"`
	Attributes []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"attr"`
}

type searchKey struct{}
type searchData struct {
	params url.Values
	title  string
	items  []itemMetadata
	page   SearchPage
}

// libraryTransport bridges library URL-construction gaps: reverse-proxy base
// paths, the canonical /api suffix, and arbitrary advertised Torznab parameters.
type libraryTransport struct {
	base   *url.URL
	apiKey string
}

func (t *libraryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r := request.Clone(request.Context())
	u := *request.URL
	if !sameOrigin(t.base, &u) {
		return nil, errors.New("Jackett request changed origin")
	}
	u.Path = strings.TrimRight(t.base.Path, "/") + u.Path
	if strings.HasSuffix(u.Path, "/results/torznab") {
		u.Path += "/api"
	}
	u.RawPath = ""
	data, _ := request.Context().Value(searchKey{}).(*searchData)
	query := u.Query()
	if data != nil {
		query = cloneValues(data.params)
	}
	query.Set("apikey", t.apiKey)
	u.RawQuery = query.Encode()
	r.URL = &u
	response, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError(response, t.apiKey)
	}
	if data == nil {
		return response, nil
	}
	body, err := readBounded(response.Body, response.ContentLength)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	var supplement struct {
		XMLName xml.Name
		Channel struct {
			Title    string         `xml:"title"`
			Items    []itemMetadata `xml:"item"`
			Response *struct {
				Offset *int `xml:"offset,attr"`
				Total  *int `xml:"total,attr"`
			} `xml:"response"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &supplement); err != nil {
		return nil, errors.New("invalid Jackett feed or pagination metadata")
	}
	data.title = supplement.Channel.Title
	data.items = supplement.Channel.Items
	data.page.Count = len(supplement.Channel.Items)
	if page := supplement.Channel.Response; page != nil {
		if (page.Offset != nil && *page.Offset < 0) || (page.Total != nil && *page.Total < 0) {
			return nil, errors.New("invalid Jackett pagination metadata")
		}
		data.page.Offset, data.page.Total = page.Offset, page.Total
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func adaptResult(item sdk.Result, indexer string, data *searchData, position int) Result {
	result := Result{Title: item.Title, Indexer: item.Tracker, IndexerID: indexer, GUID: item.ID, Link: item.Link, Magnet: item.MagnetURI, Size: int64(item.Size), Seeders: int(item.Seeders), Peers: int(item.Peers), Categories: []string{}, DownloadVolumeFactor: 1, UploadVolumeFactor: 1}
	if result.Indexer == "" {
		result.Indexer = data.title
	}
	if result.Indexer == "" {
		result.Indexer = indexer
	}
	if !item.PublishDate.IsZero() {
		result.Published = item.PublishDate.UTC().Format(time.RFC3339)
	}
	for _, category := range item.Categories {
		result.Categories = appendUnique(result.Categories, strconv.FormatUint(uint64(category), 10))
	}
	if position < len(data.items) {
		metadata := data.items[position]
		result.Details = metadata.Comments
		var hasPeers, hasLeechers bool
		var leechers int
		for _, attr := range metadata.Attributes {
			switch strings.ToLower(attr.Name) {
			case "downloadvolumefactor":
				result.DownloadVolumeFactor = parseFactor(attr.Value)
			case "uploadvolumefactor":
				result.UploadVolumeFactor = parseFactor(attr.Value)
			case "category":
				for _, value := range strings.Split(attr.Value, ",") {
					result.Categories = appendUnique(result.Categories, strings.TrimSpace(value))
				}
			case "peers":
				hasPeers = true
			case "leechers":
				var err error
				leechers, err = strconv.Atoi(attr.Value)
				hasLeechers = err == nil
			case "size":
				if result.Size == 0 {
					result.Size, _ = strconv.ParseInt(attr.Value, 10, 64)
				}
			}
		}
		if !hasPeers && hasLeechers {
			result.Peers = result.Seeders + leechers
		}
	}
	if len(result.Link) >= 7 && strings.EqualFold(result.Link[:7], "magnet:") {
		result.Link = ""
	}
	if result.Magnet == "" && validInfoHash(item.InfoHash) {
		result.Magnet = "magnet:?" + url.Values{"xt": {"urn:btih:" + item.InfoHash}, "dn": {result.Title}}.Encode()
	}
	identity := result.GUID
	if identity == "" {
		identity = result.Link
	}
	if identity == "" {
		identity = result.Magnet
	}
	if identity == "" {
		identity = result.Title + "\x00" + strconv.FormatInt(result.Size, 10)
	}
	hash := sha256.Sum256([]byte(indexer + "\x00" + identity))
	result.ID = hex.EncodeToString(hash[:])
	return result
}

func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var upstream *sdk.IndexerError
	if errors.As(err, &upstream) {
		if code, parseErr := strconv.Atoi(upstream.Code); parseErr == nil {
			return fmt.Errorf("Torznab error %d", code)
		}
		return errors.New("Torznab error")
	}
	var status statusError
	if errors.As(err, &status) {
		return status
	}
	if errors.Is(err, errTooLarge) {
		return errTooLarge
	}
	// Library errors can include remote descriptions or URL query strings.
	return errors.New("Jackett request failed (connection, timeout, or invalid response)")
}

var errTooLarge = errors.New("Jackett response exceeds 16 MiB")

func readBounded(body io.Reader, contentLength int64) ([]byte, error) {
	if contentLength > maxResponseBytes {
		return nil, errTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("cannot read Jackett response")
	}
	if len(data) > maxResponseBytes {
		return nil, errTooLarge
	}
	return data, nil
}

// Download contains exactly one of torrent bytes or a resolved magnet URI.
type Download struct {
	Torrent []byte
	Magnet  string
}

// ResolveDownload follows up to five redirects within the configured Jackett
// origin/base path. Magnet redirects are returned, never fetched over HTTP.
func (c *Client) ResolveDownload(ctx context.Context, link string) (Download, error) {
	if c.base == nil {
		return Download{}, errors.New("invalid Jackett base URL")
	}
	if link == "" {
		return Download{}, errors.New("no Jackett download URL")
	}
	u, err := url.Parse(link)
	if err != nil {
		return Download{}, errors.New("invalid Jackett download URL")
	}
	base := *c.base
	base.Path = strings.TrimRight(base.Path, "/") + "/"
	base.RawPath = ""
	u = base.ResolveReference(u)
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	for redirects := 0; ; redirects++ {
		if u.User != nil || u.Fragment != "" || !sameOrigin(c.base, u) || !withinBasePath(c.base.Path, u.Path) {
			return Download{}, errors.New("download URL is outside the configured Jackett origin or path")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return Download{}, errors.New("cannot create Jackett download request")
		}
		response, err := c.http.Do(req)
		if err != nil {
			return Download{}, safeError(ctx, err)
		}
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return Download{}, errors.New("Jackett download redirect has no location")
			}
			next, err := url.Parse(location)
			if err != nil {
				return Download{}, errors.New("invalid Jackett download redirect")
			}
			if strings.EqualFold(next.Scheme, "magnet") {
				if next.Opaque != "" || next.Host != "" || next.Path != "" || next.Fragment != "" || next.Query().Get("xt") == "" {
					return Download{}, errors.New("invalid Jackett magnet redirect")
				}
				return Download{Magnet: next.String()}, nil
			}
			if redirects >= 5 {
				return Download{}, errors.New("Jackett download exceeded five redirects")
			}
			u = u.ResolveReference(next)
		default:
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return Download{}, responseError(response, c.apiKey)
			}
			data, err := readBounded(response.Body, response.ContentLength)
			response.Body.Close()
			return Download{Torrent: data}, err
		}
	}
}

func parallel(count int, fn func(int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(count, maxConcurrency) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := range count {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func validIndexerID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func parseFactor(value string) float64 {
	factor, err := strconv.ParseFloat(value, 64)
	if err != nil || factor < 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return 1
	}
	return factor
}

func validInfoHash(hash string) bool {
	if len(hash) == 40 {
		_, err := hex.DecodeString(hash)
		return err == nil
	}
	if len(hash) != 32 {
		return false
	}
	for _, char := range hash {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '2' && char <= '7') {
			return false
		}
	}
	return true
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if port := u.Port(); port != "" {
			return port
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func withinBasePath(base, candidate string) bool {
	if strings.Contains(candidate, "\\") {
		return false
	}
	for _, part := range strings.Split(candidate, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	base = strings.TrimRight(base, "/")
	return base == "" || candidate == base || strings.HasPrefix(candidate, base+"/")
}
