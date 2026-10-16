package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"ts/internal/jackett"
)

func TestAggregationConsumesShortPages(t *testing.T) {
	for _, withTotal := range []bool{false, true} {
		t.Run(fmt.Sprintf("total=%t", withTotal), func(t *testing.T) {
			offsets := []string{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				offset := r.URL.Query().Get("offset")
				offsets = append(offsets, offset)
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
				if withTotal {
					fmt.Fprintf(w, `<torznab:response offset="%s" total="3"/>`, offset)
				}
				switch offset {
				case "0":
					io.WriteString(w, `<item><title>First</title><guid>first</guid></item><item><title>Second</title><guid>second</guid></item>`)
				case "2":
					io.WriteString(w, `<item><title>Last page match</title><guid>last</guid></item>`)
				case "3":
				default:
					t.Errorf("skipped actual page boundary: %q", offset)
				}
				io.WriteString(w, "</channel></rss>")
			}))
			defer upstream.Close()
			s := &Server{jackett: jackett.New(upstream.URL, "private-key", 0)}
			response, info, err := s.aggregateSeasons(context.Background(), []string{"test"}, url.Values{"t": {"search"}})
			if err != nil {
				t.Fatal(err)
			}
			if info.Partial || len(response.Results) != 3 || response.Results[2].Title != "Last page match" {
				t.Fatalf("short pages lost results: %#v %#v %v", info, response.Results, response.Errors)
			}
			wantOffsets := []string{"0", "2", "3"}
			if withTotal {
				wantOffsets = []string{"0", "2"}
			}
			if !reflect.DeepEqual(offsets, wantOffsets) {
				t.Fatalf("offsets %v, want %v", offsets, wantOffsets)
			}
		})
	}
}

func TestAggregationMarksUnreliablePaginationPartial(t *testing.T) {
	for _, kind := range []string{"repeat short page", "wrong offset", "early empty", "invalid total"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				offset := r.URL.Query().Get("offset")
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
				switch kind {
				case "wrong offset":
					io.WriteString(w, `<torznab:response offset="5" total="10"/>`)
				case "early empty":
					fmt.Fprintf(w, `<torznab:response offset="%s" total="10"/>`, offset)
				case "invalid total":
					io.WriteString(w, `<torznab:response offset="0" total="bad"/>`)
				}
				if kind != "early empty" {
					id := "repeated"
					fmt.Fprintf(w, "<item><title>Candidate</title><guid>%s</guid></item>", id)
				}
				io.WriteString(w, "</channel></rss>")
			}))
			defer upstream.Close()
			s := &Server{jackett: jackett.New(upstream.URL, "private-key", 0)}
			response, info, err := s.aggregateSeasons(context.Background(), []string{"test"}, url.Values{"t": {"search"}})
			if err != nil || !info.Partial || len(response.Errors) == 0 {
				t.Fatalf("unreliable pagination claimed exhaustion: %#v %v %v", info, response.Errors, err)
			}
			if kind == "repeat short page" && (requests != 2 || len(response.Results) != 1) {
				t.Fatalf("repeated short page was not stopped: %d %d", requests, len(response.Results))
			}
		})
	}
}

func TestAggregationUsesRawCountForOverlappingPages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss><channel>`)
		switch r.URL.Query().Get("offset") {
		case "0":
			io.WriteString(w, `<item><title>First</title><guid>first</guid></item><item><title>Second</title><guid>second</guid></item>`)
		case "2":
			io.WriteString(w, `<item><title>Second</title><guid>second</guid></item><item><title>Last match</title><guid>last</guid></item>`)
		case "4":
		default:
			t.Errorf("did not advance by raw page size: %s", r.URL.RawQuery)
		}
		io.WriteString(w, `</channel></rss>`)
	}))
	defer upstream.Close()
	s := &Server{jackett: jackett.New(upstream.URL, "private-key", 0)}
	response, info, err := s.aggregateSeasons(context.Background(), []string{"test"}, url.Values{"t": {"search"}})
	if err != nil || info.Partial || len(response.Results) != 3 || response.Results[2].Title != "Last match" {
		t.Fatalf("overlap skipped last match: %#v %#v %v", info, response.Results, err)
	}
}

func TestAggregationConsumesAllPagesBeyondFormerCaps(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss><channel>`)
		if offset < 55 {
			fmt.Fprintf(w, "<item><title>Episode %d</title><guid>episode%d</guid></item>", offset+1, offset+1)
		}
		io.WriteString(w, `</channel></rss>`)
	}))
	defer upstream.Close()
	s := &Server{jackett: jackett.New(upstream.URL, "private-key", 0)}
	response, info, err := s.aggregateSeasons(context.Background(), []string{"test"}, url.Values{"t": {"search"}})
	if err != nil || info.Partial || len(response.Results) != 55 || response.Results[54].Title != "Episode 55" {
		t.Fatalf("last page was not consumed: %#v results=%d errors=%v err=%v", info, len(response.Results), response.Errors, err)
	}
}
