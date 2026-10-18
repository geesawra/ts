package transmission

import (
	"context"
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

func TestDuplicateLabelingBoundaries(t *testing.T) {
	for _, test := range []struct {
		name       string
		labels     []string
		lookupFail bool
		setFail    bool
		missing    bool
		otherHash  bool
		wantSet    bool
		wantError  bool
	}{
		{name: "already managed", labels: []string{"keep-me", Label}},
		{name: "previously unlabeled", labels: []string{}, wantSet: true},
		{name: "missing label support", wantError: true},
		{name: "lookup failure", lookupFail: true, wantError: true},
		{name: "label update failure", labels: []string{"keep-me"}, setFail: true, wantSet: true, wantError: true},
		{name: "disappeared duplicate", missing: true, wantError: true},
		{name: "wrong torrent returned", labels: []string{}, otherHash: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := readRPC(t, r)
				switch request.Method {
				case "torrent-add":
					writeAdded(w, request, true)
				case "torrent-get":
					requireHashIDs(t, request, testHash)
					if test.lookupFail {
						json.NewEncoder(w).Encode(map[string]any{"result": "private-lookup-secret", "tag": request.Tag})
						return
					}
					if test.missing {
						writeTorrents(w, request)
						return
					}
					hash := testHash
					if test.otherHash {
						hash = strings.Repeat("f", 40)
					}
					writeTorrents(w, request, map[string]any{"hashString": hash, "labels": test.labels})
				case "torrent-set":
					sets.Add(1)
					requireHashIDs(t, request, testHash)
					want := make([]any, 0, len(test.labels)+1)
					for _, label := range test.labels {
						want = append(want, label)
					}
					want = append(want, Label)
					if !reflect.DeepEqual(request.Arguments["labels"], want) {
						t.Errorf("labels=%#v, want %#v", request.Arguments["labels"], want)
					}
					if test.setFail {
						json.NewEncoder(w).Encode(map[string]any{"result": "private-label-secret", "tag": request.Tag})
						return
					}
					writeRPCResult(w, request, map[string]any{})
				default:
					t.Errorf("unexpected RPC method %q", request.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			result, err := New(server.URL, "private-user", "private-password", time.Second).Add(context.Background(), testMagnet, nil, "")
			if (err != nil) != test.wantError {
				t.Fatalf("result=%#v, err=%v, wantError=%v", result, err, test.wantError)
			}
			if err != nil {
				if result != (AddResult{}) {
					t.Fatalf("failed labeling must not report success: %#v", result)
				}
				if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), server.URL) {
					t.Fatalf("labeling error leaked private data: %v", err)
				}
			} else if !result.Duplicate || result.Hash != testHash {
				t.Fatalf("lost duplicate result: %#v", result)
			}
			if (sets.Load() != 0) != test.wantSet || sets.Load() > 1 {
				t.Fatalf("label update calls=%d, wantSet=%v", sets.Load(), test.wantSet)
			}
		})
	}
}

func TestListFiltersAndProjectsManagedTorrents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := readRPC(t, r)
		if request.Method != "torrent-get" {
			t.Errorf("unexpected method %q", request.Method)
		}
		writeTorrents(w, request,
			map[string]any{
				"hashString": testHash, "name": "Example", "labels": []string{"keep-me", Label},
				"status": 4, "percentDone": 0.375, "totalSize": 8192,
				"downloadedEver": 3072, "uploadedEver": 6144, "rateDownload": 1024,
				"rateUpload": 512, "eta": 5, "uploadRatio": 2,
				"error": 2, "errorString": "private-tracker https://private-user:private-password@example.test/announce",
			},
			map[string]any{"hashString": strings.Repeat("a", 40), "labels": []string{}},
			map[string]any{"hashString": strings.Repeat("b", 40), "labels": []string{Label + "-other"}},
			map[string]any{"hashString": strings.Repeat("c", 40), "labels": []string{strings.ToUpper(Label)}},
			map[string]any{"hashString": strings.Repeat("d", 40)},
		)
	}))
	defer server.Close()
	torrents, err := New(server.URL, "", "", time.Second).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 || torrents[0].Hash != testHash {
		t.Fatalf("list must include only the exact labeled torrent: %#v", torrents)
	}
	// The RPC library stores totalSize as bits; consumers need bytes.
	if torrents[0].Size != 8192 {
		t.Fatalf("incorrect byte size: %d", torrents[0].Size)
	}
	encoded, err := json.Marshal(torrents[0])
	if err != nil {
		t.Fatal(err)
	}
	if torrents[0].Error == "" || strings.Contains(string(encoded), "private-") {
		t.Fatalf("torrent error missing or leaking daemon secrets: %s", encoded)
	}
}

func TestListEmptyAndOptionalFieldBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		torrents []map[string]any
		want     []Torrent
	}{
		{name: "no torrents", want: []Torrent{}},
		{name: "only unmanaged torrents", torrents: []map[string]any{{"hashString": testHash}}, want: []Torrent{}},
		{
			name: "sparse metadata and bounded progress",
			torrents: []map[string]any{
				{"hashString": testHash, "labels": []string{Label}},
				{"hashString": strings.Repeat("a", 64), "labels": []string{Label}, "percentDone": 1.5, "eta": -2},
				{"hashString": strings.Repeat("b", 40), "labels": []string{Label}, "percentDone": -0.5},
			},
			want: []Torrent{
				{Hash: testHash, ETA: -1},
				{Hash: strings.Repeat("a", 64), Progress: 1, ETA: -2},
				{Hash: strings.Repeat("b", 40), ETA: -1},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTorrents(w, readRPC(t, r), test.torrents...)
			}))
			defer server.Close()
			got, err := New(server.URL, "", "", time.Second).List(context.Background())
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("torrents=%#v, err=%v, want %#v", got, err, test.want)
			}
			if len(got) == 0 {
				encoded, err := json.Marshal(got)
				if err != nil || string(encoded) != "[]" {
					t.Fatalf("empty managed list must serialize as [], got %s, err=%v", encoded, err)
				}
			}
		})
	}
}

func TestActionsManageOnlyRequestedHashAndRemovalFiles(t *testing.T) {
	for _, action := range []string{"start", "stop", "remove", "remove-delete"} {
		method := "torrent-" + action
		if action == "remove-delete" {
			method = "torrent-remove"
		}
		for _, hash := range []string{testHash, strings.ToUpper(testHash), strings.Repeat("a", 64)} {
			t.Run(action+"/"+hash, func(t *testing.T) {
				var mutex sync.Mutex
				present, running, filesExist := true, action == "stop", true
				untouchedRunning, untouchedPresent, untouchedFiles := false, true, true
				var methods []string
				var attempts int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					request := readRPC(t, r)
					mutex.Lock()
					defer mutex.Unlock()
					methods = append(methods, request.Method)
					requireHashIDs(t, request, hash)
					if request.Method == "torrent-get" {
						writeTorrents(w, request, map[string]any{"hashString": strings.ToLower(hash), "labels": []string{Label}})
						return
					}
					if request.Method != method {
						t.Errorf("unexpected mutation method %q", request.Method)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					attempts++
					if attempts == 1 {
						w.Header().Set("X-Transmission-Session-Id", "renewed-session")
						w.WriteHeader(http.StatusConflict)
						return
					}
					if r.Header.Get("X-Transmission-Session-Id") != "renewed-session" {
						t.Error("mutation retry lost session negotiation")
					}
					for _, id := range request.Arguments["ids"].([]any) {
						if id != hash {
							untouchedRunning, untouchedPresent, untouchedFiles = true, false, false
							continue
						}
						switch action {
						case "start":
							running = true
						case "stop":
							running = false
						case "remove", "remove-delete":
							present = false
							deleteData, ok := request.Arguments["delete-local-data"].(bool)
							if !ok {
								t.Error("remove must explicitly choose whether to delete local files")
							}
							if deleteData {
								filesExist = false
							}
						}
					}
					writeRPCResult(w, request, map[string]any{})
				}))
				defer server.Close()
				if err := New(server.URL, "", "", time.Second).Action(context.Background(), hash, action); err != nil {
					t.Fatal(err)
				}
				mutex.Lock()
				defer mutex.Unlock()
				if present != (action != "remove" && action != "remove-delete") || running != (action == "start") || filesExist != (action != "remove-delete") {
					t.Fatalf("unexpected torrent state: present=%v running=%v files=%v", present, running, filesExist)
				}
				if untouchedRunning || !untouchedPresent || !untouchedFiles {
					t.Fatal("action affected another torrent")
				}
				if !reflect.DeepEqual(methods, []string{"torrent-get", method, method}) {
					t.Fatalf("action must recheck ownership before session-retried mutation: %v", methods)
				}
			})
		}
	}
}

