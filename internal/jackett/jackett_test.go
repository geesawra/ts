package jackett

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testCaps = `<caps><searching><search available="yes" supportedParams="q,cat"/><tv-search available="yes" supportedParams="q,season,ep,tvdbid"/><movie-search available="yes" supportedParams="q,imdbid"/><music-search available="no" supportedParams="q"/><book-search available="yes" supportedParams="q,author,title"/></searching><categories><category id="2000" name="Movies"><subcat id="2040" name="HD"/></category></categories></caps>`

func TestIndexersDiscoveryAndMissingEmbeddedCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "secret-key" {
			t.Error("missing API key")
		}
		switch r.URL.Path {
		case "/jackett/api/v2.0/indexers/all/results/torznab/api":
			if r.URL.Query().Get("configured") != "true" || r.URL.Query().Get("t") != "indexers" {
				t.Error("not restricted to configured indexers")
			}
			fmt.Fprint(w, `<indexers><indexer id="example" configured="true"><title>Example Tracker</title><description>Example</description><type>private</type><language>en-US</language><caps><categories><category id="2000" name="Movies"/></categories></caps></indexer><indexer id="disabled" configured="false"/></indexers>`)
		case "/jackett/api/v2.0/indexers/example/results/torznab/api":
			if r.URL.Query().Get("t") != "caps" {
				t.Error("missing caps request")
			}
			fmt.Fprint(w, testCaps)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	indexers, err := New(server.URL+"/jackett/", "secret-key", time.Second).Indexers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(indexers) != 1 {
		t.Fatalf("indexers = %#v", indexers)
	}
	indexer := indexers[0]
	if indexer.ID != "example" || indexer.Name != "Example Tracker" || indexer.Type != "private" || indexer.Language != "en-US" {
		t.Fatalf("metadata = %#v", indexer)
	}
	if !reflect.DeepEqual(indexer.Categories, []Category{{ID: "2000", Name: "Movies"}, {ID: "2040", Name: "HD"}}) {
		t.Fatalf("categories = %#v", indexer.Categories)
	}
	want := map[string][]string{"search": {"q", "cat"}, "tvsearch": {"q", "season", "ep", "tvdbid"}, "movie": {"q", "imdbid"}, "book": {"q", "author", "title"}}
	if !reflect.DeepEqual(indexer.Modes, want) {
		t.Fatalf("modes = %#v", indexer.Modes)
	}
}

func TestIndexersXMLInlineCapabilities(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `<indexers><indexer id="example" configured="true"><title>Example</title><description>Tracker</description><language>en</language><type>public</type>`+testCaps+`</indexer><indexer id="disabled" configured="false"><title>Disabled</title></indexer></indexers>`)
	}))
	defer server.Close()
	indexers, err := New(server.URL, "secret-key", time.Second).Indexers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(indexers) != 1 || indexers[0].Name != "Example" || len(indexers[0].Modes) != 4 {
		t.Fatalf("indexers = %#v", indexers)
	}
	if requests.Load() != 1 {
		t.Fatal("inline caps caused redundant requests")
	}
}

