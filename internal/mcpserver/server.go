// Package mcpserver exposes the MediaDock application through the official
// Model Context Protocol Go SDK. The MCP layer is deliberately thin: all
// business rules, confirmation gates, provider selection and downloader
// integration remain in the application services.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Reso1mi/media-dock/internal/acquisition"
	"github.com/Reso1mi/media-dock/internal/auth"
	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/search"
	"github.com/Reso1mi/media-dock/internal/store"
)

const serverVersion = "0.3.0"

type Handler struct {
	search      *search.Service
	acquisition *acquisition.Service
}

type SearchInput struct {
	Query     string   `json:"query" jsonschema:"media title to search for"`
	MediaType string   `json:"media_type,omitempty" jsonschema:"one of movie, tv, or anime"`
	Year      *int     `json:"year,omitempty" jsonschema:"release year when known"`
	Quality   string   `json:"quality,omitempty" jsonschema:"preferred quality such as 1080p or 2160p"`
	Subtitles []string `json:"subtitles,omitempty" jsonschema:"preferred subtitle languages such as zh-CN"`
	Sources   []string `json:"sources,omitempty" jsonschema:"optional provider names such as pansou or prowlarr"`
	Limit     int      `json:"limit,omitempty" jsonschema:"maximum number of candidates, defaults to 20"`
}

type AcquireInput struct {
	CandidateID    string                 `json:"candidate_id" jsonschema:"candidate ID returned by media_search"`
	Goal           domain.AcquisitionGoal `json:"goal,omitempty" jsonschema:"save_to_cloud for an OpenList share, or download_to_local for a configured download adapter; defaults by candidate kind"`
	TargetProfile  string                 `json:"target_profile,omitempty" jsonschema:"server-configured target profile from media_capabilities"`
	TargetDir      string                 `json:"target_dir,omitempty" jsonschema:"OpenList destination path for save_to_cloud; it is passed to the configured adapter and validated there"`
	Confirmed      bool                   `json:"confirmed" jsonschema:"must be true only after the user explicitly selected this candidate and destination"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty" jsonschema:"optional stable key used to safely retry the same acquisition request"`
}

type CopyInput struct {
	TargetProfile  string `json:"target_profile,omitempty" jsonschema:"OpenList target profile from media_capabilities"`
	SourcePath     string `json:"source_path" jsonschema:"absolute OpenList file path to COPY"`
	TargetDir      string `json:"target_dir" jsonschema:"absolute OpenList destination directory"`
	Overwrite      bool   `json:"overwrite,omitempty" jsonschema:"replace an existing target file; defaults to false"`
	SkipExisting   bool   `json:"skip_existing,omitempty" jsonschema:"skip an existing target file; defaults to true"`
	Merge          bool   `json:"merge,omitempty" jsonschema:"OpenList merge behavior; use only when explicitly supported"`
	Confirmed      bool   `json:"confirmed" jsonschema:"must be true after the user explicitly confirmed source and destination"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional stable key used to safely retry the same COPY request"`
}

type JobInput struct {
	JobID string `json:"job_id" jsonschema:"acquisition job ID"`
}

type JobsListInput struct {
	Limit    int      `json:"limit,omitempty" jsonschema:"maximum number of jobs, defaults to 20 and is capped at 100"`
	Offset   int      `json:"offset,omitempty" jsonschema:"number of jobs to skip"`
	Statuses []string `json:"statuses,omitempty" jsonschema:"optional statuses such as downloading, downloaded, transferred, transfer_uncertain, submission_uncertain, or failed"`
}

type CandidateAcquisitionOutput struct {
	Available      bool                     `json:"available"`
	Reason         string                   `json:"reason,omitempty"`
	SupportedGoals []domain.AcquisitionGoal `json:"supported_goals,omitempty"`
}

type CandidateOutput struct {
	ID                   string                     `json:"id"`
	Rank                 int                        `json:"rank"`
	Provider             string                     `json:"provider"`
	Kind                 string                     `json:"kind"`
	Title                string                     `json:"title"`
	SourceName           string                     `json:"source_name,omitempty"`
	SizeBytes            int64                      `json:"size_bytes,omitempty"`
	Quality              string                     `json:"quality,omitempty"`
	Codec                string                     `json:"codec,omitempty"`
	Audio                string                     `json:"audio,omitempty"`
	Subtitles            []string                   `json:"subtitles,omitempty"`
	Tags                 []string                   `json:"tags,omitempty"`
	Seeders              int                        `json:"seeders,omitempty"`
	Leechers             int                        `json:"leechers,omitempty"`
	Completeness         string                     `json:"completeness,omitempty"`
	PublishedAt          string                     `json:"published_at,omitempty"`
	Score                float64                    `json:"score"`
	RequiresConfirmation bool                       `json:"requires_confirmation"`
	Acquisition          CandidateAcquisitionOutput `json:"acquisition"`
}

