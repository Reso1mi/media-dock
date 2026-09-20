package search

import (
	"net/url"
	"path"
	"strings"

	"github.com/Reso1mi/media-dock/internal/domain"
)

var directFileExtensions = map[string]struct{}{
	".264":  {},
	".265":  {},
	".7z":   {},
	".ass":  {},
	".avi":  {},
	".flac": {},
	".m2ts": {},
	".m4a":  {},
	".m4v":  {},
	".mkv":  {},
	".mov":  {},
	".mp3":  {},
	".mp4":  {},
	".mpeg": {},
	".mpg":  {},
	".rar":  {},
	".srt":  {},
	".tar":  {},
	".ts":   {},
	".vtt":  {},
	".wav":  {},
	".webm": {},
	".zip":  {},
}

var cloudLinkTypes = map[string]struct{}{
	"115":    {},
	"123":    {},
	"aliyun": {},
	"baidu":  {},
	"cloud":  {},
	"quark":  {},
	"tianyi": {},
	"uc":     {},
}

// classifyResourceKind uses an explicit provider type when it is meaningful,
// then falls back to conservative URL inspection. In particular, a generic
// HTTP URL is unknown rather than an acquisition-ready torrent.
func classifyResourceKind(linkType, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(strings.ToLower(rawURL), "magnet:") {
		return domain.CandidateKindMagnet
	}

	typeName := strings.ToLower(strings.TrimSpace(linkType))
	switch typeName {
	case "magnet":
		return domain.CandidateKindMagnet
	case "torrent", "torrent_file":
		return domain.CandidateKindTorrent
	case "http_file", "direct", "file":
		if isHTTPURL(rawURL) {
			return domain.CandidateKindHTTPFile
		}
		return domain.CandidateKindUnknown
	}
	if _, ok := cloudLinkTypes[typeName]; ok {
		return domain.CandidateKindCloudShare
	}
	return kindFromURL(rawURL)
}

func kindFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !isHTTPURL(rawURL) {
		return domain.CandidateKindUnknown
	}
	if strings.EqualFold(path.Ext(parsed.Path), ".torrent") {
		return domain.CandidateKindTorrent
	}
	if _, ok := directFileExtensions[strings.ToLower(path.Ext(parsed.Path))]; ok {
		return domain.CandidateKindHTTPFile
	}
	return domain.CandidateKindUnknown
}

func isHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
