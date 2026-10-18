package transmission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHash   = "0123456789abcdef0123456789abcdef01234567"
	testMagnet = "magnet:?xt=urn:btih:" + testHash + "&dn=Example"
)

// A real daemon echoes the library's request tag in every RPC response.
type rpcRequest struct {
	Method    string          `json:"method"`
	Arguments map[string]any  `json:"arguments"`
	Tag       json.RawMessage `json:"tag"`
}

func readRPC(t *testing.T, r *http.Request) rpcRequest {
	t.Helper()
	var request rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
	}
	return request
}

func writeAdded(w http.ResponseWriter, request rpcRequest, duplicate bool) {
	key := "torrent-added"
	if duplicate {
		key = "torrent-duplicate"
	}
	json.NewEncoder(w).Encode(map[string]any{
		"result": "success", "tag": request.Tag,
		"arguments": map[string]any{key: map[string]string{"name": "Example", "hashString": testHash}},
	})
}

func writeRPCResult(w http.ResponseWriter, request rpcRequest, arguments any) {
	json.NewEncoder(w).Encode(map[string]any{
		"result": "success", "tag": request.Tag, "arguments": arguments,
	})
}

func writeTorrents(w http.ResponseWriter, request rpcRequest, torrents ...map[string]any) {
	if torrents == nil {
		torrents = []map[string]any{}
	}
	writeRPCResult(w, request, map[string]any{"torrents": torrents})
}

func requireHashIDs(t *testing.T, request rpcRequest, hash string) {
	t.Helper()
	if !reflect.DeepEqual(request.Arguments["ids"], []any{hash}) {
		t.Errorf("RPC must select exactly one hash, got %#v", request.Arguments["ids"])
	}
}

func TestMagnetAdapterSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := readRPC(t, r)
		username, password, ok := r.BasicAuth()
		if !ok || username != "rpc-user" || password != "rpc-password" {
			t.Error("missing RPC authentication")
		}
		if r.Method != http.MethodPost || request.Method != "torrent-add" || request.Arguments["filename"] != testMagnet || request.Arguments["paused"] != false || request.Arguments["download-dir"] != "/downloads" {
			t.Errorf("unexpected torrent-add request: %#v", request)
		}
		if !reflect.DeepEqual(request.Arguments["labels"], []any{Label}) {
			t.Errorf("missing ts label: %#v", request.Arguments)
		}
		if _, exists := request.Arguments["metainfo"]; exists {
			t.Error("magnet request must not include metainfo")
		}
		writeAdded(w, request, false)
	}))
	defer server.Close()
	result, err := New(server.URL, "rpc-user", "rpc-password", time.Second).Add(context.Background(), testMagnet, nil, "/downloads")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Example" || result.Hash != testHash || result.Duplicate {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestTorrentAdapterPreservesDuplicate(t *testing.T) {
	torrent := []byte("d4:infod4:name7:Exampleee")
	var mutex sync.Mutex
	var setAttempts int
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := readRPC(t, r)
		mutex.Lock()
		defer mutex.Unlock()
		calls = append(calls, request.Method)
		switch request.Method {
		case "torrent-add":
			if request.Arguments["metainfo"] != base64.StdEncoding.EncodeToString(torrent) || request.Arguments["paused"] != false || !reflect.DeepEqual(request.Arguments["labels"], []any{Label}) {
				t.Errorf("unexpected torrent payload: %#v", request.Arguments)
			}
			if _, exists := request.Arguments["filename"]; exists {
				t.Error("torrent request must not include filename")
			}
			if _, exists := request.Arguments["download-dir"]; exists {
				t.Error("empty directory must preserve daemon default")
			}
			writeAdded(w, request, true)
		case "torrent-get":
			requireHashIDs(t, request, testHash)
			writeTorrents(w, request, map[string]any{"hashString": testHash, "labels": []string{"keep-me", "another-label"}})
		case "torrent-set":
			requireHashIDs(t, request, testHash)
			if !reflect.DeepEqual(request.Arguments["labels"], []any{"keep-me", "another-label", Label}) {
				t.Errorf("existing labels were not preserved: %#v", request.Arguments["labels"])
			}
			setAttempts++
			if setAttempts == 1 {
				w.Header().Set("X-Transmission-Session-Id", "label-session")
				w.WriteHeader(http.StatusConflict)
				return
			}
			if r.Header.Get("X-Transmission-Session-Id") != "label-session" {
				t.Error("label update retry lost session negotiation")
			}
			writeRPCResult(w, request, map[string]any{})
		default:
			t.Errorf("unexpected RPC method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	result, err := New(server.URL, "", "", time.Second).Add(context.Background(), "", torrent, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Example" || result.Hash != testHash || !result.Duplicate {
		t.Fatalf("unexpected duplicate result: %#v", result)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if !reflect.DeepEqual(calls, []string{"torrent-add", "torrent-get", "torrent-set", "torrent-set"}) {
		t.Fatalf("unexpected duplicate labeling sequence: %v", calls)
	}
}

func TestConcurrentAddsKeepDuplicateMetadataSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := readRPC(t, r)
		switch request.Method {
		case "torrent-add":
			writeAdded(w, request, strings.Contains(request.Arguments["filename"].(string), "existing"))
		case "torrent-get":
			requireHashIDs(t, request, testHash)
			writeTorrents(w, request, map[string]any{"hashString": testHash, "labels": []string{Label, "keep-me"}})
		default:
			t.Errorf("unexpected RPC method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := New(server.URL, "", "", 5*time.Second)
	var group sync.WaitGroup
	for i := range 24 {
		group.Add(1)
		go func() {
			defer group.Done()
			duplicate := i%2 == 0
			magnet := testMagnet
			if duplicate {
				magnet += "-existing"
			}
			result, err := client.Add(context.Background(), magnet, nil, "")
			if err != nil {
				t.Error(err)
				return
			}
			if result.Duplicate != duplicate {
				t.Errorf("duplicate=%v, want %v", result.Duplicate, duplicate)
			}
		}()
	}
	group.Wait()
}

func TestAdapterSanitizesFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP error", 401, "private-server-secret"},
		{"RPC failure", 200, `{"result":"private-server-secret","arguments":{}}`},
		{"invalid JSON", 200, "private-server-secret"},
		{"missing torrent", 200, `{"result":"success","arguments":{}}`},
		{"missing hash", 200, `{"result":"success","arguments":{"torrent-added":{"name":"Example"}}}`},
		{"invalid hash", 200, `{"result":"success","arguments":{"torrent-added":{"hashString":"recently-active"}}}`},
		{"ambiguous torrent", 200, `{"result":"success","arguments":{"torrent-added":{"hashString":"a"},"torrent-duplicate":{"hashString":"a"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := readRPC(t, r)
				body := test.body
				var envelope map[string]any
				if json.Unmarshal([]byte(body), &envelope) == nil {
					envelope["tag"] = request.Tag
					encoded, _ := json.Marshal(envelope)
					body = string(encoded)
				}
				w.WriteHeader(test.status)
				io.WriteString(w, body)
			}))
			defer server.Close()
			_, err := New(server.URL, "private-user", "private-password", time.Second).Add(context.Background(), testMagnet, nil, "")
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("secret leaked in error: %v", err)
			}
		})
	}
}

func TestAdapterDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := New(server.URL, "user", "password", time.Second).Add(context.Background(), testMagnet, nil, "")
	if err == nil || reached.Load() {
		t.Fatalf("RPC redirect was not rejected: err=%v reached=%v", err, reached.Load())
	}
}

func TestAdapterTimeoutCoversSessionRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(server.URL, "", "", 50*time.Millisecond)
	if _, err := client.Add(context.Background(), testMagnet, nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected whole-add deadline, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Add(ctx, testMagnet, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestInvalidSourceAndEndpoint(t *testing.T) {
	client := New("http://example.test/rpc", "", "", time.Second)
	for _, test := range []struct {
		magnet  string
		torrent []byte
	}{
		{"", nil}, {testMagnet, []byte("torrent")}, {"http://example.com/private-secret", nil},
	} {
		if _, err := client.Add(context.Background(), test.magnet, test.torrent, ""); err == nil {
			t.Fatal("expected source validation error")
		}
	}
	for _, endpoint := range []string{"ftp://example.test", "http://private-user:private-password@example.test", "http://example.test/?secret=private-secret", ":invalid"} {
		client := New(endpoint, "", "", time.Second)
		_, err := client.Add(context.Background(), testMagnet, nil, "")
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("expected sanitized endpoint error, got %v", err)
		}
		if _, err := client.List(context.Background()); err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("expected sanitized list endpoint error, got %v", err)
		}
		if err := client.Action(context.Background(), testHash, "remove"); err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("expected sanitized action endpoint error, got %v", err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
	read   int
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestAdapterBoundsAndClosesBodies(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", maxResponseBody+100))}
	client := New("http://example.test/rpc", "", "", time.Second)
	client.http.Transport = boundedTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	})}
	if _, err := client.Add(context.Background(), testMagnet, nil, ""); err == nil {
		t.Fatal("expected oversized response error")
	}
	if !body.closed || body.read != maxResponseBody+1 {
		t.Fatalf("body closed=%v, bytes read=%d", body.closed, body.read)
	}
}

func TestAdapterSanitizesTransportErrors(t *testing.T) {
	client := New("http://example.test/rpc", "private-user", "private-password", time.Second)
	client.http.Transport = boundedTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private-transport-secret")
	})}
	_, err := client.Add(context.Background(), testMagnet, nil, "")
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("expected sanitized transport error, got %v", err)
	}
}
