package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ts/internal/config"
	"ts/internal/jackett"
	"ts/internal/omdb"
	"ts/internal/transmission"
	"ts/web"
)

type cachedResult struct {
	result  jackett.Result
	expires time.Time
}
type Server struct {
	cfg      config.Config
	jackett  *jackett.Client
	clients  map[string]*transmission.Client
	mu       sync.Mutex
	results  map[string]cachedResult
	metadata seasonMetadata
}
type publicResult struct {
	ID                   string   `json:"id"`
	Title                string   `json:"title"`
	Indexer              string   `json:"indexer"`
	IndexerID            string   `json:"indexer_id"`
	Details              string   `json:"details"`
	Published            string   `json:"published"`
	Size                 int64    `json:"size"`
	Seeders              int      `json:"seeders"`
	Peers                int      `json:"peers"`
	Categories           []string `json:"categories"`
	DownloadVolumeFactor float64  `json:"download_volume_factor"`
	UploadVolumeFactor   float64  `json:"upload_volume_factor"`
	HasMagnet            bool     `json:"has_magnet"`
	HasTorrent           bool     `json:"has_torrent"`
}

func New(cfg config.Config) http.Handler {
	s := &Server{cfg: cfg, jackett: jackett.New(cfg.Jackett.URL, cfg.Jackett.APIKey, time.Duration(cfg.Jackett.TimeoutSeconds)*time.Second), clients: make(map[string]*transmission.Client), results: make(map[string]cachedResult)}
	if strings.TrimSpace(cfg.OMDb.APIKey) != "" {
		s.metadata = omdb.New(cfg.OMDb.APIKey, time.Duration(cfg.OMDb.TimeoutSeconds)*time.Second)
	}
	for _, ep := range cfg.Transmission {
		if ep.SeedRatioLimit != nil {
			s.clients[ep.Name] = transmission.NewWithSeedRatioLimit(ep.URL, ep.Username, ep.Password, time.Duration(ep.TimeoutSeconds)*time.Second, *ep.SeedRatioLimit)
		} else {
			s.clients[ep.Name] = transmission.New(ep.URL, ep.Username, ep.Password, time.Duration(ep.TimeoutSeconds)*time.Second)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/config", s.configuration)
	mux.HandleFunc("GET /api/indexers", s.indexers)
	mux.HandleFunc("GET /api/series", s.series)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("POST /api/download", s.download)
	mux.HandleFunc("GET /api/torrent", s.torrent)
	mux.HandleFunc("GET /api/magnet", s.magnet)
	mux.HandleFunc("GET /api/torrents", s.torrents)
	mux.HandleFunc("POST /api/torrents/action", s.torrentAction)
	assets, _ := fs.Sub(web.Assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		page := "index.html"
		switch r.URL.Path {
		case "/":
		case "/torrents":
			page = "torrents.html"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, page)
		if err != nil {
			fail(w, 500, "UI unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	return s.middleware(mux)
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.Server.Username != "" {
			user, pass, ok := r.BasicAuth()
			u := sha256.Sum256([]byte(user))
			p := sha256.Sum256([]byte(pass))
			eu := sha256.Sum256([]byte(s.cfg.Server.Username))
			ep := sha256.Sum256([]byte(s.cfg.Server.Password))
			if !ok || subtle.ConstantTimeCompare(u[:], eu[:])&subtle.ConstantTimeCompare(p[:], ep[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="ts", charset="UTF-8"`)
				fail(w, 401, "Authentication required")
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				fail(w, 403, "Cross-origin request denied")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					fail(w, 403, "Cross-origin request denied")
					return
				}
			}
			if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
				fail(w, 415, "Use application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) configuration(w http.ResponseWriter, r *http.Request) {
	endpoints := make([]map[string]string, 0, len(s.cfg.Transmission))
	for _, ep := range s.cfg.Transmission {
		endpoints = append(endpoints, map[string]string{"name": ep.Name})
	}
	jsonResponse(w, 200, map[string]any{"endpoints": endpoints, "authenticated": s.cfg.Server.Username != "", "omdb_enabled": s.metadata != nil})
}
func (s *Server) indexers(w http.ResponseWriter, r *http.Request) {
	v, err := s.jackett.Indexers(r.Context())
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{"indexers": v})
}

// Tracker extensions are forwarded as query values, never as paths or URLs.
// The client always supplies the configured API key itself.
func validSearchKey(key string) bool {
	if key == "apikey" || len(key) == 0 || len(key) > 64 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	params := url.Values{}
	for key, values := range r.URL.Query() {
		if key == "indexer" || key == "complete_seasons" || key == "series_id" {
			continue
		}
		if !validSearchKey(key) {
			fail(w, 400, "Unsupported search parameter")
			return
		}
		params[key] = values
	}
	if params.Get("t") == "" {
		params.Set("t", "search")
	}
	if cats := params["cat"]; len(cats) > 1 {
		params.Set("cat", strings.Join(cats, ","))
	}
	seasonRequest, ok := s.prepareSeasonSearch(w, r, params)
	if !ok {
		return
	}
	var result jackett.SearchResponse
	var aggregation aggregationInfo
	var err error
	if seasonRequest != nil {
		result, aggregation, err = s.discoverSeasons(r.Context(), r.URL.Query()["indexer"], params, seasonRequest)
	} else {
		result, err = s.jackett.Search(r.Context(), r.URL.Query()["indexer"], params)
	}
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	rows := make([]publicResult, 0, len(result.Results))
	now := time.Now()
	s.mu.Lock()
	for id, entry := range s.results {
		if now.After(entry.expires) {
			delete(s.results, id)
		}
	}
	if seasonRequest == nil && len(s.results)+len(result.Results) > 20000 {
		s.mu.Unlock()
		fail(w, 503, "Search result cache full; retry after older searches expire")
		return
	}
	for _, item := range result.Results {
		var token [24]byte
		if _, err := rand.Read(token[:]); err != nil {
			s.mu.Unlock()
			fail(w, 500, "Cannot create download token")
			return
		}
		id := hex.EncodeToString(token[:])
		s.results[id] = cachedResult{item, now.Add(30 * time.Minute)}
		details := item.Details
		if u, err := url.Parse(details); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.Contains(details, s.cfg.Jackett.APIKey) {
			details = ""
		}
		rows = append(rows, publicResult{
			ID: id, Title: item.Title, Indexer: item.Indexer, IndexerID: item.IndexerID, Details: details, Published: item.Published,
			Size: item.Size, Seeders: item.Seeders, Peers: item.Peers, Categories: item.Categories,
			DownloadVolumeFactor: item.DownloadVolumeFactor, UploadVolumeFactor: item.UploadVolumeFactor,
			HasMagnet: strings.HasPrefix(item.Magnet, "magnet:"), HasTorrent: item.Link != "" && !strings.HasPrefix(item.Link, "magnet:"),
		})
	}
	s.mu.Unlock()
	errors := result.Errors
	if errors == nil {
		errors = []string{}
	}
	if seasonRequest != nil {
		groups := s.buildSeasonGroups(r.Context(), seasonRequest, rows)
		jsonResponse(w, 200, map[string]any{"results": rows, "errors": errors, "seasons": groups, "aggregation": aggregation})
		return
	}
	jsonResponse(w, 200, map[string]any{"results": rows, "errors": errors})
}
func (s *Server) lookup(w http.ResponseWriter, r *http.Request, id string) (jackett.Result, bool) {
	s.mu.Lock()
	entry, ok := s.results[id]
	s.mu.Unlock()
	if !ok || time.Now().After(entry.expires) {
		fail(w, 410, "Search result expired; search again")
		return jackett.Result{}, false
	}
	return entry.result, true
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID       string `json:"id"`
		Endpoint string `json:"endpoint"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil {
		fail(w, 400, "Invalid download request")
		return
	}
	client, ok := s.clients[request.Endpoint]
	if !ok {
		fail(w, 400, "Unknown Transmission endpoint")
		return
	}
	item, ok := s.lookup(w, r, request.ID)
	if !ok {
		return
	}
	magnet := item.Magnet
	if magnet == "" && strings.HasPrefix(item.Link, "magnet:") {
		magnet = item.Link
	}
	var data []byte
	if magnet == "" {
		resolved, err := s.jackett.ResolveDownload(r.Context(), item.Link)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		magnet, data = resolved.Magnet, resolved.Torrent
	}
	dir := ""
	zeroRatio := false
	var err error
	for _, ep := range s.cfg.Transmission {
		if ep.Name == request.Endpoint {
			dir = ep.DownloadDir
			zeroRatio = ep.SeedRatioLimit != nil && *ep.SeedRatioLimit == 0
			break
		}
	}
	var result transmission.AddResult
	if zeroRatio {
		public, classifyErr := s.jackett.IsPublicIndexer(r.Context(), item.IndexerID)
		if classifyErr == nil && public {
			result, err = client.AddForPublicIndexer(r.Context(), magnet, data, dir)
		} else {
			result, err = client.Add(r.Context(), magnet, data, dir)
		}
	} else {
		result, err = client.Add(r.Context(), magnet, data, dir)
	}
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, result)
}
func (s *Server) torrent(w http.ResponseWriter, r *http.Request) {
	item, ok := s.lookup(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	resolved, err := s.jackett.ResolveDownload(r.Context(), item.Link)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if resolved.Magnet != "" {
		http.Redirect(w, r, resolved.Magnet, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", `attachment; filename="download.torrent"`)
	w.Write(resolved.Torrent)
}
func (s *Server) magnet(w http.ResponseWriter, r *http.Request) {
	item, ok := s.lookup(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	if !strings.HasPrefix(item.Magnet, "magnet:") {
		fail(w, 404, "No magnet available")
		return
	}
	jsonResponse(w, 200, map[string]string{"magnet": item.Magnet})
}

// Shutdown allows the entry point to drain active requests before exiting.
func Shutdown(s *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		return fmt.Errorf("HTTP shutdown: %w", err)
	}
	return nil
}
