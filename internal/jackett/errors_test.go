package jackett

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadErrorBodies(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"empty", "text/plain", "", "Jackett returned HTTP 404"},
		{"whitespace", "text/plain", " \n\t", "Jackett returned HTTP 404"},
		{"plain", "text/plain", "Download blocked: VIP torrent and you are not VIP or higher.", "Jackett returned HTTP 404: Download blocked: VIP torrent and you are not VIP or higher."},
		{"html", "text/html", `<html><head><title>Error page</title><style>body { color: red; }</style></head><body><h1>Download blocked: VIP &amp; account required.</h1><script>alert("secret")</script></body></html>`, "Jackett returned HTTP 404: Download blocked: VIP & account required."},
		{"json", "application/json", `{"message":"VIP access required","stackTrace":"private diagnostics"}`, "Jackett returned HTTP 404: VIP access required"},
		{"problem detail", "application/problem+json", `{"title":"Forbidden","detail":"VIP access required"}`, "Jackett returned HTTP 404: VIP access required"},
		{"xml", "application/xml", `<?xml version="1.0"?><error code="100" description="Invalid API key: secret-key"/>`, "Jackett returned HTTP 404: Invalid API key: [redacted]"},
		{"html without content type", "", `<h1>VIP access required.</h1>`, "Jackett returned HTTP 404: VIP access required."},
		{"redacted", "text/plain", "Blocked secret-key; token=private-token; passkey='private-passkey'; https://tracker.test/private-path/announce?x=private-value", "Jackett returned HTTP 404: Blocked [redacted]; token=[redacted]; passkey=[redacted]; [URL redacted]"},
		{"cookie", "text/plain", "Blocked\nCookie: mam_id=private-cookie; other=private-value\nTry again", "Jackett returned HTTP 404: Blocked [credentials redacted] Try again"},
		{"authorization", "text/plain", "Blocked\nAuthorization: Bearer private-auth\nTry again", "Jackett returned HTTP 404: Blocked [credentials redacted] Try again"},
		{"echoed request secret", "text/plain", "Blocked private-query-token", "Jackett returned HTTP 404: Blocked [redacted]"},
		{"json escaped key", "application/json", `{"error":"Blocked secret\u002dkey"}`, "Jackett returned HTTP 404: Blocked [redacted]"},
		{"unicode", "text/plain", strings.Repeat("界", maxErrorRunes+1), "Jackett returned HTTP 404: " + strings.Repeat("界", maxErrorRunes) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			_, err := New(upstream.URL, "secret-key", time.Second).ResolveDownload(context.Background(), upstream.URL+"/dl/test?token=private-query-token")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSearchHTTPErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "Indexer temporarily unavailable: secret-key")
	}))
	defer upstream.Close()
	result, err := New(upstream.URL, "secret-key", time.Second).Search(context.Background(), []string{"example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 1 || result.Errors[0] != "example: Jackett returned HTTP 503: Indexer temporarily unavailable: [redacted]" {
		t.Fatalf("errors = %v", result.Errors)
	}
}

type errorBodyReader struct {
	remaining int
	read      int
	closed    bool
	fail      bool
}

func (r *errorBodyReader) Read(p []byte) (int, error) {
	if r.fail {
		return 0, errors.New("private read failure")
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

func (r *errorBodyReader) Close() error { r.closed = true; return nil }

func TestErrorBodyReadLimitAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		body := &errorBodyReader{remaining: maxErrorBodyBytes * 2, fail: fail}
		err := responseError(&http.Response{StatusCode: 502, Body: body}, "secret-key")
		if !body.closed || body.read > maxErrorBodyBytes {
			t.Fatalf("closed = %v, read = %d", body.closed, body.read)
		}
		if fail && err.Error() != "Jackett returned HTTP 502" {
			t.Fatalf("read failure = %v", err)
		}
		if !fail && err.Error() != "Jackett returned HTTP 502: "+strings.Repeat("x", maxErrorRunes)+"…" {
			t.Fatalf("bounded preview = %v", err)
		}
	}
}
