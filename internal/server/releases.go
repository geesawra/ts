package server

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type releaseParts struct {
	Show         string `json:"show"`
	Season       int    `json:"season"`
	Episodes     []int  `json:"episodes"`
	Pack         bool   `json:"pack"`
	ReleaseGroup string `json:"releaseGroup"`
	Resolution   string `json:"resolution"`
	Source       string `json:"source"`
	Codec        string `json:"codec"`
}

var (
	seasonMarker     = regexp.MustCompile(`(?i)(?:^|[ ._-])s([0-9]{1,2})`)
	xMarker          = regexp.MustCompile(`(?i)(?:^|[ ._-])([0-9]{1,2})x([0-9]{1,3})`)
	episodeMarker    = regexp.MustCompile(`(?i)^[ ._]*e([0-9]{1,3})`)
	episodeRange     = regexp.MustCompile(`(?i)^-[ ._]*e?([0-9]{1,3})`)
	groupSuffix      = regexp.MustCompile(`-([A-Za-z][A-Za-z0-9]{0,30})(?:\[[^\]]*\])?$`)
	resolutionMarker = regexp.MustCompile(`(?i)\b(2160p|1440p|1080[pi]|720p|576p|480p|4k)\b`)
	sourceMarker     = regexp.MustCompile(`(?i)WEB[ ._-]*DL|WEB[ ._-]*Rip|Blu[ ._-]*Ray|BDRip|HDTV|DVDRip|Remux`)
	codecMarker      = regexp.MustCompile(`(?i)\b(x26[45]|H[ .]?26[45]|HEVC|AV1|AVC)\b`)
)

func normalizeShow(title string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ")
}

// Parse only explicit season/episode markers. Ambiguous multi-season or malformed
// episode markers stay ordinary releases; a season-only marker is an unverified pack.
func parseRelease(title string) *releaseParts {
	match := seasonMarker.FindStringSubmatchIndex(title)
	var season, start, end int
	episodes := []int{}
	if match != nil {
		season, _ = strconv.Atoi(title[match[2]:match[3]])
		start = match[2] - 1
		end = match[1]
		suffix := title[end:]
		for {
			ep := episodeMarker.FindStringSubmatchIndex(suffix)
			if ep == nil {
				break
			}
			n, _ := strconv.Atoi(suffix[ep[2]:ep[3]])
			episodes = append(episodes, n)
			end += ep[1]
			suffix = suffix[ep[1]:]
		}
	} else {
		match = xMarker.FindStringSubmatchIndex(title)
		if match == nil {
			return nil
		}
		season, _ = strconv.Atoi(title[match[2]:match[3]])
		start = match[2]
		end = match[1]
		ep, _ := strconv.Atoi(title[match[4]:match[5]])
		episodes = append(episodes, ep)
	}
	suffix := title[end:]
	if len(episodes) > 0 {
		if rng := episodeRange.FindStringSubmatchIndex(suffix); rng != nil {
			last, _ := strconv.Atoi(suffix[rng[2]:rng[3]])
			first := episodes[len(episodes)-1]
			if last <= first || last-first > 100 {
				return nil
			}
			for n := first + 1; n <= last; n++ {
				episodes = append(episodes, n)
			}
			end += rng[1]
			suffix = title[end:]
		}
	}
	// Boundary checks prevent S01E01foo, S01-S03 and partially parsed chains.
	if suffix != "" {
		first := suffix[0]
		if first != ' ' && first != '.' && first != '_' && first != '-' && first != '[' && first != '(' {
			return nil
		}
		rest := strings.TrimLeft(suffix, " ._-")
		if seasonMarker.MatchString(" "+rest) || episodeMarker.MatchString(rest) {
			return nil
		}
		if len(episodes) == 0 && len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			return nil
		}
	}
	show := strings.Trim(strings.NewReplacer(".", " ", "_", " ").Replace(title[:start]), " -_")
	show = strings.Join(strings.Fields(show), " ")
	if normalizeShow(show) == "" {
		return nil
	}
	parsed := &releaseParts{Show: show, Season: season, Episodes: episodes, Pack: len(episodes) == 0, Resolution: resolutionMarker.FindString(title), Source: sourceMarker.FindString(title), Codec: codecMarker.FindString(title)}
	if group := groupSuffix.FindStringSubmatch(title); group != nil {
		switch strings.ToLower(group[1]) {
		case "dl", "rip", "webrip", "hdrip", "bdrip", "brrip", "hdtv", "dvdrip", "x264", "x265", "h264", "h265", "hevc", "av1", "aac":
		default:
			parsed.ReleaseGroup = group[1]
		}
	}
	return parsed
}
