package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ts/internal/jackett"
)

func TestSeasonSearchDiscoversOlderEpisodesBeyondBroadTrackerResults(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
		item := func(guid, title string, seeds int) {
			fmt.Fprintf(w, `<item><title>%s</title><guid>%s</guid><torznab:attr name="seeders" value="%d"/><torznab:attr name="infohash" value="0123456789012345678901234567890123456789"/></item>`, title, guid, seeds)
		}
		switch q.Get("q") {
		case "Show Name", "Show Name S01":
			// The tracker exposes a bounded latest-results window and ignores
			// season-only filtering, like EZTV. Offset exhaustion is not full
			// coverage of the series.
			if offset == 0 {
				item("recent", "Show.Name.S02E01.1080p-RECENT", 100)
			}
		case "Show Name S01E01":
			if offset == 0 {
				item("e1", "Show.Name.S01E01.1080p-PRIMARY", 1)
				item("alternative", "Show.Name.S01E01.1080p-OTHER", 999)
			}
		case "Show Name S01E02":
			if offset == 0 {
				// An overlapping result on the first page must not hide the
				// requested episode on the next, short page.
				item("e1", "Show.Name.S01E01.1080p-PRIMARY", 1)
			} else if offset == 1 {
				item("e2", "Show.Name.S01E02.1080p-PRIMARY", 1)
			}
		default:
			t.Errorf("unexpected search: %s", q.Get("q"))
		}
		io.WriteString(w, `</channel></rss>`)
	}))
	defer upstream.Close()
	s := &Server{cfg: testConfig(upstream.URL), jackett: jackett.New(upstream.URL, "private-key", 0), metadata: fixtureMetadata{}, results: map[string]cachedResult{}}
	response := httptest.NewRecorder()
	s.search(response, httptest.NewRequest("GET", "/api/search?indexer=test&t=search&q=Show+Name&complete_seasons=1&series_id=tt1234567&season=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("search failed: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Results     []publicResult  `json:"results"`
		Seasons     []seasonGroup   `json:"seasons"`
		Aggregation aggregationInfo `json:"aggregation"`
		Errors      []string        `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Aggregation.Partial || len(body.Errors) != 0 || len(body.Results) != 4 || len(body.Seasons) != 1 {
		t.Fatalf("discovery lost or duplicated releases: %s", response.Body.String())
	}
	group := body.Seasons[0]
	if !group.CoverageComplete || len(group.Missing) != 0 || len(group.Candidates) != 3 || group.PreferredReleaseGroup != "PRIMARY" {
		t.Fatalf("older season was not assembled: %#v", group)
	}
	byID := make(map[string]publicResult)
	for _, result := range body.Results {
		byID[result.ID] = result
	}
	if len(group.RecommendedIDs) != 2 {
		t.Fatalf("expected one PRIMARY release per episode: %#v", group.RecommendedIDs)
	}
	for _, id := range group.RecommendedIDs {
		parsed := parseRelease(byID[id].Title)
		if parsed == nil || parsed.ReleaseGroup != "PRIMARY" {
			t.Fatalf("most widespread release group was not selected: %#v", byID[id])
		}
	}
}