func TestActionsRejectUnmanagedOrMismatchedTorrents(t *testing.T) {
	for _, action := range []string{"start", "stop", "remove", "remove-delete"} {
		for _, test := range []struct {
			name     string
			torrents []map[string]any
		}{
			{name: "missing"},
			{name: "unlabeled", torrents: []map[string]any{{"hashString": testHash, "labels": []string{}}}},
			{name: "unrelated label", torrents: []map[string]any{{"hashString": testHash, "labels": []string{Label + "-other"}}}},
			{name: "different hash", torrents: []map[string]any{{"hashString": strings.Repeat("f", 40), "labels": []string{Label}}}},
			{name: "missing hash", torrents: []map[string]any{{"labels": []string{Label}}}},
			{name: "ambiguous result", torrents: []map[string]any{{"hashString": testHash, "labels": []string{Label}}, {"hashString": strings.Repeat("f", 40), "labels": []string{Label}}}},
		} {
			t.Run(action+"/"+test.name, func(t *testing.T) {
				var mutations atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					request := readRPC(t, r)
					if request.Method != "torrent-get" {
						mutations.Add(1)
						writeRPCResult(w, request, map[string]any{})
						return
					}
					requireHashIDs(t, request, testHash)
					writeTorrents(w, request, test.torrents...)
				}))
				defer server.Close()
				err := New(server.URL, "", "", time.Second).Action(context.Background(), testHash, action)
				if !errors.Is(err, ErrNotManaged) || mutations.Load() != 0 {
					t.Fatalf("unmanaged action: err=%v, mutations=%d", err, mutations.Load())
				}
			})
		}
	}
}

func TestActionRejectsInvalidInputsWithoutRPC(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(server.URL, "", "", time.Second)
	for _, action := range []string{"", "delete", "remove-data", "START"} {
		if err := client.Action(context.Background(), testHash, action); !errors.Is(err, ErrInvalidAction) {
			t.Errorf("action %q: err=%v, want ErrInvalidAction", action, err)
		}
	}
	for _, hash := range []string{"", "1", "recently-active", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("g", 40), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		if err := client.Action(context.Background(), hash, "start"); err == nil {
			t.Errorf("accepted invalid hash %q", hash)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid input reached Transmission %d times", requests.Load())
	}
}

func TestManagementSanitizesFailures(t *testing.T) {
	for _, operation := range []string{"list", "scope", "start", "stop", "remove"} {
		for _, failure := range []string{"HTTP", "RPC", "JSON", "oversized"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					request := readRPC(t, r)
					if operation != "list" && operation != "scope" && request.Method == "torrent-get" {
						writeTorrents(w, request, map[string]any{"hashString": testHash, "labels": []string{Label}})
						return
					}
					switch failure {
					case "HTTP":
						w.WriteHeader(http.StatusUnauthorized)
						io.WriteString(w, "private-http-secret")
					case "RPC":
						json.NewEncoder(w).Encode(map[string]any{"tag": request.Tag, "result": "private-rpc-secret"})
					case "JSON":
						io.WriteString(w, "private-json-secret")
					case "oversized":
						io.WriteString(w, strings.Repeat("private-body-secret", maxResponseBody/10))
					}
				}))
				defer server.Close()
				client := New(server.URL, "private-user", "private-password", time.Second)
				var err error
				if operation == "list" {
					_, err = client.List(context.Background())
				} else {
					action := operation
					if operation == "scope" {
						action = "start"
					}
					err = client.Action(context.Background(), testHash, action)
				}
				if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), server.URL) {
					t.Fatalf("expected sanitized %s failure, got %v", operation, err)
				}
			})
		}
	}
}