type SearchOutput struct {
	SearchID         string                  `json:"search_id"`
	Request          domain.SearchRequest    `json:"request"`
	Candidates       []CandidateOutput       `json:"candidates"`
	ProviderHealth   []domain.ProviderHealth `json:"provider_health"`
	Warnings         []string                `json:"warnings,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
	ExpiresAt        time.Time               `json:"expires_at"`
	ConfirmationHint string                  `json:"confirmation_hint"`
}

type JobOutput struct {
	JobID             string                 `json:"job_id"`
	SearchID          string                 `json:"search_id,omitempty"`
	CandidateID       string                 `json:"candidate_id,omitempty"`
	Downloader        string                 `json:"downloader,omitempty"`
	Operation         domain.OperationKind   `json:"operation"`
	Goal              domain.AcquisitionGoal `json:"goal"`
	TargetProfile     string                 `json:"target_profile,omitempty"`
	ResultKind        string                 `json:"result_kind,omitempty"`
	Phase             domain.JobPhase        `json:"phase"`
	Status            domain.JobStatus       `json:"status"`
	Ownership         domain.JobOwnership    `json:"ownership,omitempty"`
	CanCancel         bool                   `json:"can_cancel"`
	StatusStale       bool                   `json:"status_stale"`
	LastCheckedAt     *time.Time             `json:"last_checked_at,omitempty"`
	Progress          float64                `json:"progress"`
	Message           string                 `json:"message,omitempty"`
	Error             string                 `json:"error,omitempty"`
	UncertaintyReason string                 `json:"uncertainty_reason,omitempty"`
	RecoveryAction    string                 `json:"recovery_action,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

type JobsListOutput struct {
	Jobs   []JobOutput `json:"jobs"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type CapabilitiesOutput struct {
	SchemaVersion   int                            `json:"schema_version"`
	Mode            string                         `json:"mode"`
	Providers       []domain.ComponentCapability   `json:"providers"`
	Acquisition     domain.AcquisitionCapabilities `json:"acquisition"`
	Policy          domain.CapabilityPolicy        `json:"policy"`
	SearchProviders []string                       `json:"search_providers"`
	Downloaders     []string                       `json:"downloaders"`
	Transport       string                         `json:"transport"`
	Confirmation    string                         `json:"confirmation"`
	TargetProfiles  []domain.TargetProfileSummary  `json:"target_profiles"`
}

func NewServer(searchService *search.Service, acquisitionService *acquisition.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "media-dock",
		Title:   "MediaDock Media Orchestrator",
		Version: serverVersion,
	}, &mcp.ServerOptions{
		Instructions: "Use media_search first for media candidates. Show candidates and obtain explicit confirmation before media_acquire. Use media_copy only for a user-confirmed OpenList source file and destination. OpenList paths are passed to the adapter; raw provider URLs and credentials are never exposed through MCP.",
	})

	handlers := &Handler{search: searchService, acquisition: acquisitionService}
	readOnly := true
	openWorld := true
	additive := false
	destructive := true

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_search",
		Title:       "Search media resources",
		Description: "Search movies, TV shows, and anime across configured providers. This is read-only and never starts a download.",
		Annotations: &mcp.ToolAnnotations{Title: "Search media resources", ReadOnlyHint: true, OpenWorldHint: &openWorld},
	}, handlers.searchMedia)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_acquire",
		Title:       "Acquire a confirmed media candidate",
		Description: "Start acquisition for a candidate only after the user explicitly selected and confirmed it. The candidate must come from media_search. Use goal=save_to_cloud for a supported OpenList share and pass target_dir when selecting its destination; use goal=download_to_local for a configured download adapter.",
		Annotations: &mcp.ToolAnnotations{Title: "Acquire confirmed media", ReadOnlyHint: false, DestructiveHint: &additive, OpenWorldHint: &openWorld},
	}, handlers.acquireMedia)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_copy",
		Title:       "COPY a confirmed OpenList file",
		Description: "COPY one confirmed OpenList file to a confirmed target directory through the configured OpenList adapter. This is a media business operation, not a raw OpenList API proxy.",
		Annotations: &mcp.ToolAnnotations{Title: "COPY OpenList file", ReadOnlyHint: false, DestructiveHint: &additive, OpenWorldHint: &openWorld},
	}, handlers.copyMedia)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_job_status",
		Title:       "Get acquisition job status",
		Description: "Read the current status of an acquisition job.",
		Annotations: &mcp.ToolAnnotations{Title: "Get acquisition job status", ReadOnlyHint: readOnly, OpenWorldHint: &openWorld},
	}, handlers.jobStatus)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_job_reconcile",
		Title:       "Reconcile an uncertain acquisition",
		Description: "Check the configured external destination for an uncertain acquisition. This never resubmits the transfer and may still require manual verification.",
		Annotations: &mcp.ToolAnnotations{Title: "Reconcile uncertain acquisition", ReadOnlyHint: readOnly, OpenWorldHint: &openWorld},
	}, handlers.reconcileJob)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_job_cancel",
		Title:       "Cancel an acquisition job",
		Description: "Cancel an active acquisition job when its adapter supports cancellation. OpenList share transfer is synchronous and cannot be cancelled after submission; an uncertain result must be checked at the configured destination.",
		Annotations: &mcp.ToolAnnotations{Title: "Cancel acquisition job", ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &openWorld},
	}, handlers.cancelJob)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_jobs_list",
		Title:       "List acquisition jobs",
		Description: "Find recent, active, or failed acquisition jobs after a chat session was interrupted. This is read-only and supports bounded pagination and status filters.",
		Annotations: &mcp.ToolAnnotations{Title: "List acquisition jobs", ReadOnlyHint: readOnly, OpenWorldHint: &openWorld},
	}, handlers.jobsList)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_capabilities",
		Title:       "Inspect MediaDock capabilities",
		Description: "List configured search providers and downloaders before planning a media acquisition.",
		Annotations: &mcp.ToolAnnotations{Title: "Inspect capabilities", ReadOnlyHint: true, OpenWorldHint: &openWorld},
	}, handlers.capabilities)

	return server
}

func NewHTTPHandler(server *mcp.Server) http.Handler {
	// JSON responses keep ordinary tool calls bounded while retaining the
	// standard Streamable HTTP session semantics. The SDK still accepts
	// text/event-stream for clients that need server-to-client events.
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
}

// BearerAuth is kept in this package for source compatibility with existing
// callers. REST and MCP endpoints both use the same implementation from the
// auth package.
func BearerAuth(next http.Handler, token string) http.Handler {
	return auth.BearerAuth(next, token)
}

func validBearerToken(header, expected string) bool {
	return auth.ValidBearerToken(header, expected)
}

func (h *Handler) searchMedia(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	result, err := h.search.Search(ctx, domain.SearchRequest{
		Query:     input.Query,
		MediaType: domain.MediaType(input.MediaType),
		Year:      input.Year,
		Quality:   input.Quality,
		Subtitles: input.Subtitles,
		Sources:   input.Sources,
		Limit:     input.Limit,
	})
	if err != nil {
		return nil, SearchOutput{}, toolError(err)
	}
	return nil, toSearchOutput(result.Session, h.acquisition), nil
}

func (h *Handler) acquireMedia(ctx context.Context, _ *mcp.CallToolRequest, input AcquireInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.StartRequest(ctx, acquisition.AcquisitionRequest{
		CandidateID:    input.CandidateID,
		Goal:           input.Goal,
		TargetProfile:  input.TargetProfile,
		TargetDir:      input.TargetDir,
		Confirmed:      input.Confirmed,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, acquisition.ErrAcquisitionOutcomeUncertain) && job.ID != "" {
			return nil, toJobOutput(job), nil
		}
		if job.ID != "" {
			return nil, JobOutput{}, toolError(fmt.Errorf("%w (job_id=%s)", err, job.ID))
		}
		return nil, JobOutput{}, toolError(err)
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) copyMedia(ctx context.Context, _ *mcp.CallToolRequest, input CopyInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.StartCopy(ctx, acquisition.CopyAcquisitionRequest{
		TargetProfile:  input.TargetProfile,
		SourcePath:     input.SourcePath,
		TargetDir:      input.TargetDir,
		Options:        domain.CopyOptions{Overwrite: input.Overwrite, SkipExisting: input.SkipExisting, Merge: input.Merge},
		Confirmed:      input.Confirmed,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, acquisition.ErrAcquisitionOutcomeUncertain) && job.ID != "" {
			return nil, toJobOutput(job), nil
		}
		if job.ID != "" {
			return nil, JobOutput{}, toolError(fmt.Errorf("%w (job_id=%s)", err, job.ID))
		}
		return nil, JobOutput{}, toolError(err)
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) jobStatus(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Get(ctx, input.JobID)
	if err != nil {
		if job.ID != "" && job.StatusStale {
			return nil, toJobOutput(job), nil
		}
		return nil, JobOutput{}, toolError(err)
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) reconcileJob(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Reconcile(ctx, input.JobID)
	if err != nil {
		return nil, toJobOutput(job), toolError(err)
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) cancelJob(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Cancel(ctx, input.JobID)
	if err != nil {
		return nil, JobOutput{}, toolError(err)
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) jobsList(_ context.Context, _ *mcp.CallToolRequest, input JobsListInput) (*mcp.CallToolResult, JobsListOutput, error) {
	limit := input.Limit
	if limit < 0 {
		return nil, JobsListOutput{}, toolError(fmt.Errorf("limit must be between 1 and 100"))
	}
	if limit == 0 {
		limit = 20
	}
	if limit > 100 {
		return nil, JobsListOutput{}, toolError(fmt.Errorf("limit must be between 1 and 100"))
	}
	if input.Offset < 0 {
		return nil, JobsListOutput{}, toolError(fmt.Errorf("offset must not be negative"))
	}
	statuses, err := acquisition.ParseJobStatuses(input.Statuses)
	if err != nil {
		return nil, JobsListOutput{}, toolError(err)
	}
	jobs, err := h.acquisition.ListJobs(store.JobQuery{Limit: limit, Offset: input.Offset, Statuses: statuses})
	if err != nil {
		return nil, JobsListOutput{}, toolError(err)
	}
	output := JobsListOutput{Jobs: make([]JobOutput, 0, len(jobs)), Limit: limit, Offset: input.Offset}
	for _, job := range jobs {
		output.Jobs = append(output.Jobs, toJobOutput(job))
	}
	return nil, output, nil
}

func toolError(err error) error {
	code := "internal_error"
	retryable := true
	nextAction := "retry_or_check_service_logs"
	switch {
	case errors.Is(err, search.ErrInvalidRequest), errors.Is(err, acquisition.ErrInvalidRequest):
		code = "invalid_request"
		retryable = false
		nextAction = "fix_arguments"
	case errors.Is(err, acquisition.ErrConfirmationRequired):
		code = "confirmation_required"
		retryable = false
		nextAction = "show_candidate_and_confirm"
	case errors.Is(err, acquisition.ErrSearchSessionExpired):
		code = "candidate_expired"
		retryable = false
		nextAction = "search_again"
	case errors.Is(err, acquisition.ErrDownloaderUnavailable):
		code = "downloader_unavailable"
		retryable = false
		nextAction = "inspect_capabilities_or_configuration"
	case errors.Is(err, acquisition.ErrIdempotencyConflict):
		code = "idempotency_conflict"
		retryable = false
		nextAction = "use_a_new_idempotency_key"
	case errors.Is(err, acquisition.ErrTargetProfileRequired):
		code = "target_profile_required"
		retryable = false
		nextAction = "inspect_capabilities_and_choose_a_target_profile"
	case errors.Is(err, acquisition.ErrUnknownTargetProfile):
		code = "unknown_target_profile"
		retryable = false
		nextAction = "inspect_capabilities_or_choose_a_supported_profile"
	case errors.Is(err, acquisition.ErrGoalUnsupported):
		code = "goal_unsupported"
		retryable = false
		nextAction = "inspect_capabilities_or_choose_a_supported_goal"
	case errors.Is(err, acquisition.ErrAcquisitionOutcomeUncertain):
		code = "transfer_outcome_uncertain"
		retryable = false
		nextAction = "inspect_openlist_destination_before_retry"
	case errors.Is(err, acquisition.ErrJobNotCancellable):
		code = "job_not_cancellable"
		retryable = false
		nextAction = "inspect_current_job_state"
	case errors.Is(err, acquisition.ErrRemoteTaskNotManaged):
		code = "remote_task_not_managed"
		retryable = false
		nextAction = "inspect_the_downloader_directly"
	case errors.Is(err, acquisition.ErrReconcileUnavailable):
		code = "reconcile_unavailable"
		retryable = false
		nextAction = "inspect_the_external_service_directly"
	case errors.Is(err, store.ErrNotFound):
		code = "not_found"
		retryable = false
		nextAction = "search_again_or_list_jobs"
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"code":        code,
		"message":     err.Error(),
		"retryable":   retryable,
		"next_action": nextAction,
	})
	if marshalErr != nil {
		return err
	}
	return errors.New(string(payload))
}

func (h *Handler) capabilities(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, CapabilitiesOutput, error) {
	acquisitionCapabilities, downloaders := h.acquisition.Capabilities()
	mode := "search_only"
	if len(downloaders) > 0 {
		mode = "search_and_acquire"
	}
	return nil, CapabilitiesOutput{
		SchemaVersion:   1,
		Mode:            mode,
		Providers:       h.search.Capabilities(),
		Acquisition:     acquisitionCapabilities,
		Policy:          domain.CapabilityPolicy{ConfirmationRequired: true, TrustedApprovalAvailable: false, ManageExistingTasks: false, DeleteFiles: false},
		SearchProviders: h.search.ProviderNames(),
		Downloaders:     h.acquisition.DownloaderNames(),
		Transport:       "streamable-http",
		Confirmation:    "media_acquire requires confirmed=true after explicit user selection",
		TargetProfiles:  acquisitionCapabilities.TargetProfiles,
	}, nil
}

func toSearchOutput(session domain.SearchSession, acquisitionService *acquisition.Service) SearchOutput {
	candidates := make([]CandidateOutput, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
		available, reason := acquisitionService.CandidateAvailability(candidate)
		candidates = append(candidates, CandidateOutput{
			ID:                   candidate.ID,
			Rank:                 candidate.Rank,
			Provider:             candidate.Provider,
			Kind:                 candidate.Kind,
			Title:                candidate.Title,
			SourceName:           candidate.SourceName,
			SizeBytes:            candidate.SizeBytes,
			Quality:              candidate.Quality,
			Codec:                candidate.Codec,
			Audio:                candidate.Audio,
			Subtitles:            candidate.Subtitles,
			Tags:                 candidate.Tags,
			Seeders:              candidate.Seeders,
			Leechers:             candidate.Leechers,
			Completeness:         candidate.Completeness,
			PublishedAt:          candidate.PublishedAt,
			Score:                candidate.Score,
			RequiresConfirmation: true,
			Acquisition:          CandidateAcquisitionOutput{Available: available, Reason: reason, SupportedGoals: acquisitionService.CandidateGoals(candidate)},
		})
	}
	return SearchOutput{
		SearchID:         session.ID,
		Request:          session.Request,
		Candidates:       candidates,
		ProviderHealth:   session.ProviderHealth,
		Warnings:         session.Warnings,
		CreatedAt:        session.CreatedAt,
		ExpiresAt:        session.ExpiresAt,
		ConfirmationHint: "先向用户展示候选资源并获得明确选择，再调用 media_acquire。",
	}
}

func effectivePhase(job domain.AcquisitionJob) domain.JobPhase {
	if job.Phase != "" {
		return job.Phase
	}
	return domain.PhaseForStatus(job.Status)
}

func toJobOutput(job domain.AcquisitionJob) JobOutput {
	return JobOutput{
		JobID:             job.ID,
		SearchID:          job.SearchID,
		CandidateID:       job.CandidateID,
		Downloader:        job.Downloader,
		Operation:         job.Operation,
		Goal:              job.Goal,
		TargetProfile:     job.TargetProfile,
		ResultKind:        job.ResultKind,
		Phase:             effectivePhase(job),
		Status:            job.Status,
		Ownership:         job.Ownership,
		CanCancel:         acquisition.CanCancel(job),
		StatusStale:       job.StatusStale,
		LastCheckedAt:     job.LastCheckedAt,
		Progress:          job.Progress,
		Message:           job.Message,
		Error:             job.Error,
		UncertaintyReason: job.UncertaintyReason,
		RecoveryAction:    job.RecoveryAction,
		CreatedAt:         job.CreatedAt,
		UpdatedAt:         job.UpdatedAt,
	}
}
