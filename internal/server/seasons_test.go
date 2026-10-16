package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"ts/internal/jackett"
	"ts/internal/omdb"
)

func TestReleaseRecognition(t *testing.T) {
	cases := []struct {
		title    string
		season   int
		episodes []int
		pack     bool
		group    string
	}{
		{"Show.Name.S02E03.1080p.WEB-DL-GROUP", 2, []int{3}, false, "GROUP"},
		{"Show Name 2x03-05 720p HDTV-TEAM", 2, []int{3, 4, 5}, false, "TEAM"},
		{"Show.Name.S02E03E04.1080p.WEB-DL-GROUP", 2, []int{3, 4}, false, "GROUP"},
		{"Show.Name.S02E03-E05.1080p.WEB-DL-GROUP", 2, []int{3, 4, 5}, false, "GROUP"},
		{"Show.Name.S00E01.1080p-WEB", 0, []int{1}, false, "WEB"},
		{"Show.Name.S02.Complete.1080p.WEB-DL", 2, []int{}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			parsed := parseRelease(tc.title)
			if parsed == nil || parsed.Show != "Show Name" || parsed.Season != tc.season || !reflect.DeepEqual(parsed.Episodes, tc.episodes) || parsed.Pack != tc.pack || parsed.ReleaseGroup != tc.group {
				t.Fatalf("wrong release interpretation: %#v", parsed)
			}
		})
	}
	for _, title := range []string{"Movie.2024.1080p-GRP", "Show.S01E01foo", "Show.S01-S03.Complete", "Show.S01E04-E01", "Show.S01E01-E999", "Show.S01E01.S02E01"} {
		if parsed := parseRelease(title); parsed != nil {
			t.Errorf("ambiguous title %q interpreted as %#v", title, parsed)
		}
	}
}

type fixtureMetadata struct{ fail bool }

func (fixtureMetadata) Search(context.Context, string) ([]omdb.Series, error) {
	return []omdb.Series{{ID: "tt1234567", Title: "Show Name", Year: "2020", TotalSeasons: 2}}, nil
}
func (fixtureMetadata) Series(context.Context, string) (omdb.Series, error) {
	return omdb.Series{ID: "tt1234567", Title: "Show Name", Year: "2020", TotalSeasons: 2}, nil
}
func (m fixtureMetadata) Season(_ context.Context, _ string, n int) ([]omdb.Episode, error) {
	if m.fail {
		return nil, errors.New("metadata unavailable")
	}
	return []omdb.Episode{{Number: 1, Title: "One", Released: "2020-01-01"}, {Number: 2, Title: "Two", Released: "2020-01-08"}}, nil
}

func TestSeasonCoverageRequiresInventoryAndDownloadableEpisodes(t *testing.T) {
	s := &Server{metadata: fixtureMetadata{}}
	request := &seasonSearch{series: omdb.Series{ID: "tt1234567", Title: "Show Name", Year: "2020", TotalSeasons: 2}}
	rows := []publicResult{
		{ID: "pack", Title: "Show.Name.S01.Complete.1080p-GRP", HasMagnet: true},
		{ID: "e1", Title: "Show.Name.S01E01.1080p-GRP", HasMagnet: true},
		{ID: "dead", Title: "Show.Name.S01E02.1080p-GRP"},
		{ID: "wrong", Title: "Other.Show.S01E02.1080p-GRP", HasMagnet: true},
		{ID: "range", Title: "Show.Name.2020.S02E01-E02.1080p-GRP", HasMagnet: true},
	}
	groups := s.buildSeasonGroups(context.Background(), request, rows)
	if groups[0].CoverageComplete || !reflect.DeepEqual(groups[0].Missing, []int{2}) || len(groups[0].Candidates) != 3 {
		t.Fatalf("pack/non-downloadable/wrong series must not fill coverage: %#v", groups[0])
	}
	if !groups[1].CoverageComplete || len(groups[1].Missing) != 0 || len(groups[1].Candidates) != 1 {
		t.Fatalf("multi-episode coverage lost: %#v", groups[1])
	}
	s.metadata = fixtureMetadata{fail: true}
	groups = s.buildSeasonGroups(context.Background(), request, rows)
	if groups[1].CoverageComplete || groups[1].MetadataError == "" {
		t.Fatalf("metadata failure claimed complete: %#v", groups[1])
	}
}

