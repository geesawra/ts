package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ts/internal/transmission"
)

const managedTorrentHash = "0123456789abcdef0123456789abcdef01234567"
const otherTorrentHash = "abcdef0123456789abcdef0123456789abcdef01"

type torrentRPCRequest struct {
	Method    string          `json:"method"`
	Arguments map[string]any  `json:"arguments"`
	Tag       json.RawMessage `json:"tag"`
}

type torrentRPCFixture struct {
	mu            sync.Mutex
	requests      []torrentRPCRequest
	torrents      []map[string]any
	failure       string
	failureMethod string
}

func torrentFixtureRow(hash string, labels []string) map[string]any {
	return map[string]any{
		"id": 7, "hashString": hash, "name": "Linux ISO", "labels": labels,
		"status": 4, "percentDone": 0.5, "totalSize": 1000,
		"downloadedEver": 500, "uploadedEver": 250,
		"rateDownload": 100, "rateUpload": 50, "eta": 5,
		"uploadRatio": 0.5, "errorString": "",
	}
}

func newTorrentFixture(t *testing.T, fixture *torrentRPCFixture) http.Handler {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "torrent-token" {
			w.Header().Set("X-Transmission-Session-Id", "torrent-token")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var request torrentRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC: %v", err)
			w.WriteHeader(400)
			return
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.requests = append(fixture.requests, request)
		result := "success"
		arguments := map[string]any{}
		if fixture.failure != "" && (fixture.failureMethod == "" || fixture.failureMethod == request.Method) {
			result = fixture.failure
		} else {
			switch request.Method {
			case "torrent-get":
				rows := make([]map[string]any, 0)
				ids, scoped := request.Arguments["ids"].([]any)
				for _, row := range fixture.torrents {
					if !scoped {
						rows = append(rows, row)
						continue
					}
					for _, id := range ids {
						if hash, ok := id.(string); ok && strings.EqualFold(hash, row["hashString"].(string)) {
							rows = append(rows, row)
						}
					}
				}
				arguments["torrents"] = rows
			case "torrent-start", "torrent-stop", "torrent-remove":
			default:
				t.Errorf("unexpected RPC method %q", request.Method)
				result = "unsupported method"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": result, "tag": request.Tag, "arguments": arguments})
	}))
	t.Cleanup(upstream.Close)
	cfg := testConfig(upstream.URL)
	cfg.Transmission[0].Username = "rpc-user"
	cfg.Transmission[0].Password = "rpc-secret"
	return New(cfg)
}

func torrentAPIRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetBasicAuth("user", "secret")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestTorrentListScope(t *testing.T) {
	fixture := &torrentRPCFixture{torrents: []map[string]any{
		torrentFixtureRow(managedTorrentHash, []string{"personal", transmission.Label}),
		torrentFixtureRow(otherTorrentHash, []string{"personal"}),
	}}
	response := torrentAPIRequest(newTorrentFixture(t, fixture), "GET", "/api/torrents?endpoint=NAS", "")
	if response.Code != 200 {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Torrents []transmission.Torrent `json:"torrents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Torrents) != 1 || result.Torrents[0].Hash != managedTorrentHash {
		t.Fatalf("expected only tagged torrent: %s", response.Body.String())
	}
}

func TestTorrentListEmptyArray(t *testing.T) {
	fixture := &torrentRPCFixture{torrents: []map[string]any{torrentFixtureRow(otherTorrentHash, nil)}}
	response := torrentAPIRequest(newTorrentFixture(t, fixture), "GET", "/api/torrents?endpoint=NAS", "")
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"torrents":[]}` {
		t.Fatalf("empty list: %d %s", response.Code, response.Body.String())
	}
}

func TestTorrentRequestValidation(t *testing.T) {
	fixture := &torrentRPCFixture{}
	handler := newTorrentFixture(t, fixture)
	valid := `{"endpoint":"NAS","hash":"` + managedTorrentHash + `","action":"start"}`
	for _, tc := range []struct{ name, method, target, body string }{
		{"missing list endpoint", "GET", "/api/torrents", ""},
		{"empty list endpoint", "GET", "/api/torrents?endpoint=", ""},
		{"unknown list endpoint", "GET", "/api/torrents?endpoint=other", ""},
		{"ambiguous list endpoint", "GET", "/api/torrents?endpoint=NAS&endpoint=other", ""},
		{"malformed JSON", "POST", "/api/torrents/action", `{`},
		{"trailing JSON", "POST", "/api/torrents/action", valid + `{}`},
		{"trailing garbage", "POST", "/api/torrents/action", valid + `!`},
		{"oversized body", "POST", "/api/torrents/action", valid + strings.Repeat(" ", 4096)},
		{"unknown RPC field", "POST", "/api/torrents/action", strings.TrimSuffix(valid, "}") + `,"method":"session-close"}`},
		{"missing endpoint", "POST", "/api/torrents/action", `{"hash":"` + managedTorrentHash + `","action":"start"}`},
		{"unknown action endpoint", "POST", "/api/torrents/action", strings.Replace(valid, "NAS", "other", 1)},
		{"missing hash", "POST", "/api/torrents/action", `{"endpoint":"NAS","action":"start"}`},
		{"short hash", "POST", "/api/torrents/action", strings.Replace(valid, managedTorrentHash, "7", 1)},
		{"nonhex hash", "POST", "/api/torrents/action", strings.Replace(valid, managedTorrentHash, strings.Repeat("z", 40), 1)},
		{"missing action", "POST", "/api/torrents/action", `{"endpoint":"NAS","hash":"` + managedTorrentHash + `"}`},
		{"arbitrary action", "POST", "/api/torrents/action", strings.Replace(valid, "start", "session-close", 1)},
		{"wrong field type", "POST", "/api/torrents/action", `{"endpoint":"NAS","hash":7,"action":"start"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := torrentAPIRequest(handler, tc.method, tc.target, tc.body)
			if response.Code != 400 {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
		})
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 0 {
		t.Fatalf("invalid requests reached RPC: %#v", fixture.requests)
	}
}

func TestTorrentAuthenticationAndOriginBoundary(t *testing.T) {
	handler := newTorrentFixture(t, &torrentRPCFixture{})
	for _, tc := range []struct {
		name, method, target, origin, site, contentType string
		auth                                            bool
		status                                          int
	}{
		{"list auth", "GET", "/api/torrents?endpoint=NAS", "", "", "", false, 401},
		{"action auth", "POST", "/api/torrents/action", "", "", "application/json", false, 401},
		{"cross origin", "POST", "/api/torrents/action", "https://attacker.invalid", "", "application/json", true, 403},
		{"cross site", "POST", "/api/torrents/action", "", "cross-site", "application/json", true, 403},
		{"form action", "POST", "/api/torrents/action", "", "", "application/x-www-form-urlencoded", true, 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{}`))
			if tc.auth {
				request.SetBasicAuth("user", "secret")
			}
			request.Header.Set("Origin", tc.origin)
			request.Header.Set("Sec-Fetch-Site", tc.site)
			request.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}

func TestTorrentActionsUseScopedHashesAndRemovalFiles(t *testing.T) {
	for _, tc := range []struct{ action, method, hash string }{
		{"start", "torrent-start", managedTorrentHash},
		{"stop", "torrent-stop", strings.ToUpper(managedTorrentHash)},
		{"remove", "torrent-remove", strings.Repeat("a", 64)},
		{"remove-delete", "torrent-remove", managedTorrentHash},
	} {
		t.Run(tc.action, func(t *testing.T) {
			fixture := &torrentRPCFixture{torrents: []map[string]any{torrentFixtureRow(tc.hash, []string{transmission.Label})}}
			handler := newTorrentFixture(t, fixture)
			request := `{"endpoint":"NAS","hash":"` + tc.hash + `","action":"` + tc.action + `"}`
			response := torrentAPIRequest(handler, "POST", "/api/torrents/action", request)
			if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"ok":true}` {
				t.Fatalf("action: %d %s", response.Code, response.Body.String())
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.requests) != 2 || fixture.requests[0].Method != "torrent-get" || fixture.requests[1].Method != tc.method {
				t.Fatalf("expected scope check then action: %#v", fixture.requests)
			}
			for _, rpc := range fixture.requests {
				ids, ok := rpc.Arguments["ids"].([]any)
				if !ok || len(ids) != 1 {
					t.Fatalf("expected one hash ID: %#v", rpc)
				}
				hash, ok := ids[0].(string)
				if !ok || !strings.EqualFold(hash, tc.hash) {
					t.Fatalf("expected hash ID %s: %#v", tc.hash, rpc)
				}
			}
			if (tc.action == "remove" || tc.action == "remove-delete") && fixture.requests[1].Arguments["delete-local-data"] != (tc.action == "remove-delete") {
				t.Fatalf("incorrect file deletion choice for %s: %#v", tc.action, fixture.requests[1])
			}
		})
	}
}

func TestTorrentActionRejectsUnmanagedOrMissingHash(t *testing.T) {
	for _, hash := range []string{otherTorrentHash, strings.Repeat("f", 40)} {
		fixture := &torrentRPCFixture{torrents: []map[string]any{torrentFixtureRow(otherTorrentHash, []string{"personal"})}}
		handler := newTorrentFixture(t, fixture)
		response := torrentAPIRequest(handler, "POST", "/api/torrents/action", `{"endpoint":"NAS","hash":"`+hash+`","action":"remove"}`)
		if response.Code != 404 {
			t.Fatalf("unmanaged action: %d %s", response.Code, response.Body.String())
		}
		fixture.mu.Lock()
		if len(fixture.requests) != 1 || fixture.requests[0].Method != "torrent-get" {
			t.Errorf("unmanaged torrent mutated: %#v", fixture.requests)
		}
		fixture.mu.Unlock()
	}
}

func TestTorrentUpstreamFailureDoesNotLeakSecrets(t *testing.T) {
	for _, tc := range []struct{ method, failureMethod string }{
		{"GET", "torrent-get"},
		{"POST", "torrent-get"},
		{"POST", "torrent-start"},
	} {
		fixture := &torrentRPCFixture{
			torrents:      []map[string]any{torrentFixtureRow(managedTorrentHash, []string{transmission.Label})},
			failure:       "http://rpc-user:rpc-secret@private.invalid/transmission/rpc private-key",
			failureMethod: tc.failureMethod,
		}
		handler := newTorrentFixture(t, fixture)
		target, body := "/api/torrents?endpoint=NAS", ""
		if tc.method == "POST" {
			target = "/api/torrents/action"
			body = `{"endpoint":"NAS","hash":"` + managedTorrentHash + `","action":"start"}`
		}
		response := torrentAPIRequest(handler, tc.method, target, body)
		if response.Code != 502 {
			t.Fatalf("upstream failure: %d %s", response.Code, response.Body.String())
		}
		data, err := io.ReadAll(response.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"rpc-user", "rpc-secret", "private.invalid", "private-key", "transmission/rpc"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("leaked %q: %s", secret, data)
			}
		}
	}
}