func TestActionsRecheckLabelBeforeEveryMutation(t *testing.T) {
	var managed atomic.Bool
	managed.Store(true)
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := readRPC(t, r)
		requireHashIDs(t, request, testHash)
		if request.Method == "torrent-get" {
			labels := []string{}
			if managed.Load() {
				labels = []string{Label}
			}
			writeTorrents(w, request, map[string]any{"hashString": testHash, "labels": labels})
			return
		}
		mutations.Add(1)
		writeRPCResult(w, request, map[string]any{})
	}))
	defer server.Close()
	client := New(server.URL, "", "", time.Second)
	if err := client.Action(context.Background(), testHash, "start"); err != nil {
		t.Fatal(err)
	}
	managed.Store(false)
	if err := client.Action(context.Background(), testHash, "remove"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("removed ownership label was ignored: %v", err)
	}
	if mutations.Load() != 1 {
		t.Fatalf("unmanaged torrent was mutated after relabeling: %d mutations", mutations.Load())
	}
}

func TestListRejectsInvalidManagedHashes(t *testing.T) {
	for _, test := range []struct {
		name string
		hash any
	}{
		{name: "missing hash"},
		{name: "empty hash", hash: ""},
		{name: "broad identifier", hash: "recently-active"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTorrents(w, readRPC(t, r), map[string]any{"hashString": test.hash, "labels": []string{Label}})
			}))
			defer server.Close()
			torrents, err := New(server.URL, "", "", time.Second).List(context.Background())
			if err == nil || torrents != nil {
				t.Fatalf("accepted unusable managed hash: torrents=%#v, err=%v", torrents, err)
			}
		})
	}
}

func TestManagementSanitizesTransportErrors(t *testing.T) {
	for _, operation := range []string{"list", "scope", "remove"} {
		t.Run(operation, func(t *testing.T) {
			client := New("http://private-host.test/rpc", "private-user", "private-password", time.Second)
			client.http.Transport = boundedTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				request := readRPC(t, r)
				if operation == "remove" && request.Method == "torrent-get" {
					response := httptest.NewRecorder()
					writeTorrents(response, request, map[string]any{"hashString": testHash, "labels": []string{Label}})
					return response.Result(), nil
				}
				return nil, errors.New("private-transport-secret")
			})}
			var err error
			if operation == "list" {
				_, err = client.List(context.Background())
			} else {
				err = client.Action(context.Background(), testHash, "remove")
			}
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("expected sanitized transport error, got %v", err)
			}
		})
	}
}

func TestManagementTimeoutAndCancellation(t *testing.T) {
	for _, operation := range []string{"list", "action", "duplicate"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := readRPC(t, r)
				if operation == "duplicate" && request.Method == "torrent-add" {
					writeAdded(w, request, true)
					return
				}
				if operation != "list" && request.Method == "torrent-get" {
					labels := []string{Label}
					if operation == "duplicate" {
						labels = []string{}
					}
					writeTorrents(w, request, map[string]any{"hashString": testHash, "labels": labels})
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := New(server.URL, "", "", 50*time.Millisecond)
			run := func(ctx context.Context) error {
				switch operation {
				case "list":
					_, err := client.List(ctx)
					return err
				case "action":
					return client.Action(ctx, testHash, "start")
				default:
					_, err := client.Add(ctx, testMagnet, nil, "")
					return err
				}
			}
			if err := run(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected operation deadline, got %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
		})
	}
}