func TestAggregationFetchesNextPageAndStopsRepeatedPages(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprint("repeat=", repeat), func(t *testing.T) {
			offsets := []string{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				offsets = append(offsets, q.Get("offset"))
				if q.Get("limit") != "100" || q.Get("complete_seasons") != "" {
					t.Errorf("incorrect tracker parameters: %s", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, "<rss><channel>")
				if q.Get("offset") == "0" || repeat {
					for n := range 100 {
						fmt.Fprintf(w, "<item><title>Show.Name.S01E01-GRP</title><guid>item%d</guid></item>", n)
					}
				} else {
					io.WriteString(w, "<item><title>Show.Name.S01E02-GRP</title><guid>next</guid></item>")
				}
				io.WriteString(w, "</channel></rss>")
			}))
			defer upstream.Close()
			s := &Server{jackett: jackett.New(upstream.URL, "private-key", 0)}
			response, info, err := s.aggregateSeasons(context.Background(), []string{"test"}, url.Values{"q": {"Show Name"}, "t": {"search"}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(offsets, []string{"0", "100"}) {
				t.Fatalf("incorrect page traversal: %v", offsets)
			}
			if repeat {
				if !info.Partial || len(response.Results) != 100 || len(response.Errors) == 0 {
					t.Fatalf("repeated page not flagged: %#v", info)
				}
			} else if info.Partial || len(response.Results) != 101 {
				t.Fatalf("second page lost: %d %#v", len(response.Results), info)
			}
		})
	}
}

func TestSeasonSearchOptionalAndDownloadTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("complete_seasons") != "" || r.URL.Query().Get("series_id") != "" {
			t.Error("application options leaked to tracker")
		}
		if r.URL.Query().Get("q") != "Show Name" {
			t.Error("series title was not resolved")
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Show.Name.S01E01-E02.1080p-GROUP</title><guid>one</guid><torznab:attr name="infohash" value="0123456789012345678901234567890123456789"/></item></channel></rss>`)
	}))
	defer upstream.Close()
	s := &Server{cfg: testConfig(upstream.URL), jackett: jackett.New(upstream.URL, "private-key", 0), results: map[string]cachedResult{}}
	request := httptest.NewRequest("GET", "/api/search?indexer=test&t=search&q=show&complete_seasons=1&series_id=tt1234567&season=1", nil)
	disabled := httptest.NewRecorder()
	s.search(disabled, request)
	if disabled.Code != 400 || !strings.Contains(disabled.Body.String(), "not configured") {
		t.Fatalf("disabled season feature accepted: %s", disabled.Body.String())
	}
	s.metadata = fixtureMetadata{}
	response := httptest.NewRecorder()
	s.search(response, request)
	if response.Code != 200 {
		t.Fatalf("season search: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Results     []publicResult  `json:"results"`
		Seasons     []seasonGroup   `json:"seasons"`
		Aggregation aggregationInfo `json:"aggregation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Results) != 1 || len(body.Seasons) != 1 || body.Aggregation.Pages != 1 {
		t.Fatalf("missing season response: %s", response.Body.String())
	}
	if !body.Seasons[0].CoverageComplete || len(body.Seasons[0].Missing) != 0 {
		t.Fatalf("season inventory should be covered by the multi-episode release: %s", response.Body.String())
	}
	id := body.Seasons[0].Candidates[0].ID
	magnetResponse := httptest.NewRecorder()
	s.magnet(magnetResponse, httptest.NewRequest("GET", "/api/magnet?id="+id, nil))
	var downloaded struct {
		Magnet string `json:"magnet"`
	}
	if err := json.Unmarshal(magnetResponse.Body.Bytes(), &downloaded); err != nil {
		t.Fatal(err)
	}
	if magnetResponse.Code != 200 || !strings.Contains(downloaded.Magnet, "urn%3Abtih%3A0123456789012345678901234567890123456789") {
		t.Fatalf("selected season component could not be downloaded: %d %s", magnetResponse.Code, magnetResponse.Body.String())
	}
}
