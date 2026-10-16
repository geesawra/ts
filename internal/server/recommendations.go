package server

import (
	"sort"
	"strings"
)

// Recommend one consistent releasing group by distinct downloadable episode
// coverage, not torrent count. Unknown groups and unverified packs cannot win.
func recommendSeasonReleases(group *seasonGroup, releases map[string]*publicResult) {
	group.RecommendedIDs = []string{}
	expected := make(map[int]bool, len(group.Expected))
	for _, episode := range group.Expected {
		expected[episode.Number] = true
	}
	relevant := func(number int) bool { return len(expected) == 0 || expected[number] }
	coverage := make(map[string]map[int]bool)
	names := make(map[string]string)
	ranked := make([]int, 0, len(group.Candidates))
	for i, candidate := range group.Candidates {
		result := releases[candidate.ID]
		if result == nil || (!result.HasMagnet && !result.HasTorrent) || candidate.Parsed.Pack {
			continue
		}
		ranked = append(ranked, i)
		name := strings.TrimSpace(candidate.Parsed.ReleaseGroup)
		key := strings.ToLower(name)
		if key == "" {
			continue
		}
		if coverage[key] == nil {
			coverage[key] = make(map[int]bool)
			names[key] = name
		}
		if name < names[key] {
			names[key] = name
		}
		for _, episode := range candidate.Parsed.Episodes {
			if relevant(episode) {
				coverage[key][episode] = true
			}
		}
	}
	preferred := ""
	most := 0
	for key, episodes := range coverage {
		if len(episodes) > most || (len(episodes) == most && most > 0 && key < preferred) {
			preferred, most = key, len(episodes)
		}
	}
	group.PreferredReleaseGroup = names[preferred]
	sort.Slice(ranked, func(i, j int) bool {
		a, b := group.Candidates[ranked[i]], group.Candidates[ranked[j]]
		left, right := releases[a.ID], releases[b.ID]
		if left.Seeders != right.Seeders {
			return left.Seeders > right.Seeders
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		return a.ID < b.ID
	})
	covered := make(map[int]bool)
	selected := make(map[string]bool)
	// First select preferred-group releases without overlaps, then any remaining
	// preferred-group coverage. Other groups fill only the gaps, in the same order.
	for pass := range 4 {
		for _, index := range ranked {
			candidate := group.Candidates[index]
			if selected[candidate.ID] {
				continue
			}
			isPreferred := preferred != "" && strings.EqualFold(strings.TrimSpace(candidate.Parsed.ReleaseGroup), preferred)
			if isPreferred != (pass < 2) {
				continue
			}
			adds, overlaps := false, false
			for _, episode := range candidate.Parsed.Episodes {
				if covered[episode] {
					overlaps = true
				} else if relevant(episode) {
					adds = true
				}
			}
			if !adds || (pass%2 == 0 && overlaps) {
				continue
			}
			group.RecommendedIDs = append(group.RecommendedIDs, candidate.ID)
			selected[candidate.ID] = true
			for _, episode := range candidate.Parsed.Episodes {
				covered[episode] = true
			}
		}
	}
}
