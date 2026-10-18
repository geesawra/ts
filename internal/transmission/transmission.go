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
	"slices"
	"strings"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

const (
	Label           = "added-with-ts"
	maxResponseBody = 1 << 20
)

var (
	ErrNotManaged    = errors.New("torrent is not managed by ts")
	ErrInvalidAction = errors.New("invalid torrent action")
)

type Client struct {
	rpc            *transmissionrpc.Client
	http           *http.Client
	timeout        time.Duration
	seedRatioLimit *float64
}

type AddResult struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	Duplicate bool   `json:"duplicate"`
}

// Torrent is the label-scoped, credential-free projection exposed by ts.
type Torrent struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	Status       int     `json:"status"`
	Progress     float64 `json:"progress"`
	Size         int64   `json:"size"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	DownloadRate int64   `json:"download_rate"`
	UploadRate   int64   `json:"upload_rate"`
	ETA          int64   `json:"eta"`
	Ratio        float64 `json:"ratio"`
	Error        string  `json:"error"`
}

func New(endpoint, username, password string, timeout time.Duration) *Client {
	return newClient(endpoint, username, password, timeout, nil)
}

// NewWithSeedRatioLimit applies a per-torrent seed ratio to torrents added by
// this client. A zero ratio stops seeding as soon as a torrent completes.
func NewWithSeedRatioLimit(endpoint, username, password string, timeout time.Duration, seedRatioLimit float64) *Client {
	return newClient(endpoint, username, password, timeout, &seedRatioLimit)
}

func newClient(endpoint, username, password string, timeout time.Duration, seedRatioLimit *float64) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := &Client{
		timeout:        timeout,
		seedRatioLimit: seedRatioLimit,
		http: &http.Client{
			// Add, List, and Action each bound the whole operation via context.
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
	payload := transmissionrpc.TorrentAddPayload{Paused: &paused, Labels: []string{Label}}
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
		return AddResult{}, operationError(ctx, "torrent-add")
	}
	if result.HashString == nil || !validHash(*result.HashString) {
		return AddResult{}, errors.New("Transmission returned an invalid torrent-add result")
	}
	if state.duplicate {
		if err := c.labelDuplicate(ctx, *result.HashString); err != nil {
			return AddResult{}, err
		}
	}
	if c.seedRatioLimit != nil {
		mode := transmissionrpc.SeedRatioModeCustom
		limit := *c.seedRatioLimit
		ctx = context.WithValue(ctx, hashMutationKey{}, hashMutation{method: "torrent-set", hash: *result.HashString})
		if err := c.rpc.TorrentSet(ctx, transmissionrpc.TorrentSetPayload{
			IDs: []int64{0}, SeedRatioMode: &mode, SeedRatioLimit: &limit,
		}); err != nil {
			return AddResult{}, operationError(ctx, "torrent-set")
		}
	}
	added := AddResult{Hash: *result.HashString, Duplicate: state.duplicate}
	if result.Name != nil {
		added.Name = *result.Name
	}
	return added, nil
}

// List returns only torrents explicitly labeled as added through ts.
func (c *Client) List(ctx context.Context) ([]Torrent, error) {
	if c.rpc == nil {
		return nil, errors.New("invalid Transmission endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	torrents, err := c.rpc.TorrentGet(ctx, []string{
		"hashString", "name", "status", "percentDone", "totalSize",
		"downloadedEver", "uploadedEver", "rateDownload", "rateUpload",
		"eta", "uploadRatio", "error", "errorString", "labels",
	}, nil)
	if err != nil {
		return nil, operationError(ctx, "torrent-get")
	}
	managed := make([]Torrent, 0, len(torrents))
	for _, torrent := range torrents {
		if !slices.Contains(torrent.Labels, Label) {
			continue
		}
		if torrent.HashString == nil || !validHash(*torrent.HashString) {
			return nil, errors.New("Transmission returned an invalid torrent-get result")
		}
		item := Torrent{
			Hash:         *torrent.HashString,
			Name:         value(torrent.Name),
			Status:       int(value(torrent.Status)),
			Progress:     max(0, min(1, value(torrent.PercentDone))),
			Downloaded:   value(torrent.DownloadedEver),
			Uploaded:     value(torrent.UploadedEver),
			DownloadRate: value(torrent.RateDownload),
			UploadRate:   value(torrent.RateUpload),
			ETA:          -1,
			Ratio:        value(torrent.UploadRatio),
		}
		if torrent.TotalSize != nil {
			item.Size = int64(*torrent.TotalSize / 8)
		}
		if torrent.ETA != nil {
			item.ETA = *torrent.ETA
		}
		if value(torrent.Error) != 0 || value(torrent.ErrorString) != "" {
			// Daemon errors may contain private tracker URLs or credentials.
			item.Error = "Transmission reported a torrent error"
		}
		managed = append(managed, item)
	}
	return managed, nil
}

// Action rechecks ownership by hash before changing one torrent. Only the
// explicit remove-delete action deletes downloaded files.
func (c *Client) Action(ctx context.Context, hash, action string) error {
	switch action {
	case "start", "stop", "remove", "remove-delete":
	default:
		return ErrInvalidAction
	}
	if !validHash(hash) {
		return errors.New("invalid torrent hash")
	}
	if c.rpc == nil {
		return errors.New("invalid Transmission endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	torrent, err := c.torrentByHash(ctx, hash)
	if err != nil {
		return err
	}
	if !slices.Contains(torrent.Labels, Label) {
		return ErrNotManaged
	}
	switch action {
	case "start":
		err = c.rpc.TorrentStartHashes(ctx, []string{hash})
	case "stop":
		err = c.rpc.TorrentStopHashes(ctx, []string{hash})
	case "remove", "remove-delete":
		ctx = context.WithValue(ctx, hashMutationKey{}, hashMutation{method: "torrent-remove", hash: hash})
		err = c.rpc.TorrentRemove(ctx, transmissionrpc.TorrentRemovePayload{
			IDs: []int64{0}, DeleteLocalData: action == "remove-delete",
		})
	}
	if err != nil {
		return operationError(ctx, "torrent-"+action)
	}
	return nil
}

func (c *Client) labelDuplicate(ctx context.Context, hash string) error {
	torrent, err := c.torrentByHash(ctx, hash)
	if err != nil {
		return err
	}
	if slices.Contains(torrent.Labels, Label) {
		return nil
	}
	if torrent.Labels == nil {
		return errors.New("Transmission did not return torrent labels")
	}
	labels := append(torrent.Labels, Label)
	ctx = context.WithValue(ctx, hashMutationKey{}, hashMutation{method: "torrent-set", hash: hash})
	if err := c.rpc.TorrentSet(ctx, transmissionrpc.TorrentSetPayload{
		IDs: []int64{0}, Labels: labels,
	}); err != nil {
		return operationError(ctx, "torrent-set")
	}
	return nil
}

func (c *Client) torrentByHash(ctx context.Context, hash string) (transmissionrpc.Torrent, error) {
	torrents, err := c.rpc.TorrentGetHashes(ctx, []string{"hashString", "labels"}, []string{hash})
	if err != nil {
		return transmissionrpc.Torrent{}, operationError(ctx, "torrent-get")
	}
	if len(torrents) != 1 || torrents[0].HashString == nil || !strings.EqualFold(*torrents[0].HashString, hash) {
		return transmissionrpc.Torrent{}, ErrNotManaged
	}
	return torrents[0], nil
}

func validHash(hash string) bool {
	if len(hash) != 40 && len(hash) != 64 {
		return false
	}
	for _, digit := range hash {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') && (digit < 'A' || digit > 'F') {
			return false
		}
	}
	return true
}

func value[T any](pointer *T) T {
	if pointer != nil {
		return *pointer
	}
	var zero T
	return zero
}

func operationError(ctx context.Context, method string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Library errors include URLs, credentials, and arbitrary RPC error text.
	return errors.New("Transmission " + method + " failed")
}

type hashMutationKey struct{}
type hashMutation struct {
	method string
	hash   string
}

type addStateKey struct{}
type addState struct{ duplicate bool }

// The library owns RPC encoding, validation, and session negotiation. This small
// transport caps response memory and retains the duplicate bit that TorrentAdd
// otherwise discards. It also fills the library's missing hash support for
// torrent-set and torrent-remove, so numeric torrent IDs never leave ts.
// State belongs to a single operation context, never to the shared client.
type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if mutation, ok := request.Context().Value(hashMutationKey{}).(hashMutation); ok {
		// v3.0.0 accepts only []int64 for set/remove; replace the placeholder
		// before the underlying transport sees it, including session retries.
		var payload struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
			Tag       json.RawMessage            `json:"tag"`
		}
		err := json.NewDecoder(request.Body).Decode(&payload)
		request.Body.Close()
		if err != nil || payload.Method != mutation.method || (mutation.method != "torrent-set" && mutation.method != "torrent-remove") || payload.Arguments == nil || !validHash(mutation.hash) {
			return nil, errors.New("invalid Transmission hash mutation")
		}
		payload.Arguments["ids"], err = json.Marshal([]string{mutation.hash})
		if err != nil {
			return nil, errors.New("invalid Transmission hash mutation")
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("invalid Transmission hash mutation")
		}
		outgoing := *request
		request = &outgoing
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		request.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
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
		if state, ok := request.Context().Value(addStateKey{}).(*addState); ok && (envelope.Arguments.Added != nil || envelope.Arguments.Duplicate != nil) {
			state.duplicate = envelope.Arguments.Duplicate != nil
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
