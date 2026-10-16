package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

const maxResponseBody = 1 << 20

type Client struct {
	rpc     *transmissionrpc.Client
	http    *http.Client
	timeout time.Duration
}

type AddResult struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	Duplicate bool   `json:"duplicate"`
}

func New(endpoint, username, password string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := &Client{
		timeout: timeout,
		http: &http.Client{
			Timeout:   timeout,
			Transport: boundedTransport{base: http.DefaultTransport},
			// Never redirect RPC credentials or torrent contents elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(endpoint, "#") {
		return client
	}
	if username != "" || password != "" {
		parsed.User = url.UserPassword(username, password)
	}
	client.rpc, _ = transmissionrpc.New(parsed, &transmissionrpc.Config{CustomClient: client.http})
	return client
}

// Add starts exactly one magnet or torrent using the library's torrent-add RPC.
func (c *Client) Add(ctx context.Context, magnet string, torrent []byte, downloadDir string) (AddResult, error) {
	if (magnet == "") == (len(torrent) == 0) {
		return AddResult{}, errors.New("provide exactly one magnet or torrent")
	}
	if c.rpc == nil {
		return AddResult{}, errors.New("invalid Transmission endpoint")
	}
	paused := false
	payload := transmissionrpc.TorrentAddPayload{Paused: &paused}
	if downloadDir != "" {
		payload.DownloadDir = &downloadDir
	}
	if magnet != "" {
		parsed, err := url.Parse(magnet)
		if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") || parsed.Opaque != "" || parsed.Host != "" || parsed.Path != "" || parsed.RawQuery == "" {
			return AddResult{}, errors.New("invalid magnet URI")
		}
		payload.Filename = &magnet
	} else {
		metainfo := base64.StdEncoding.EncodeToString(torrent)
		payload.MetaInfo = &metainfo
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	state := &addState{}
	ctx = context.WithValue(ctx, addStateKey{}, state)
	result, err := c.rpc.TorrentAdd(ctx, payload)
	if err != nil {
		if ctx.Err() != nil {
			return AddResult{}, ctx.Err()
		}
		// Library errors include URLs, credentials, and arbitrary RPC error text.
		return AddResult{}, errors.New("Transmission torrent-add failed")
	}
	if result.HashString == nil || *result.HashString == "" {
		return AddResult{}, errors.New("Transmission returned an invalid torrent-add result")
	}
	added := AddResult{Hash: *result.HashString, Duplicate: state.duplicate}
	if result.Name != nil {
		added.Name = *result.Name
	}
	return added, nil
}

type addStateKey struct{}
type addState struct{ duplicate bool }

// The library owns RPC encoding, validation, and session negotiation. This small
// transport caps response memory and retains the duplicate bit that TorrentAdd
// otherwise discards when it merges torrent-added and torrent-duplicate.
// State belongs to a single Add context, never to the shared client.
type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	response.Body.Close()
	if err != nil {
		return nil, errors.New("cannot read Transmission response")
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("Transmission response is too large")
	}
	if response.StatusCode == http.StatusOK {
		var envelope struct {
			Arguments struct {
				Added     *struct{} `json:"torrent-added"`
				Duplicate *struct{} `json:"torrent-duplicate"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, errors.New("invalid Transmission response")
		}
		if envelope.Arguments.Added != nil && envelope.Arguments.Duplicate != nil {
			return nil, errors.New("ambiguous Transmission torrent-add result")
		}
		if state, ok := request.Context().Value(addStateKey{}).(*addState); ok {
			state.duplicate = envelope.Arguments.Duplicate != nil
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
