package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ts/internal/config"
)

func testConfig(upstream string) config.Config {
	return config.Config{Server: config.Server{Username: "user", Password: "secret"}, Jackett: config.Jackett{URL: upstream, APIKey: "private-key", TimeoutSeconds: 5}, Transmission: []config.Transmission{{Name: "NAS", URL: upstream + "/transmission/rpc", TimeoutSeconds: 5}}}
}
func TestDownloadBoundary(t *testing.T) {
	app := httptest.NewServer(New(testConfig("http://127.0.0.1:9")))
	defer app.Close()
	cases := []struct {
		name, body, origin, contentType string
		auth                            bool
		status                          int
	}{
		{"unauthenticated", `{"id":"fake","endpoint":"NAS"}`, "", "application/json", false, 401},
		{"cross origin", `{"id":"fake","endpoint":"NAS"}`, "https://attacker.invalid", "application/json", true, 403},
		{"form submission", `id=fake&endpoint=NAS`, "", "application/x-www-form-urlencoded", true, 415},
		{"unknown endpoint", `{"id":"fake","endpoint":"other"}`, "", "application/json", true, 400},
		{"uncached URL", `{"id":"http://127.0.0.1/secrets","endpoint":"NAS"}`, "", "application/json", true, 410},
		{"arbitrary URL field", `{"id":"fake","endpoint":"NAS","url":"http://127.0.0.1/secrets"}`, "", "application/json", true, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", app.URL+"/api/download", bytes.NewBufferString(tc.body))
			if tc.auth {
				r.SetBasicAuth("user", "secret")
			}
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.contentType)
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestOriginBoundary(t *testing.T) {
	handler := (&Server{}).middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, origin, site string
		status             int
	}{
		{"LAN origin without fetch metadata", "http://192.168.1.10:8080", "", 204},
		{"same origin with fetch metadata", "http://192.168.1.10:8080", "same-origin", 204},
		{"opaque origin", "null", "", 403},
		{"different port", "http://192.168.1.10:8081", "", 403},
		{"different host", "http://192.168.1.11:8080", "", 403},
		{"cross-site fetch metadata", "http://192.168.1.10:8080", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://192.168.1.10:8080/api/download", strings.NewReader(`{}`))
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("Sec-Fetch-Site", tc.site)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}
func TestSearchHidesCredentialsAndDownloadsCachedTorrent(t *testing.T) {
	var base string
	adds := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2.0/indexers":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"id":"test","name":"Test"}]`)
		case "/api/v2.0/indexers/test/results/torznab/api":
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("t") == "caps" {
				io.WriteString(w, `<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`)
				return
			}
			io.WriteString(w, `<rss><channel><item><title>ISO</title><guid>private-key</guid><link>`+base+`/dl/test?apikey=private-key</link><size>12</size></item></channel></rss>`)
		case "/dl/test":
			io.WriteString(w, "d4:infod4:name3:ISOee")
		case "/transmission/rpc":
			if r.Header.Get("X-Transmission-Session-Id") != "token" {
				w.Header().Set("X-Transmission-Session-Id", "token")
				w.WriteHeader(409)
				return
			}
			var added map[string]any
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			decoder.Decode(&added)
			adds <- added
			json.NewEncoder(w).Encode(map[string]any{"result": "success", "tag": added["tag"], "arguments": map[string]any{"torrent-added": map[string]string{"name": "ISO", "hashString": managedTorrentHash}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base = upstream.URL
	cfg := testConfig(base)
	cfg.Server.Username = ""
	cfg.Server.Password = ""
	app := httptest.NewServer(New(cfg))
	defer app.Close()
	resp, err := http.Get(app.URL + "/api/search?indexer=test&t=search&q=ISO")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("search: %d %s", resp.StatusCode, data)
	}
	if bytes.Contains(data, []byte("private-key")) || bytes.Contains(data, []byte(base)) {
		t.Fatalf("upstream secret leaked: %s", data)
	}
	var search struct {
		Results []struct {
			ID string `json:"id"`
		}
	}
	if err := json.Unmarshal(data, &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search results: %s", data)
	}
	payload, _ := json.Marshal(map[string]string{"id": search.Results[0].ID, "endpoint": "NAS"})
	resp, err = http.Post(app.URL+"/api/download", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("download: %d %s", resp.StatusCode, data)
	}
	added := <-adds
	args, ok := added["arguments"].(map[string]any)
	if !ok || args["metainfo"] != "ZDQ6aW5mb2Q0Om5hbWUzOklTT2Vl" || args["paused"] != false {
		t.Fatalf("wrong torrent add: %#v", added)
	}
}

type blockedWriter struct {
	header           http.Header
	entered, release chan struct{}
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(data), nil
}

func TestSlowSearchReaderDoesNotBlockDownloadLookup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss><channel><item><title>ISO</title><guid>iso</guid></item></channel></rss>`)
	}))
	defer upstream.Close()
	cfg := testConfig(upstream.URL)
	cfg.Server.Username = ""
	cfg.Server.Password = ""
	app := New(cfg)
	writer := &blockedWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		app.ServeHTTP(writer, httptest.NewRequest("GET", "/api/search?indexer=test&t=search", nil))
	}()
	defer func() { close(writer.release); <-finished }()
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not reach response writer")
	}
	lookedUp := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest("GET", "/api/magnet?id=expired", nil))
		lookedUp <- response.Code
	}()
	select {
	case status := <-lookedUp:
		if status != 410 {
			t.Fatalf("lookup status %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("slow search reader blocked download lookup")
	}
}

func TestAddResolvesJackettMagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789012345678901234567890123456789&dn=Example"
	var base string
	adds := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2.0/indexers/test/results/torznab/api":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<rss><channel><item><title>Redirected release</title><guid>redirect</guid><link>`+base+`/dl/start</link></item></channel></rss>`)
		case "/dl/start":
			http.Redirect(w, r, "magnet", http.StatusFound)
		case "/dl/magnet":
			w.Header().Set("Location", magnet)
			w.WriteHeader(http.StatusFound)
		case "/transmission/rpc":
			if r.Header.Get("X-Transmission-Session-Id") != "session" {
				w.Header().Set("X-Transmission-Session-Id", "session")
				w.WriteHeader(http.StatusConflict)
				return
			}
			var request struct {
				Tag       json.RawMessage `json:"tag"`
				Arguments map[string]any  `json:"arguments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			adds <- request.Arguments
			json.NewEncoder(w).Encode(map[string]any{"result": "success", "tag": request.Tag, "arguments": map[string]any{"torrent-added": map[string]string{"name": "Redirected release", "hashString": managedTorrentHash}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base = upstream.URL
	cfg := testConfig(base)
	cfg.Server.Username, cfg.Server.Password = "", ""
	cfg.Transmission[0].DownloadDir = "/downloads/selected"
	app := httptest.NewServer(New(cfg))
	defer app.Close()
	response, err := http.Get(app.URL + "/api/search?indexer=test&t=search")
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	err = json.NewDecoder(response.Body).Decode(&search)
	response.Body.Close()
	if err != nil || len(search.Results) != 1 {
		t.Fatalf("search = %#v, %v", search, err)
	}
	payload, _ := json.Marshal(map[string]string{"id": search.Results[0].ID, "endpoint": "NAS"})
	response, err = http.Post(app.URL+"/api/download", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Add = %d %s", response.StatusCode, body)
	}
	arguments := <-adds
	if arguments["filename"] != magnet || arguments["paused"] != false || arguments["download-dir"] != "/downloads/selected" {
		t.Fatalf("redirected Add arguments = %#v", arguments)
	}
	if _, ok := arguments["metainfo"]; ok {
		t.Fatal("magnet redirect was submitted as torrent bytes")
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = noRedirect.Get(app.URL + "/api/torrent?id=" + search.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != magnet {
		t.Fatalf("torrent link = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
}
