package search

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
)

var (
	seasonEpisodePattern = regexp.MustCompile(`(?i)\bS\d{1,2}(?:E\d{1,3})?\b`)
	episodePattern       = regexp.MustCompile(`第\s*\d+\s*集`)
	qualityPattern       = regexp.MustCompile(`(?i)(2160p|4k|1080p|720p|480p)`)
	codecPattern         = regexp.MustCompile(`(?i)\b(x265|h\.265|hevc|x264|h\.264|av1)\b`)
	parenPattern         = regexp.MustCompile(`[\[\](){}._-]+`)
)

func CleanQuery(query string) string {
	query = strings.TrimSpace(query)
	query = seasonEpisodePattern.ReplaceAllString(query, "")
	query = episodePattern.ReplaceAllString(query, "")
	return strings.Join(strings.Fields(query), " ")
}

func parseQuality(title string) string {
	match := qualityPattern.FindStringSubmatch(title)
	if len(match) == 0 {
		return ""
	}
	quality := strings.ToLower(match[1])
	if quality == "4k" {
		return "2160p"
	}
	return quality
}

func parseCodec(title string) string {
	match := codecPattern.FindStringSubmatch(title)
	if len(match) == 0 {
		return ""
	}
	codec := strings.ToLower(match[1])
	switch codec {
	case "h.265":
		return "hevc"
	case "h.264":
		return "x264"
	default:
		return codec
	}
}

func parseSubtitles(title string) []string {
	lower := strings.ToLower(title)
	result := make([]string, 0, 2)
	if strings.Contains(title, "简中") || strings.Contains(title, "简体") || strings.Contains(lower, "chs") || strings.Contains(lower, "sc") {
		result = append(result, "zh-CN")
	}
	if strings.Contains(title, "繁中") || strings.Contains(title, "繁体") || strings.Contains(lower, "cht") || strings.Contains(lower, "tc") {
		result = append(result, "zh-TW")
	}
	if strings.Contains(title, "中字") || strings.Contains(lower, "chinese") {
		if len(result) == 0 {
			result = append(result, "zh")
		}
	}
	return result
}

func classifyQuality(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "2160p", "4k":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	default:
		return 0
	}
}

func rankAndDeduplicate(items []domain.Candidate, request domain.SearchRequest, sessionID string, now time.Time) []domain.Candidate {
	seen := make(map[string]int, len(items))
	result := make([]domain.Candidate, 0, len(items))
	for _, item := range items {
		if item.Title == "" {
			continue
		}
		if item.Quality == "" {
			item.Quality = parseQuality(item.Title)
		}
		if item.Codec == "" {
			item.Codec = parseCodec(item.Title)
		}
		if len(item.Subtitles) == 0 {
			item.Subtitles = parseSubtitles(item.Title)
		}
		item.Score = score(item, request)
		key := candidateKey(item)
		if previous, ok := seen[key]; ok {
			if item.Score > result[previous].Score {
				item.ID = result[previous].ID
				item.SearchID = result[previous].SearchID
				item.CreatedAt = result[previous].CreatedAt
				result[previous] = item
			}
			continue
		}
		item.ID = "candidate_" + shortHash(sessionID+"|"+key)
		item.SearchID = sessionID
		item.CreatedAt = now
		seen[key] = len(result)
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].Title < result[j].Title
		}
		return result[i].Score > result[j].Score
	})
	return result
}

func candidateKey(item domain.Candidate) string {
	url := strings.TrimSpace(strings.ToLower(item.RawURL))
	if url != "" {
		return item.Kind + "|" + url
	}
	title := strings.ToLower(parenPattern.ReplaceAllString(item.Title, " "))
	return item.Provider + "|" + strings.Join(strings.Fields(title), " ") + "|" + strconv.FormatInt(item.SizeBytes, 10)
}

func score(item domain.Candidate, request domain.SearchRequest) float64 {
	value := 0.0
	if request.Quality != "" && classifyQuality(item.Quality) == classifyQuality(request.Quality) {
		value += 30
	} else if item.Quality != "" {
		value += float64(classifyQuality(item.Quality) * 4)
	}
	if len(request.Subtitles) > 0 {
		wanted := make(map[string]struct{}, len(request.Subtitles))
		for _, subtitle := range request.Subtitles {
			wanted[strings.ToLower(subtitle)] = struct{}{}
		}
		for _, subtitle := range item.Subtitles {
			if _, ok := wanted[strings.ToLower(subtitle)]; ok {
				value += 25
			}
		}
	}
	if item.Completeness == "complete" {
		value += 20
	}
	if item.Kind == "cloud" {
		value += 8
	}
	if item.Seeders > 0 {
		value += math.Min(12, math.Log1p(float64(item.Seeders))*3)
	}
	return value
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