func TestSearchRichTorznabResults(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>Example</title><item><title>A release</title><guid isPermaLink="false">stable-guid</guid><link>https://jackett.test/dl/example?jackett_apikey=secret</link><comments>https://tracker.test/details/1</comments><pubDate>Fri, 16 Oct 2026 08:30:00 +0200</pubDate><size>123456</size><category>2000</category><torznab:attr name="category" value="2040"/><torznab:attr name="category" value="2000"/><torznab:attr name="seeders" value="27"/><torznab:attr name="peers" value="31"/><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:0123456789012345678901234567890123456789"/><torznab:attr name="downloadvolumefactor" value="0"/><torznab:attr name="uploadvolumefactor" value="2"/></item><item><title>Hash only</title><guid>second</guid><enclosure url="https://jackett.test/dl/second" length="98765" type="application/x-bittorrent"/><torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/><torznab:attr name="seeders" value="5"/><torznab:attr name="leechers" value="3"/></item></channel></rss>`)
	results, err := searchFixture(t, data, "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	first := results[0]
	if first.Title != "A release" || first.Indexer != "Example" || first.IndexerID != "example" || first.GUID != "stable-guid" || first.Size != 123456 || first.Seeders != 27 || first.Peers != 31 {
		t.Fatalf("first = %#v", first)
	}
	if first.Published != "2026-10-16T06:30:00Z" || first.Details != "https://tracker.test/details/1" || first.Magnet == "" || first.Link == "" {
		t.Fatalf("links/date = %#v", first)
	}
	if first.DownloadVolumeFactor != 0 || first.UploadVolumeFactor != 2 || !reflect.DeepEqual(first.Categories, []string{"2000", "2040"}) {
		t.Fatalf("attributes = %#v", first)
	}
	second := results[1]
	if second.Size != 98765 || second.Peers != 8 || second.DownloadVolumeFactor != 1 || second.UploadVolumeFactor != 1 || !strings.HasPrefix(second.Magnet, "magnet:?") {
		t.Fatalf("fallbacks = %#v", second)
	}
	if len(first.ID) != 64 || strings.Contains(first.ID, "secret") || first.ID == second.ID {
		t.Fatalf("IDs = %s, %s", first.ID, second.ID)
	}
	again, err := searchFixture(t, data, "example")
	if err != nil || again[0].ID != first.ID {
		t.Fatal("result IDs are not stable")
	}
	other, err := searchFixture(t, data, "different-indexer")
	if err != nil || other[0].ID == first.ID {
		t.Fatal("result IDs collide across indexers")
	}
}

func searchFixture(t *testing.T, body []byte, indexer string) ([]Result, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	response, err := New(server.URL, "secret-key", time.Second).Search(context.Background(), []string{indexer}, nil)
	if err != nil {
		return nil, err
	}
	if len(response.Errors) != 0 {
		return nil, fmt.Errorf("%s", strings.Join(response.Errors, "; "))
	}
	return response.Results, nil
}

func TestSearchBoundedConcurrencyAndPartialErrors(t *testing.T) {
	var active, maximum atomic.Int32
	var open sync.Once
	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		if current == maxConcurrency {
			open.Do(func() { close(gate) })
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		query := r.URL.Query()
		if query.Get("apikey") != "secret-key" || query.Get("cat") != "2000,2040" || query.Get("t") != "tvsearch" || query.Get("season") != "3" || query.Get("indexer") != "" {
			t.Errorf("query = %v", query)
		}
		if query.Get("custom_param") != "supported value" || query.Get("limit") != "25" || query.Get("offset") != "50" {
			t.Errorf("advanced query parameters lost: %v", query)
		}
		id := strings.Split(r.URL.Path, "/")[4]
		if id == "i2" {
			fmt.Fprint(w, `<error code="100" description="invalid key secret-key https://host/?apikey=secret-key"/>`)
			return
		}
		fmt.Fprintf(w, `<rss><channel><title>%s</title><item><title>%s release</title><guid>%s-guid</guid></item></channel></rss>`, id, id, id)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	params := url.Values{"t": {"tvsearch"}, "season": {"3"}, "cat": {"2000", "2040"}, "apikey": {"untrusted"}, "indexer": {"ignored"}, "custom_param": {"supported value"}, "limit": {"25"}, "offset": {"50"}}
	response, err := New(server.URL, "secret-key", time.Second).Search(ctx, []string{"i0", "i1", "i2", "i3", "i4", "i5", "i6", "i0"}, params)
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != maxConcurrency {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	if len(response.Results) != 6 || len(response.Errors) != 1 || response.Errors[0] != "i2: Torznab error 100" {
		t.Fatalf("response = %#v", response)
	}
	for i, id := range []string{"i0", "i1", "i3", "i4", "i5", "i6"} {
		if response.Results[i].IndexerID != id {
			t.Fatalf("result order = %#v", response.Results)
		}
	}
	if !reflect.DeepEqual(params["cat"], []string{"2000", "2040"}) || params.Get("apikey") != "untrusted" {
		t.Fatal("caller query mutated")
	}
}

func TestProtocolErrorsNeverExposeUpstreamSecrets(t *testing.T) {
	for _, body := range []string{`<error code="900" description="secret-key"/>`, `<error code="secret-key"/>`, `<rss><channel><item>secret-key`, `{"error":"secret-key"}`} {
		_, err := searchFixture(t, []byte(body), "example")
		if err == nil || strings.Contains(err.Error(), "secret-key") {
			t.Fatalf("body %q produced %v", body, err)
		}
	}
}

func TestIndexersRetainHealthyTrackersWhenOneCapabilitiesRequestFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "indexers":
			fmt.Fprint(w, `<indexers><indexer id="healthy" configured="true"><title>Healthy</title>`+testCaps+`</indexer><indexer id="failed" configured="true"><title>Unavailable</title><caps><categories><category id="2000" name="Movies"/></categories></caps></indexer></indexers>`)
		case "caps":
			fmt.Fprint(w, `<error code="900" description="secret-key"/>`)
		default:
			t.Errorf("unexpected query %v", r.URL.Query())
		}
	}))
	defer server.Close()
	indexers, err := New(server.URL, "secret-key", time.Second).Indexers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(indexers) != 2 {
		t.Fatalf("indexers = %#v", indexers)
	}
	for _, indexer := range indexers {
		switch indexer.ID {
		case "healthy":
			if len(indexer.Modes) == 0 || indexer.CapabilityError != "" {
				t.Fatalf("healthy = %#v", indexer)
			}
		case "failed":
			if len(indexer.Modes) != 0 || indexer.CapabilityError != "Torznab error 900" || !reflect.DeepEqual(indexer.Categories, []Category{{ID: "2000", Name: "Movies"}}) {
				t.Fatalf("failed = %#v", indexer)
			}
		}
	}
}

