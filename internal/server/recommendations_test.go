package server

import (
	"reflect"
	"slices"
	"testing"

	"ts/internal/omdb"
)

func TestPreferredGroupCountsDistinctDownloadableEpisodes(t *testing.T) {
	group := seasonGroup{Expected: []omdb.Episode{{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}}}
	releases := map[string]*publicResult{}
	add := func(id, name string, episodes []int, seeds int, downloadable, pack bool) {
		group.Candidates = append(group.Candidates, seasonCandidate{ID: id, Parsed: &releaseParts{ReleaseGroup: name, Episodes: episodes, Pack: pack}})
		releases[id] = &publicResult{ID: id, Title: id, Seeders: seeds, HasMagnet: downloadable}
	}
	add("minority1", "MINORITY", []int{1}, 100, true, false)
	add("minority1mirror", "MINORITY", []int{1}, 200, true, false)
	add("minority1quality", "MINORITY", []int{1}, 300, true, false)
	add("minority2", "MINORITY", []int{2}, 1000, true, false)
	add("majority1", "MAJORITY", []int{1}, 5, true, false)
	add("majority2", "majority", []int{2}, 3, true, false)
	add("majority3", "MAJORITY", []int{3}, 2, true, false)
	add("gap4", "", []int{4}, 5, true, false)
	add("broken", "BROKEN", []int{1, 2, 3, 4}, 500, false, false)
	add("pack", "PACK", nil, 500, true, true)
	add("outside", "OUTSIDE", []int{5, 6, 7, 8, 9}, 500, true, false)
	recommendSeasonReleases(&group, releases)
	if group.PreferredReleaseGroup != "MAJORITY" {
		t.Fatalf("duplicates/unknown episodes skewed group selection: %q", group.PreferredReleaseGroup)
	}
	selected := slices.Clone(group.RecommendedIDs)
	slices.Sort(selected)
	if !reflect.DeepEqual(selected, []string{"gap4", "majority1", "majority2", "majority3"}) {
		t.Fatalf("wrong default releases: %v", selected)
	}
}

func TestPreferredGroupTieAndMultiEpisodeRecommendations(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		group := seasonGroup{Candidates: []seasonCandidate{
			{ID: "beta", Parsed: &releaseParts{ReleaseGroup: "BETA", Episodes: []int{1, 2, 3}}},
			{ID: "alpha12", Parsed: &releaseParts{ReleaseGroup: "ALPHA", Episodes: []int{1, 2}}},
			{ID: "alpha23", Parsed: &releaseParts{ReleaseGroup: "ALPHA", Episodes: []int{2, 3}}},
			{ID: "unknown4", Parsed: &releaseParts{Episodes: []int{4}}},
		}}
		if reverse {
			slices.Reverse(group.Candidates)
		}
		releases := map[string]*publicResult{
			"beta":     {Title: "beta", HasMagnet: true, Seeders: 100},
			"alpha12":  {Title: "alpha12", HasMagnet: true, Seeders: 5},
			"alpha23":  {Title: "alpha23", HasMagnet: true, Seeders: 10},
			"unknown4": {Title: "unknown4", HasTorrent: true},
		}
		recommendSeasonReleases(&group, releases)
		if group.PreferredReleaseGroup != "ALPHA" || !reflect.DeepEqual(group.RecommendedIDs, []string{"alpha23", "alpha12", "unknown4"}) {
			t.Fatalf("unstable preferred-group or range recommendations: %#v", group)
		}
	}
}

func TestUnverifiedPackCannotSupplyGroupEpisodeCoverage(t *testing.T) {
	group := seasonGroup{Candidates: []seasonCandidate{{ID: "pack", Parsed: &releaseParts{ReleaseGroup: "PACK", Pack: true}}}}
	recommendSeasonReleases(&group, map[string]*publicResult{"pack": {HasMagnet: true}})
	if group.PreferredReleaseGroup != "" || len(group.RecommendedIDs) != 0 {
		t.Fatalf("unverified pack incorrectly recommended as verified episode coverage: %#v", group)
	}
}
