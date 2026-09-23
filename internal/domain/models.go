package domain

import "time"

type MediaType string

const (
	MediaTypeMovie MediaType = "movie"
	MediaTypeTV    MediaType = "tv"
	MediaTypeAnime MediaType = "anime"
)

// Candidate kinds describe the material required by an acquisition adapter.
// Keep these values narrow: an HTTP URL alone does not tell us whether it is a
// torrent file, a direct media file, or a page that still needs resolving.
const (
	CandidateKindMagnet     = "magnet"
	CandidateKindTorrent    = "torrent"
	CandidateKindHTTPFile   = "http_file"
	CandidateKindCloudShare = "cloud_share"
	CandidateKindUnknown    = "unknown"
)

type ComponentCapability struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type UnavailableKind struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type TargetProfileSummary struct {
	ID                  string   `json:"id"`
	Type                string   `json:"type"`
	DisplayName         string   `json:"display_name"`
	SupportedKinds      []string `json:"supported_kinds"`
	SupportedGoals      []string `json:"supported_goals"`
	SupportedOperations []string `json:"supported_operations,omitempty"`
}

type AcquisitionCapabilities struct {
	SupportedKinds      []string               `json:"supported_kinds"`
	DefaultDownloaderID string                 `json:"default_downloader_id,omitempty"`
	UnavailableKinds    []UnavailableKind      `json:"unavailable_kinds,omitempty"`
	TargetProfiles      []TargetProfileSummary `json:"target_profiles,omitempty"`
}

type CapabilityPolicy struct {
	ConfirmationRequired     bool `json:"confirmation_required"`
	TrustedApprovalAvailable bool `json:"trusted_approval_available"`
	ManageExistingTasks      bool `json:"manage_existing_tasks"`
	DeleteFiles              bool `json:"delete_files"`
}

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
	Kind         string         `json:"kind"` // magnet, torrent, http_file, cloud_share, unknown
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

type AcquisitionGoal string

const (
	// GoalSaveToCloud asks a cloud adapter to persist a share in the requested
	// OpenList destination. It does not imply that the media has been downloaded
	// to a local target.
	GoalSaveToCloud AcquisitionGoal = "save_to_cloud"
	// GoalDownloadToLocal asks a download adapter to place media in its
	// requested local or remote target. OpenList implements this by COPYing a
	// known file into the requested OpenList directory.
	GoalDownloadToLocal AcquisitionGoal = "download_to_local"
)

type OperationKind string

const (
	OperationDownload      OperationKind = "download"
	OperationShareTransfer OperationKind = "share_transfer"
	OperationOpenListCopy  OperationKind = "openlist_copy"
)

type CopyOptions struct {
	Overwrite    bool `json:"overwrite"`
	SkipExisting bool `json:"skip_existing"`
	Merge        bool `json:"merge"`
}

type JobPhase string

const (
	PhaseQueued       JobPhase = "queued"
	PhaseSubmitting   JobPhase = "submitting"
	PhaseTransferring JobPhase = "transferring"
	PhaseCopying      JobPhase = "copying"
	PhaseSaved        JobPhase = "saved"
	PhaseResolving    JobPhase = "resolving"
	PhaseDownloading  JobPhase = "downloading"
	PhaseDownloaded   JobPhase = "downloaded"
	PhaseFailed       JobPhase = "failed"
	PhaseCancelled    JobPhase = "cancelled"
	PhaseUncertain    JobPhase = "uncertain"
	PhaseUnsupported  JobPhase = "unsupported"
)

type FileReference struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	ETag      string `json:"etag,omitempty"`
	IsDir     bool   `json:"is_dir"`
}

type TargetReference struct {
	Backend     string          `json:"backend"`
	Instance    string          `json:"instance"`
	ProfileID   string          `json:"profile_id"`
	Kind        string          `json:"kind"`
	Operation   OperationKind   `json:"operation,omitempty"`
	Path        string          `json:"path"`
	OperationID string          `json:"operation_id,omitempty"`
	Files       []FileReference `json:"files,omitempty"`
	ObservedAt  time.Time       `json:"observed_at"`
}

type JobStatus string

type JobOwnership string

const (
	JobOwnershipUnknown  JobOwnership = "unknown"
	JobOwnershipManaged  JobOwnership = "managed"
	JobOwnershipExternal JobOwnership = "external"
)

const (
	JobQueued              JobStatus = "queued"
	JobAcquiring           JobStatus = "acquiring"
	JobDownloading         JobStatus = "downloading"
	JobDownloaded          JobStatus = "downloaded"
	JobTransferred         JobStatus = "transferred"
	JobTransferUncertain   JobStatus = "transfer_uncertain"
	JobSubmissionUncertain JobStatus = "submission_uncertain"
	JobFailed              JobStatus = "failed"
	JobCancelled           JobStatus = "cancelled"
	JobUnsupported         JobStatus = "unsupported"
)

type AcquisitionJob struct {
	ID                string           `json:"id"`
	SearchID          string           `json:"search_id"`
	CandidateID       string           `json:"candidate_id"`
	Provider          string           `json:"provider"`
	Downloader        string           `json:"downloader,omitempty"`
	Operation         OperationKind    `json:"operation"`
	Goal              AcquisitionGoal  `json:"goal"`
	TargetProfile     string           `json:"target_profile,omitempty"`
	ResultKind        string           `json:"result_kind,omitempty"`
	Phase             JobPhase         `json:"phase"`
	Status            JobStatus        `json:"status"`
	Ownership         JobOwnership     `json:"ownership,omitempty"`
	RemoteID          string           `json:"-"`
	TargetDir         string           `json:"-"`
	SourcePath        string           `json:"-"`
	CopyOptions       CopyOptions      `json:"-"`
	TargetReference   *TargetReference `json:"-"`
	IdempotencyKey    string           `json:"-"`
	RequestDigest     string           `json:"-"`
	Attempt           int              `json:"attempt,omitempty"`
	Progress          float64          `json:"progress"`
	Message           string           `json:"message,omitempty"`
	Error             string           `json:"error,omitempty"`
	UncertaintyReason string           `json:"uncertainty_reason,omitempty"`
	RecoveryAction    string           `json:"recovery_action,omitempty"`
	StatusStale       bool             `json:"status_stale"`
	LastCheckedAt     *time.Time       `json:"last_checked_at,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

func PhaseForStatus(status JobStatus) JobPhase {
	switch status {
	case JobQueued:
		return PhaseQueued
	case JobAcquiring:
		return PhaseSubmitting
	case JobDownloading:
		return PhaseDownloading
	case JobDownloaded:
		return PhaseDownloaded
	case JobTransferred:
		return PhaseSaved
	case JobTransferUncertain, JobSubmissionUncertain:
		return PhaseUncertain
	case JobFailed:
		return PhaseFailed
	case JobCancelled:
		return PhaseCancelled
	case JobUnsupported:
		return PhaseUnsupported
	default:
		return ""
	}
}
