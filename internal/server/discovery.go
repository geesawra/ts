package server

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"ts/internal/jackett"
	"ts/internal/omdb"
)

type seasonInventory struct {
	episodes []omdb.Episode
	err      error
}

func (request *seasonSearch) seasonRange() (int, int) {
	if request.season > 0 {
		return request.season, request.season
	}
	return 1, request.series.TotalSeasons
}

func (s *Server) loadSeasonInventory(ctx context.Context, request *seasonSearch) {
	if request.inventory != nil {
		return
	}
	first, last := request.seasonRange()
	request.inventory = make(map[int]seasonInventory, last-first+1)
	for season := first; season <= last; season++ {
		episodes, err := s.metadata.Season(ctx, request.series.ID, season)
		request.inventory[season] = seasonInventory{episodes: episodes, err: err}
		if ctx.Err() != nil {
			return
		}
	}
}

// Some trackers expose only the latest releases for a broad title query, even
// after its offset is exhausted. Search each season and inventoried episode as
// well; an episode can exist without appearing in either broader result set.
func (s *Server) discoverSeasons(ctx context.Context, indexers []string, params url.Values, request *seasonSearch) (jackett.SearchResponse, aggregationInfo, error) {
	response := jackett.SearchResponse{Results: []jackett.Result{}, Errors: []string{}}
	info := aggregationInfo{}
	if len(indexers) == 0 {
		configured, err := s.jackett.Indexers(ctx)
		if err != nil {
			return response, info, err
		}
		for _, indexer := range configured {
			indexers = append(indexers, indexer.ID)
		}
	}
	s.loadSeasonInventory(ctx, request)
	if err := ctx.Err(); err != nil {
		return response, info, err
	}
	newQuery := func(title string) url.Values {
		query := make(url.Values, len(params))
		for key, values := range params {
			query[key] = values
		}
		query.Del("season")
		query.Del("ep")
		query.Set("q", title)
		return query
	}
	queries := []url.Values{params}
	first, last := request.seasonRange()
	for season := first; season <= last; season++ {
		query := newQuery(fmt.Sprintf("%s S%02d", request.series.Title, season))
		queries = append(queries, query)
		inventory := request.inventory[season]
		if inventory.err != nil || len(inventory.episodes) == 0 {
			message := "OMDb returned no episode inventory"
			if inventory.err != nil {
				message = inventory.err.Error()
			}
			info.Partial = true
			response.Errors = append(response.Errors, fmt.Sprintf("Season %d: episode-specific searches unavailable: %s", season, message))
			continue
		}
		for _, episode := range inventory.episodes {
			query := newQuery(fmt.Sprintf("%s S%02dE%02d", request.series.Title, season, episode.Number))
			queries = append(queries, query)
		}
	}

	type discoveryQuery struct {
		indexer string
		params  url.Values
	}
	jobs := make(chan discoveryQuery)
	seen := make(map[string]bool)
	seenErrors := make(map[string]bool)
	var mu sync.Mutex
	var workers sync.WaitGroup
	var searchErr error
	for range 4 {
		workers.Go(func() {
			for job := range jobs {
				if ctx.Err() != nil {
					continue
				}
				result, pages, err := s.aggregateSeasons(ctx, []string{job.indexer}, job.params)
				mu.Lock()
				info.Pages += pages.Pages
				info.Partial = info.Partial || pages.Partial
				if err != nil && searchErr == nil {
					searchErr = err
				}
				for _, item := range result.Results {
					if !seen[item.ID] {
						seen[item.ID] = true
						response.Results = append(response.Results, item)
					}
				}
				for _, message := range result.Errors {
					if !seenErrors[message] {
						seenErrors[message] = true
						response.Errors = append(response.Errors, message)
					}
				}
				mu.Unlock()
			}
		})
	}
	visited := make(map[string]bool)
produce:
	for _, indexer := range indexers {
		if visited[indexer] {
			continue
		}
		visited[indexer] = true
		for _, query := range queries {
			select {
			case jobs <- discoveryQuery{indexer: indexer, params: query}:
			case <-ctx.Done():
				break produce
			}
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return response, info, ctx.Err()
	}
	return response, info, searchErr
}
