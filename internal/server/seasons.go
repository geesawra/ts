package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ts/internal/jackett"
	"ts/internal/omdb"
)

type seasonMetadata interface {
	Search(context.Context, string) ([]omdb.Series, error)
	Series(context.Context, string) (omdb.Series, error)
	Season(context.Context, string, int) ([]omdb.Episode, error)
}

type seasonSearch struct {
	series omdb.Series
	season int
}
type aggregationInfo struct {
	Pages   int  `json:"pages"`
	Partial bool `json:"partial"`
}
type seasonCandidate struct {
	ID     string        `json:"id"`
	Parsed *releaseParts `json:"parsed"`
}
type seasonGroup struct {
	Key              string            `json:"key"`
	Show             string            `json:"show"`
	Season           int               `json:"season"`
	Candidates       []seasonCandidate `json:"candidates"`
	Expected         []omdb.Episode    `json:"expected"`
	Missing          []int             `json:"missing"`
	CoverageComplete bool              `json:"coverage_complete"`
	MetadataError    string            `json:"metadata_error"`
}

var imdbSeriesID = regexp.MustCompile(`^tt[0-9]{7,10}$`)

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	if s.metadata == nil {
		fail(w, 404, "OMDb is not configured")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 256 {
		fail(w, 400, "Enter a series title (up to 256 characters)")
		return
	}
	series, err := s.metadata.Search(r.Context(), q)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{"series": series})
}

func (s *Server) prepareSeasonSearch(w http.ResponseWriter, r *http.Request, params url.Values) (*seasonSearch, bool) {
	if r.URL.Query().Get("complete_seasons") == "" {
		return nil, true
	}
	if r.URL.Query().Get("complete_seasons") != "1" {
		fail(w, 400, "Invalid complete_seasons option")
		return nil, false
	}
	if s.metadata == nil {
		fail(w, 400, "OMDb is not configured")
		return nil, false
	}
	id := r.URL.Query().Get("series_id")
	if !imdbSeriesID.MatchString(id) {
		fail(w, 400, "Select an IMDb series before searching complete seasons")
		return nil, false
	}
	if params.Get("t") != "search" && params.Get("t") != "tvsearch" {
		fail(w, 400, "Season search requires general or TV search mode")
		return nil, false
	}
	series, err := s.metadata.Series(r.Context(), id)
	if err != nil {
		fail(w, 502, err.Error())
		return nil, false
	}
	if series.TotalSeasons < 1 || series.TotalSeasons > 100 {
		fail(w, 422, "OMDb must provide between 1 and 100 seasons for this series")
		return nil, false
	}
	selected := 0
	if text := params.Get("season"); text != "" {
		selected, err = strconv.Atoi(text)
		if err != nil || selected < 1 || selected > series.TotalSeasons {
			fail(w, 400, "Season is outside the series season range")
			return nil, false
		}
	}
	params.Set("q", series.Title)
	// IDs unsupported by individual trackers must not compete with the resolved title.
	for _, key := range []string{"imdbid", "tvdbid", "tmdbid", "tvmazeid", "rid", "tvrageid", "traktid", "ep"} {
		params.Del(key)
	}
	return &seasonSearch{series: series, season: selected}, true
}

// Fetch bounded pages per tracker, deduplicating stable upstream IDs. A tracker
// that repeats a full page or fails cannot be reported as fully searched.
func (s *Server) aggregateSeasons(ctx context.Context, indexers []string, params url.Values) (jackett.SearchResponse, aggregationInfo, error) {
	response := jackett.SearchResponse{Results: []jackett.Result{}, Errors: []string{}}
	info := aggregationInfo{}
	if len(indexers) == 0 {
		configured, err := s.jackett.Indexers(ctx)
		if err != nil {
			return response, info, err
		}
		for _, indexer := range configured {
			indexers = append(indexers, indexer.ID)
		}
	}
	seen := map[string]bool{}
	visited := map[string]bool{}
	query := url.Values{}
	for key, values := range params {
		query[key] = append([]string(nil), values...)
	}
	query.Set("limit", "100")
	for _, indexer := range indexers {
		if visited[indexer] {
			continue
		}
		visited[indexer] = true
		for page := range 10 {
			if info.Pages >= 50 || len(response.Results) >= 10000 {
				info.Partial = true
				response.Errors = append(response.Errors, "Season aggregation limit reached; tracker coverage is partial")
				return response, info, nil
			}
			query.Set("offset", strconv.Itoa(page*100))
			result, err := s.jackett.Search(ctx, []string{indexer}, query)
			info.Pages++
			if err != nil {
				return response, info, err
			}
			if len(result.Errors) > 0 {
				info.Partial = true
				response.Errors = append(response.Errors, result.Errors...)
				break
			}
			added := 0
			for _, item := range result.Results {
				if !seen[item.ID] {
					seen[item.ID] = true
					response.Results = append(response.Results, item)
					added++
				}
			}
			if len(result.Results) < 100 {
				break
			}
			if added == 0 {
				info.Partial = true
				response.Errors = append(response.Errors, indexer+": repeated result page; tracker coverage is partial")
				break
			}
			if page == 9 {
				info.Partial = true
				response.Errors = append(response.Errors, indexer+": page limit reached; tracker coverage is partial")
			}
		}
	}
	if ctx.Err() != nil {
		return response, info, ctx.Err()
	}
	return response, info, nil
}

func (s *Server) buildSeasonGroups(ctx context.Context, request *seasonSearch, rows []publicResult) []seasonGroup {
	first, last := 1, request.series.TotalSeasons
	if request.season > 0 {
		first = request.season
		last = first
	}
	groups := make([]seasonGroup, 0, last-first+1)
	titleKey := normalizeShow(request.series.Title)
	year := strings.FieldsFunc(request.series.Year, func(r rune) bool { return r < '0' || r > '9' })
	yearKey := ""
	if len(year) > 0 {
		yearKey = titleKey + " " + year[0]
	}
	candidates := make(map[int][]seasonCandidate)
	coverage := make(map[int]map[int]bool)
	for _, row := range rows {
		parsed := parseRelease(row.Title)
		if parsed == nil || parsed.Season < first || parsed.Season > last {
			continue
		}
		key := normalizeShow(parsed.Show)
		if key != titleKey && key != yearKey {
			continue
		}
		candidates[parsed.Season] = append(candidates[parsed.Season], seasonCandidate{ID: row.ID, Parsed: parsed})
		if coverage[parsed.Season] == nil {
			coverage[parsed.Season] = make(map[int]bool)
		}
		if row.HasMagnet || row.HasTorrent {
			for _, episode := range parsed.Episodes {
				coverage[parsed.Season][episode] = true
			}
		}
	}
	for season := first; season <= last; season++ {
		group := seasonGroup{Key: fmt.Sprintf("%s|%d", request.series.ID, season), Show: request.series.Title, Season: season, Candidates: candidates[season], Expected: []omdb.Episode{}, Missing: []int{}}
		if group.Candidates == nil {
			group.Candidates = []seasonCandidate{}
		}
		expected, err := s.metadata.Season(ctx, request.series.ID, season)
		if err != nil {
			group.MetadataError = err.Error()
		} else if len(expected) == 0 {
			group.MetadataError = "OMDb returned no episode inventory"
		} else {
			group.Expected = expected
			for _, episode := range expected {
				if !coverage[season][episode.Number] {
					group.Missing = append(group.Missing, episode.Number)
				}
			}
			sort.Ints(group.Missing)
			group.CoverageComplete = len(group.Missing) == 0
		}
		groups = append(groups, group)
	}
	return groups
}
