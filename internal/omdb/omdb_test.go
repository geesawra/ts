package omdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const seriesJSON = `{"Response":"True","imdbID":"tt1234567","Title":"Example Show","Year":"2020–","Type":"series","totalSeasons":"2"}`
const searchJSON = `{"Response":"True","totalResults":"1","Search":[{"imdbID":"tt1234567","Title":"Example Show","Year":"2020–","Type":"series"}]}`
const seasonJSON = `{"Response":"True","Title":"Example Show","Season":"1","totalSeasons":"2","Episodes":[{"Episode":"2","Title":"Second","Released":"N/A","imdbID":"tt1234569"},{"Episode":"1","Title":"First","Released":"2020-01-02","imdbID":"tt1234568"}]}`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := New("private-omdb-key", time.Second)
	client.endpoint = server.URL
	client.http = server.Client()
	return client
}

func TestDisabledAndInvalidRequests(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, seriesJSON)
	})
	for _, key := range []string{"", "  "} {
		disabled := New(key, 0)
		disabled.endpoint = client.endpoint
		if disabled.timeout != 60*time.Second {
			t.Fatal("omitted timeout did not default")
		}
		if _, err := disabled.Search(context.Background(), "Example"); err == nil {
			t.Fatal("disabled search succeeded")
		}
		if _, err := disabled.Series(context.Background(), "tt1234567"); err == nil {
			t.Fatal("disabled series lookup succeeded")
		}
		if _, err := disabled.Season(context.Background(), "tt1234567", 1); err == nil {
			t.Fatal("disabled season lookup succeeded")
		}
	}
	if _, err := client.Search(context.Background(), "  "); err == nil {
		t.Fatal("blank search succeeded")
	}
	for _, id := range []string{"", "tt123", "tt1234567&apikey=other", "1234567"} {
		if _, err := client.Series(context.Background(), id); err == nil {
			t.Fatal("invalid series ID succeeded")
		}
		if _, err := client.Season(context.Background(), id, 1); err == nil {
			t.Fatal("invalid season ID succeeded")
		}
	}
	for _, number := range []int{0, -1} {
		if _, err := client.Season(context.Background(), "tt1234567", number); err == nil {
			t.Fatal("invalid season number succeeded")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("disabled or invalid request contacted upstream")
	}
}

func TestSearchAndMetadataRequests(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query := r.URL.Query()
		if query.Get("apikey") != "private-omdb-key" || r.Method != http.MethodGet {
			t.Error("incorrect authentication or method")
		}
		switch {
		case query.Has("s"):
			if query.Get("s") != "Example & Show" || query.Get("type") != "series" || query.Has("i") {
				t.Error("incorrect series search parameters")
			}
			io.WriteString(w, searchJSON)
		case query.Has("Season"):
			if query.Get("i") != "tt1234567" || query.Get("Season") != "1" {
				t.Error("incorrect season lookup parameters")
			}
			io.WriteString(w, seasonJSON)
		default:
			if query.Get("i") != "tt1234567" || query.Get("type") != "series" {
				t.Error("incorrect series lookup parameters")
			}
			io.WriteString(w, seriesJSON)
		}
	})
	ctx := context.Background()
	matches, err := client.Search(ctx, " Example & Show ")
	if err != nil || len(matches) != 1 || matches[0].ID != "tt1234567" || matches[0].TotalSeasons != 0 {
		t.Fatalf("unexpected candidates: %#v, %v", matches, err)
	}
	series, err := client.Series(ctx, matches[0].ID)
	if err != nil || series.Title != "Example Show" || series.TotalSeasons != 2 {
		t.Fatalf("unexpected series: %#v, %v", series, err)
	}
	episodes, err := client.Season(ctx, series.ID, 1)
	if err != nil || len(episodes) != 2 || episodes[0].Number != 1 || episodes[1].Number != 2 || episodes[1].Released != "N/A" {
		t.Fatalf("unexpected episode inventory: %#v, %v", episodes, err)
	}
	// Returned slices must not let callers corrupt cached metadata.
	matches[0].Title = "changed"
	episodes[0].Number = 99
	matches, err = client.Search(ctx, "Example & Show")
	if err != nil || matches[0].Title != "Example Show" {
		t.Fatal("search cache was changed by its caller")
	}
	episodes, err = client.Season(ctx, series.ID, 1)
	if err != nil || episodes[0].Number != 1 {
		t.Fatal("season cache was changed by its caller")
	}
	matches[0].Title = "changed again"
	episodes[0].Number = 98
	matches, _ = client.Search(ctx, "Example & Show")
	episodes, _ = client.Season(ctx, series.ID, 1)
	if matches[0].Title != "Example Show" || episodes[0].Number != 1 {
		t.Fatal("cache hit returned mutable backing storage")
	}
	if _, err := client.Series(ctx, series.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected one request per metadata lookup, got %d", calls.Load())
	}
}

