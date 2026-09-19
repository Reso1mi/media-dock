package domain

import "time"

type MediaType string

const (
	MediaTypeMovie MediaType = "movie"
	MediaTypeTV    MediaType = "tv"
	MediaTypeAnime MediaType = "anime"
)

type SearchRequest struct {
	Query     string    `json:"query"`
	MediaType MediaType `json:"media_type,omitempty"`
	Year      *int      `json:"year,omitempty"`
	Quality   string    `json:"quality,omitempty"`
	Subtitles []string  `json:"subtitles,omitempty"`
	Sources   []string  `json:"sources,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

type ProviderHealth struct {
	Provider  string `json:"provider"`
	Available bool   `json:"available"`
	Count     int    `json:"count"`
	Error     string `json:"error,omitempty"`
	Duration  int64  `json:"duration_ms"`
}

type Candidate struct {
	ID           string         `json:"id"`
	SearchID     string         `json:"search_id,omitempty"`
	Provider     string         `json:"provider"`
	Kind         string         `json:"kind"` // cloud, magnet, torrent, http
	Title        string         `json:"title"`
	SourceName   string         `json:"source_name,omitempty"`
	SizeBytes    int64          `json:"size_bytes,omitempty"`
	Quality      string         `json:"quality,omitempty"`
	Codec        string         `json:"codec,omitempty"`
	Audio        string         `json:"audio,omitempty"`
	Subtitles    []string       `json:"subtitles,omitempty"`
	Seeders      int            `json:"seeders,omitempty"`
	Leechers     int            `json:"leechers,omitempty"`
	Completeness string         `json:"completeness,omitempty"` // complete, partial, unknown
	PublishedAt  string         `json:"published_at,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	Score        float64        `json:"score"`
	Rank         int            `json:"rank"`
	RawURL       string         `json:"-"`
	Password     string         `json:"-"`
	RawPayload   map[string]any `json:"-"`
	CreatedAt    time.Time      `json:"created_at"`
}

type SearchSession struct {
	ID             string           `json:"id"`
	Request        SearchRequest    `json:"request"`
	Candidates     []Candidate      `json:"candidates"`
	ProviderHealth []ProviderHealth `json:"provider_health"`
	Warnings       []string         `json:"warnings,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
}

type JobStatus string

const (
	JobQueued      JobStatus = "queued"
	JobAcquiring   JobStatus = "acquiring"
	JobDownloading JobStatus = "downloading"
	JobDownloaded  JobStatus = "downloaded"
	JobFailed      JobStatus = "failed"
	JobCancelled   JobStatus = "cancelled"
	JobUnsupported JobStatus = "unsupported"
)

type AcquisitionJob struct {
	ID          string    `json:"id"`
	SearchID    string    `json:"search_id"`
	CandidateID string    `json:"candidate_id"`
	Provider    string    `json:"provider"`
	Downloader  string    `json:"downloader,omitempty"`
	Status      JobStatus `json:"status"`
	RemoteID    string    `json:"remote_id,omitempty"`
	TargetDir   string    `json:"target_dir"`
	Progress    float64   `json:"progress"`
	Message     string    `json:"message,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
