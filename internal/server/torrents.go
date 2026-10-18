package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ts/internal/transmission"
)

func (s *Server) torrents(w http.ResponseWriter, r *http.Request) {
	endpoints := r.URL.Query()["endpoint"]
	if len(endpoints) != 1 || endpoints[0] == "" {
		fail(w, 400, "Select a Transmission endpoint")
		return
	}
	client, ok := s.clients[endpoints[0]]
	if !ok {
		fail(w, 400, "Unknown Transmission endpoint")
		return
	}
	torrents, err := client.List(r.Context())
	if err != nil {
		fail(w, 502, "Cannot list Transmission torrents")
		return
	}
	jsonResponse(w, 200, map[string]any{"torrents": torrents})
}

func validTorrentHash(hash string) bool {
	if len(hash) != 40 && len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func (s *Server) torrentAction(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Endpoint string `json:"endpoint"`
		Hash     string `json:"hash"`
		Action   string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF {
		fail(w, 400, "Invalid torrent action request")
		return
	}
	client, ok := s.clients[request.Endpoint]
	if request.Endpoint == "" || !ok {
		fail(w, 400, "Unknown Transmission endpoint")
		return
	}
	if !validTorrentHash(request.Hash) {
		fail(w, 400, "Invalid torrent hash")
		return
	}
	if request.Action != "start" && request.Action != "stop" && request.Action != "remove" && request.Action != "remove-delete" {
		fail(w, 400, "Invalid torrent action")
		return
	}
	if err := client.Action(r.Context(), request.Hash, request.Action); err != nil {
		switch {
		case errors.Is(err, transmission.ErrNotManaged):
			fail(w, 404, "Torrent is not managed by ts")
		case errors.Is(err, transmission.ErrInvalidAction):
			fail(w, 400, "Invalid torrent action")
		default:
			fail(w, 502, "Cannot update Transmission torrent")
		}
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