func TestResolveDownloadTrustBoundary(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		fmt.Fprint(w, "must not reach upstream")
	}))
	defer upstream.Close()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/jackett/dl/example":
			if r.URL.Query().Get("jackett_apikey") != "secret-key" {
				t.Error("download link query was lost")
			}
			fmt.Fprint(w, "d4:infodee")
		case "/jackett/dl/redirect":
			http.Redirect(w, r, upstream.URL+"/torrent", http.StatusFound)
		case "/jackett/dl/oversized":
			w.Header().Set("Content-Length", "16777217")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/jackett", "secret-key", time.Second)
	data, err := client.ResolveDownload(context.Background(), server.URL+"/jackett/dl/example?jackett_apikey=secret-key")
	if err != nil || string(data.Torrent) != "d4:infodee" {
		t.Fatalf("download = %#v, %v", data, err)
	}
	before := requests.Load()
	for _, link := range []string{upstream.URL + "/jackett/dl/example", server.URL + "/jackett-other/dl/example", server.URL + "/jackett/%2e%2e/admin", server.URL + "/jackett/dl%5cexample", "file:///jackett/dl/example"} {
		if _, err := client.ResolveDownload(context.Background(), link); err == nil {
			t.Errorf("accepted untrusted link %s", link)
		}
	}
	if requests.Load() != before {
		t.Fatal("untrusted links reached Jackett")
	}
	for _, path := range []string{"redirect", "oversized"} {
		_, err := client.ResolveDownload(context.Background(), server.URL+"/jackett/dl/"+path)
		if err == nil || strings.Contains(err.Error(), "secret-key") {
			t.Errorf("%s: %v", path, err)
		}
	}
	if upstreamRequests.Load() != 0 {
		t.Fatal("followed an upstream redirect")
	}
}

func TestResolveDownloadStreamingLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		chunk := strings.Repeat("x", 64<<10)
		for i := 0; i <= maxResponseBytes/len(chunk); i++ {
			if _, err := fmt.Fprint(w, chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	_, err := New(server.URL, "secret", 3*time.Second).ResolveDownload(context.Background(), server.URL+"/dl/example")
	if err == nil || err.Error() != "Jackett response exceeds 16 MiB" {
		t.Fatalf("stream limit error = %v", err)
	}
}

func TestResolveDownloadRedirects(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:0123456789012345678901234567890123456789&dn=Example"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jackett/dl/magnet":
			w.Header().Set("Location", magnet)
			w.WriteHeader(http.StatusFound)
		case "/jackett/dl/start":
			http.Redirect(w, r, "torrent?token=private-token", http.StatusTemporaryRedirect)
		case "/jackett/dl/torrent":
			if r.URL.Query().Get("token") != "private-token" {
				t.Error("redirect query was lost")
			}
			fmt.Fprint(w, "d4:infodee")
		case "/jackett/dl/loop":
			http.Redirect(w, r, "loop?token=private-token", http.StatusFound)
		case "/jackett/dl/missing":
			w.WriteHeader(http.StatusFound)
		case "/jackett/dl/invalid":
			w.Header().Set("Location", "magnet:?dn=missing-xt")
			w.WriteHeader(http.StatusFound)
		case "/jackett/dl/escape":
			http.Redirect(w, r, "/outside?token=private-token", http.StatusFound)
		default:
			t.Errorf("unexpected redirect target %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/jackett", "key", time.Second)
	resolved, err := client.ResolveDownload(context.Background(), server.URL+"/jackett/dl/magnet")
	if err != nil || resolved.Magnet != magnet || resolved.Torrent != nil {
		t.Fatalf("magnet resolution = %#v, %v", resolved, err)
	}
	resolved, err = client.ResolveDownload(context.Background(), server.URL+"/jackett/dl/start")
	if err != nil || string(resolved.Torrent) != "d4:infodee" || resolved.Magnet != "" {
		t.Fatalf("torrent resolution = %#v, %v", resolved, err)
	}
	for _, path := range []string{"loop", "missing", "invalid", "escape"} {
		t.Run(path, func(t *testing.T) {
			_, err := client.ResolveDownload(context.Background(), server.URL+"/jackett/dl/"+path)
			if err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("redirect failure = %v", err)
			}
		})
	}
}