func TestRejectMalformedSeriesMetadata(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"false", `{"Response":"False","Error":"private-omdb-key"}`},
		{"missing response", strings.Replace(seriesJSON, `"Response":"True",`, "", 1)},
		{"wrong ID", strings.Replace(seriesJSON, "tt1234567", "tt7654321", 1)},
		{"invalid ID", strings.Replace(seriesJSON, "tt1234567", "bad", 1)},
		{"wrong type", strings.Replace(seriesJSON, `"series"`, `"movie"`, 1)},
		{"missing title", strings.Replace(seriesJSON, "Example Show", "", 1)},
		{"missing year", strings.Replace(seriesJSON, "2020–", "", 1)},
		{"missing seasons", strings.Replace(seriesJSON, `,"totalSeasons":"2"`, "", 1)},
		{"unknown seasons", strings.Replace(seriesJSON, `"totalSeasons":"2"`, `"totalSeasons":"N/A"`, 1)},
		{"zero seasons", strings.Replace(seriesJSON, `"totalSeasons":"2"`, `"totalSeasons":"0"`, 1)},
		{"negative seasons", strings.Replace(seriesJSON, `"totalSeasons":"2"`, `"totalSeasons":"-2"`, 1)},
		{"wrong season type", strings.Replace(seriesJSON, `"totalSeasons":"2"`, `"totalSeasons":2`, 1)},
		{"overflow seasons", strings.Replace(seriesJSON, `"totalSeasons":"2"`, `"totalSeasons":"999999999999999999999999"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) })
			if _, err := client.Series(context.Background(), "tt1234567"); err == nil {
				t.Fatal("invalid series metadata succeeded")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatal("upstream error leaked the API key")
			}
		})
	}
}

func TestRejectMalformedSearchCandidates(t *testing.T) {
	for _, body := range []string{
		`{"Response":"False","Error":"private-omdb-key"}`,
		`{"Response":"True","Search":[]}`,
		`{"Response":"True"}`,
		strings.Replace(searchJSON, "tt1234567", "bad", 1),
		strings.Replace(searchJSON, `"series"`, `"movie"`, 1),
		strings.Replace(searchJSON, "Example Show", "", 1),
	} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, err := client.Search(context.Background(), "Example"); err == nil {
			t.Fatal("invalid search candidates succeeded")
		} else if strings.Contains(err.Error(), "private-") {
			t.Fatal("upstream error leaked the API key")
		}
	}
}

func searchPage(total, first, count int) string {
	items := make([]string, 0, count)
	for i := first; i < first+count; i++ {
		items = append(items, fmt.Sprintf(`{"imdbID":"tt%07d","Title":"Show %d","Year":"2020","Type":"series"}`, i, i))
	}
	return fmt.Sprintf(`{"Response":"True","totalResults":"%d","Search":[%s]}`, total, strings.Join(items, ","))
}

func TestSearchReturnsLaterPageMatchesAndCachesCompleteResult(t *testing.T) {
	for _, total := range []int{10, 11, 20, 21, 1000} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				page := int(calls.Add(1))
				if r.URL.Query().Get("page") != fmt.Sprint(page) {
					t.Error("unexpected search page")
				}
				io.WriteString(w, searchPage(total, (page-1)*10+1, min(10, total-(page-1)*10)))
			})
			matches, err := client.Search(context.Background(), "Show")
			if err != nil || len(matches) != total {
				t.Fatalf("incomplete candidates: got %d, want %d: %v", len(matches), total, err)
			}
			for i, match := range matches {
				if match.ID != fmt.Sprintf("tt%07d", i+1) {
					t.Fatalf("missing or reordered match %d: %#v", i+1, match)
				}
			}
			matches[total-1].ID = "changed"
			cached, err := client.Search(context.Background(), "Show")
			if err != nil || len(cached) != total || cached[total-1].ID != fmt.Sprintf("tt%07d", total) {
				t.Fatalf("complete cached result was corrupted: %#v, %v", cached, err)
			}
			if calls.Load() != int32((total+9)/10) {
				t.Fatalf("search fetched beyond the final page or missed the cache: %d calls", calls.Load())
			}
		})
	}
}

func TestSearchDeduplicatesOverlappingPages(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := calls.Add(1)
		if page == 1 {
			io.WriteString(w, searchPage(20, 1, 10))
		} else {
			io.WriteString(w, searchPage(20, 10, 10))
		}
	})
	matches, err := client.Search(context.Background(), "Show")
	if err != nil || len(matches) != 19 {
		t.Fatalf("unexpected deduplicated matches: %#v, %v", matches, err)
	}
	for i, match := range matches {
		if match.ID != fmt.Sprintf("tt%07d", i+1) {
			t.Fatalf("missing or repeated candidate: %#v", matches)
		}
	}
}

func TestIncompleteSearchIsNotReturnedOrCached(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
	}{
		{"repeated page", searchPage(20, 1, 10), searchPage(20, 1, 10)},
		{"truncated first page", searchPage(20, 1, 9), ""},
		{"truncated last page", searchPage(20, 1, 10), searchPage(20, 11, 9)},
		{"empty later page", searchPage(20, 1, 10), searchPage(20, 11, 0)},
		{"inconsistent count", searchPage(20, 1, 10), searchPage(21, 11, 10)},
		{"missing count", strings.Replace(searchJSON, `"totalResults":"1",`, "", 1), ""},
		{"invalid count", strings.Replace(searchJSON, `"totalResults":"1"`, `"totalResults":"private-omdb-key"`, 1), ""},
		{"zero count", searchPage(0, 1, 1), ""},
		{"overflow count", strings.Replace(searchJSON, `"totalResults":"1"`, `"totalResults":"9999999999999999999999"`, 1), ""},
		{"too broad", searchPage(1001, 1, 10), ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var repaired atomic.Bool
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if repaired.Load() {
					io.WriteString(w, searchJSON)
				} else if r.URL.Query().Get("page") == "1" {
					io.WriteString(w, test.first)
				} else {
					io.WriteString(w, test.second)
				}
			})
			matches, err := client.Search(context.Background(), "Show")
			if err == nil || matches != nil {
				t.Fatalf("incomplete search returned success or partial matches: %#v, %v", matches, err)
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), client.endpoint) {
				t.Fatalf("unsafe search error: %v", err)
			}
			if test.name == "too broad" && (!strings.Contains(err.Error(), "narrow") || calls.Load() != 1) {
				t.Fatalf("unreachable results were not rejected immediately with narrowing guidance: %v, %d calls", err, calls.Load())
			}
			before := calls.Load()
			repaired.Store(true)
			matches, err = client.Search(context.Background(), "Show")
			if err != nil || len(matches) != 1 || matches[0].ID != "tt1234567" || calls.Load() != before+1 {
				t.Fatalf("failed search was cached instead of retried: %#v, %v", matches, err)
			}
		})
	}
}

func TestLaterSearchPageErrorsAreSafeAndNotCached(t *testing.T) {
	for _, failure := range []string{"authentication response", "HTTP authentication", "transport"} {
		t.Run(failure, func(t *testing.T) {
			client := New("private-omdb-key", time.Second)
			var calls int
			repaired := false
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body := searchPage(11, 1, 10)
				status := http.StatusOK
				if r.URL.Query().Get("page") == "2" {
					body = searchPage(11, 11, 1)
					if !repaired {
						switch failure {
						case "authentication response":
							body = `{"Response":"False","Error":"private-omdb-key invalid"}`
						case "HTTP authentication":
							status = http.StatusUnauthorized
							body = "private-omdb-key invalid"
						case "transport":
							return nil, fmt.Errorf("transport failed for %s", r.URL)
						}
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			matches, err := client.Search(context.Background(), "Show")
			if err == nil || matches != nil {
				t.Fatalf("later page failure returned partial success: %#v, %v", matches, err)
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "omdbapi.com") {
				t.Fatalf("unsafe later page error: %v", err)
			}
			repaired = true
			matches, err = client.Search(context.Background(), "Show")
			if err != nil || len(matches) != 11 || matches[10].ID != "tt0000011" || calls != 4 {
				t.Fatalf("later page failure was cached or last match lost: %#v, %v (%d calls)", matches, err, calls)
			}
		})
	}
}

func TestSearchTimeoutBoundsAllPages(t *testing.T) {
	client := New("private-omdb-key", 200*time.Millisecond)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		timer := time.NewTimer(120 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
		body := searchPage(11, 1, 10)
		if r.URL.Query().Get("page") == "2" {
			body = searchPage(11, 11, 1)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	matches, err := client.Search(context.Background(), "Show")
	if !errors.Is(err, context.DeadlineExceeded) || matches != nil {
		t.Fatalf("multi-page search exceeded its whole-operation timeout: %#v, %v", matches, err)
	}
}

func TestRejectIncompleteSeasonInventory(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"false", `{"Response":"False","Error":"private-omdb-key"}`},
		{"empty inventory", `{"Response":"True","Title":"Example Show","Season":"1","totalSeasons":"2","Episodes":[]}`},
		{"missing inventory", `{"Response":"True","Title":"Example Show","Season":"1","totalSeasons":"2"}`},
		{"wrong season", strings.Replace(seasonJSON, `"Season":"1"`, `"Season":"2"`, 1)},
		{"invalid season total", strings.Replace(seasonJSON, `"totalSeasons":"2"`, `"totalSeasons":"0"`, 1)},
		{"duplicate episode", strings.Replace(seasonJSON, `"Episode":"2"`, `"Episode":"1"`, 1)},
		{"duplicate episode ID", strings.Replace(seasonJSON, "tt1234569", "tt1234568", 1)},
		{"missing episode", strings.Replace(seasonJSON, `"Episode":"2"`, `"Episode":"3"`, 1)},
		{"missing first episode", strings.Replace(seasonJSON, `"Episode":"1"`, `"Episode":"3"`, 1)},
		{"zero episode", strings.Replace(seasonJSON, `"Episode":"2"`, `"Episode":"0"`, 1)},
		{"bad episode number", strings.Replace(seasonJSON, `"Episode":"2"`, `"Episode":"x"`, 1)},
		{"missing title", strings.Replace(seasonJSON, "Second", "", 1)},
		{"missing release date", strings.Replace(seasonJSON, "2020-01-02", "", 1)},
		{"bad release date", strings.Replace(seasonJSON, "2020-01-02", "2020-02-31", 1)},
		{"invalid episode ID", strings.Replace(seasonJSON, "tt1234569", "bad", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, test.body) })
			if _, err := client.Season(context.Background(), "tt1234567", 1); err == nil {
				t.Fatal("invalid or incomplete inventory succeeded")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatal("upstream error leaked the API key")
			}
		})
	}
}

func TestResponseBoundsAndSafeErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"malformed JSON", http.StatusOK, `{"private-omdb-key":`},
		{"trailing JSON", http.StatusOK, seriesJSON + `{}`},
		{"large body", http.StatusOK, seriesJSON + strings.Repeat(" ", maxResponseBody)},
		{"HTTP failure", http.StatusForbidden, "private-omdb-key upstream failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			})
			if _, err := client.Series(context.Background(), "tt1234567"); err == nil {
				t.Fatal("expected request failure")
			} else if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), client.endpoint) {
				t.Fatal("error disclosed upstream details")
			}
		})
	}
	client := New("private-omdb-key", time.Second)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport failed for %s", r.URL)
	})
	if _, err := client.Series(context.Background(), "tt1234567"); err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "omdbapi.com") {
		t.Fatalf("unsafe transport error: %v", err)
	}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: brokenBody{}, Header: make(http.Header)}, nil
	})
	if _, err := client.Series(context.Background(), "tt1234567"); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("unsafe body error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("private-omdb-key") }
func (brokenBody) Close() error             { return nil }

func TestRedirectDoesNotForwardKey(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Store(true)
		io.WriteString(w, seriesJSON)
	}))
	defer target.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/?apikey=private-omdb-key", http.StatusFound)
	})
	// Retain New's redirect policy while using httptest's transport.
	client.http.CheckRedirect = New("key", time.Second).http.CheckRedirect
	if _, err := client.Series(context.Background(), "tt1234567"); err == nil {
		t.Fatal("redirect succeeded")
	}
	if forwarded.Load() {
		t.Fatal("API key was sent to the redirect target")
	}
}

func TestContextCancellationAndTimeout(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	client.timeout = 20 * time.Millisecond
	if _, err := client.Series(context.Background(), "tt1234567"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected request context deadline, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Series(ctx, "tt1234567"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCacheExpirationAndFailedRequestRetry(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"Response":"False","Error":"temporary"}`)
			return
		}
		io.WriteString(w, seriesJSON)
	})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := client.Series(ctx, "tt1234567"); err == nil {
		t.Fatal("expected initial failure")
	}
	if _, err := client.Series(ctx, "tt1234567"); err != nil {
		t.Fatalf("failure was cached: %v", err)
	}
	if _, err := client.Series(ctx, "tt1234567"); err != nil || calls.Load() != 2 {
		t.Fatal("successful metadata was not cached")
	}
	now = now.Add(cacheTTL)
	if _, err := client.Series(ctx, "tt1234567"); err != nil || calls.Load() != 3 {
		t.Fatal("expired metadata did not refresh")
	}
}

func TestCacheCapacity(t *testing.T) {
	client := New("key", time.Second)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }
	for i := range cacheCapacity + 10 {
		client.store(fmt.Sprintf("series:%d", i), cacheEntry{series: Series{Title: "Example"}})
		now = now.Add(time.Second)
	}
	if len(client.cache) != cacheCapacity {
		t.Fatalf("cache size = %d, want %d", len(client.cache), cacheCapacity)
	}
	if _, ok := client.cached("series:0"); ok {
		t.Fatal("oldest metadata was not evicted")
	}
	now = now.Add(cacheTTL)
	client.store("new", cacheEntry{})
	if len(client.cache) != 1 {
		t.Fatal("expired entries were not pruned")
	}
}
