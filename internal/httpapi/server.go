package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Reso1mi/media-dock/internal/acquisition"
	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/llm"
	"github.com/Reso1mi/media-dock/internal/search"
	"github.com/Reso1mi/media-dock/internal/store"
)

type Server struct {
	search      *search.Service
	acquisition *acquisition.Service
	logger      *log.Logger
	mux         *http.ServeMux
}

func NewServer(searchService *search.Service, acquisitionService *acquisition.Service, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{
		search:      searchService,
		acquisition: acquisitionService,
		logger:      logger,
		mux:         http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return withJSONHeaders(s.mux)
}

// HealthHandler exposes only the liveness probe so the process can be checked
// without granting access to the protected business API.
func (s *Server) HealthHandler() http.Handler {
	return withJSONHeaders(http.HandlerFunc(s.handleHealth))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/providers", s.handleProviders)
	s.mux.HandleFunc("GET /api/v1/downloaders", s.handleDownloaders)
	s.mux.HandleFunc("GET /api/v1/capabilities", s.handleCapabilities)
	s.mux.HandleFunc("GET /api/v1/llm/tools", s.handleTools)
	s.mux.HandleFunc("POST /api/v1/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/v1/searches/{id}", s.handleGetSearch)
	s.mux.HandleFunc("GET /api/v1/searches/{id}/sources", s.handleSearchSources)
	s.mux.HandleFunc("POST /api/v1/acquisitions", s.handleAcquire)
	s.mux.HandleFunc("POST /api/v1/copies", s.handleCopy)
	s.mux.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	s.mux.HandleFunc("GET /api/v1/jobs/{id}", s.handleGetJob)
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/reconcile", s.handleReconcileJob)
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.handleCancelJob)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": s.search.ProviderNames()})
}

func (s *Server) handleDownloaders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"downloaders": s.acquisition.DownloaderNames()})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	acquisitionCapabilities, downloaders := s.acquisition.Capabilities()
	mode := "search_only"
	if len(downloaders) > 0 {
		mode = "search_and_acquire"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version":  1,
		"mode":            mode,
		"providers":       s.search.Capabilities(),
		"acquisition":     acquisitionCapabilities,
		"target_profiles": acquisitionCapabilities.TargetProfiles,
		"policy": domain.CapabilityPolicy{
			ConfirmationRequired:     true,
			TrustedApprovalAvailable: false,
			ManageExistingTasks:      false,
			DeleteFiles:              false,
		},
	})
}

func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": llm.Definitions()})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var request domain.SearchRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.search.Search(r.Context(), request)
	if err != nil {
		status := http.StatusBadGateway
		code := "search_failed"
		switch {
		case errors.Is(err, search.ErrInvalidRequest):
			status = http.StatusBadRequest
			code = "invalid_request"
		case errors.Is(err, search.ErrNoProviders):
			status = http.StatusServiceUnavailable
			code = "no_search_provider"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, searchResponse(result.Session, s.acquisition))
}

func (s *Server) handleGetSearch(w http.ResponseWriter, r *http.Request) {
	session, err := s.search.GetSearch(r.PathValue("id"))
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "expired") {
			status = http.StatusGone
		}
		writeError(w, status, "search_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, searchResponse(session, s.acquisition))
}

// handleSearchSources is intentionally separate from searchResponse: the
// human-facing WebUI may show the original source URL and share password, but
// normal search responses and MCP output must keep those private fields hidden.
func (s *Server) handleSearchSources(w http.ResponseWriter, r *http.Request) {
	session, err := s.search.GetSearch(r.PathValue("id"))
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "expired") {
			status = http.StatusGone
		}
		writeError(w, status, "search_not_found", err.Error())
		return
	}

	sources := make([]map[string]any, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
		sourceType := ""
		if candidate.RawPayload != nil {
			if value, ok := candidate.RawPayload["link_type"].(string); ok {
				sourceType = value
			}
		}
		sources = append(sources, map[string]any{
			"candidate_id": candidate.ID,
			"title":        candidate.Title,
			"provider":     candidate.Provider,
			"source_name":  candidate.SourceName,
			"source_type":  sourceType,
			"kind":         candidate.Kind,
			"raw_url":      candidate.RawURL,
			"password":     candidate.Password,
			"published_at": candidate.PublishedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"search_id":  session.ID,
		"expires_at": session.ExpiresAt,
		"sources":    sources,
	})
}

type acquireRequest struct {
	CandidateID    string                 `json:"candidate_id"`
	Goal           domain.AcquisitionGoal `json:"goal,omitempty"`
	TargetProfile  string                 `json:"target_profile,omitempty"`
	TargetDir      string                 `json:"target_dir,omitempty"`
	Confirmed      bool                   `json:"confirmed"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
}

type copyRequest struct {
	TargetProfile  string `json:"target_profile,omitempty"`
	SourcePath     string `json:"source_path"`
	TargetDir      string `json:"target_dir"`
	Overwrite      bool   `json:"overwrite,omitempty"`
	SkipExisting   bool   `json:"skip_existing,omitempty"`
	Merge          bool   `json:"merge,omitempty"`
	Confirmed      bool   `json:"confirmed"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (s *Server) handleAcquire(w http.ResponseWriter, r *http.Request) {
	var request acquireRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	idempotencyKey := request.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = r.Header.Get("Idempotency-Key")
	}
	job, err := s.acquisition.StartRequest(r.Context(), acquisition.AcquisitionRequest{
		CandidateID:    request.CandidateID,
		Goal:           request.Goal,
		TargetProfile:  request.TargetProfile,
		TargetDir:      request.TargetDir,
		Confirmed:      request.Confirmed,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, acquisition.ErrAcquisitionOutcomeUncertain) {
			writeJSON(w, http.StatusAccepted, job)
			return
		}
		status := http.StatusBadRequest
		code := "acquisition_failed"
		switch {
		case errors.Is(err, acquisition.ErrConfirmationRequired):
			status = http.StatusPreconditionRequired
			code = "confirmation_required"
		case errors.Is(err, acquisition.ErrInvalidRequest):
			status = http.StatusBadRequest
			code = "invalid_request"
		case errors.Is(err, acquisition.ErrSearchSessionExpired):
			status = http.StatusGone
			code = "search_session_expired"
		case errors.Is(err, acquisition.ErrDownloaderUnavailable):
			status = http.StatusNotImplemented
			code = "downloader_unavailable"
		case errors.Is(err, acquisition.ErrIdempotencyConflict):
			status = http.StatusConflict
			code = "idempotency_conflict"
		case errors.Is(err, acquisition.ErrTargetProfileRequired):
			status = http.StatusBadRequest
			code = "target_profile_required"
		case errors.Is(err, acquisition.ErrUnknownTargetProfile):
			status = http.StatusBadRequest
			code = "unknown_target_profile"
		case errors.Is(err, acquisition.ErrGoalUnsupported):
			status = http.StatusNotImplemented
			code = "goal_unsupported"
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
			code = "candidate_not_found"
		}
		writeJSON(w, status, map[string]any{"error": errorPayload(code, err.Error()), "job": job})
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	var request copyRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	idempotencyKey := request.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = r.Header.Get("Idempotency-Key")
	}
	job, err := s.acquisition.StartCopy(r.Context(), acquisition.CopyAcquisitionRequest{
		TargetProfile:  request.TargetProfile,
		SourcePath:     request.SourcePath,
		TargetDir:      request.TargetDir,
		Options:        domain.CopyOptions{Overwrite: request.Overwrite, SkipExisting: request.SkipExisting, Merge: request.Merge},
		Confirmed:      request.Confirmed,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, acquisition.ErrAcquisitionOutcomeUncertain) {
			writeJSON(w, http.StatusAccepted, job)
			return
		}
		status := http.StatusBadRequest
		code := "copy_failed"
		switch {
		case errors.Is(err, acquisition.ErrConfirmationRequired):
			status = http.StatusPreconditionRequired
			code = "confirmation_required"
		case errors.Is(err, acquisition.ErrInvalidRequest):
			status = http.StatusBadRequest
			code = "invalid_request"
		case errors.Is(err, acquisition.ErrDownloaderUnavailable):
			status = http.StatusNotImplemented
			code = "downloader_unavailable"
		case errors.Is(err, acquisition.ErrIdempotencyConflict):
			status = http.StatusConflict
			code = "idempotency_conflict"
		case errors.Is(err, acquisition.ErrTargetProfileRequired):
			status = http.StatusBadRequest
			code = "target_profile_required"
		case errors.Is(err, acquisition.ErrUnknownTargetProfile):
			status = http.StatusBadRequest
			code = "unknown_target_profile"
		}
		writeJSON(w, status, map[string]any{"error": errorPayload(code, err.Error()), "job": job})
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 20, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<31-1)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	statuses, err := acquisition.ParseJobStatuses(r.URL.Query()["status"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	jobs, err := s.acquisition.ListJobs(store.JobQuery{Limit: limit, Offset: offset, Statuses: statuses})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":   jobs,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.acquisition.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		if job.ID != "" && job.StatusStale {
			writeJSON(w, http.StatusOK, job)
			return
		}
		writeError(w, http.StatusBadGateway, "job_status_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleReconcileJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.acquisition.Reconcile(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		if errors.Is(err, acquisition.ErrReconcileUnavailable) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": errorPayload("reconcile_unavailable", err.Error()),
				"job":   job,
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": errorPayload("reconcile_failed", err.Error()),
			"job":   job,
		})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.acquisition.Cancel(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		if errors.Is(err, acquisition.ErrRemoteTaskNotManaged) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": errorPayload("remote_task_not_managed", err.Error()),
				"job":   job,
			})
			return
		}
		if errors.Is(err, acquisition.ErrJobNotCancellable) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": errorPayload("job_not_cancellable", err.Error()),
				"job":   job,
			})
			return
		}
		writeError(w, http.StatusBadGateway, "job_cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func searchResponse(session domain.SearchSession, acquisitionService *acquisition.Service) map[string]any {
	candidates := make([]map[string]any, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
		available, reason := acquisitionService.CandidateAvailability(candidate)
		// RawURL, password and RawPayload intentionally never cross this API
		// boundary. The LLM only needs a stable candidate handle and safe facts;
		// the acquisition adapter resolves the handle server-side after approval.
		candidates = append(candidates, map[string]any{
			"id":                    candidate.ID,
			"rank":                  candidate.Rank,
			"provider":              candidate.Provider,
			"kind":                  candidate.Kind,
			"title":                 candidate.Title,
			"source_name":           candidate.SourceName,
			"size_bytes":            candidate.SizeBytes,
			"quality":               candidate.Quality,
			"codec":                 candidate.Codec,
			"audio":                 candidate.Audio,
			"subtitles":             candidate.Subtitles,
			"seeders":               candidate.Seeders,
			"leechers":              candidate.Leechers,
			"completeness":          candidate.Completeness,
			"published_at":          candidate.PublishedAt,
			"tags":                  candidate.Tags,
			"score":                 candidate.Score,
			"requires_confirmation": true,
			"acquisition": map[string]any{
				"available":       available,
				"reason":          reason,
				"supported_goals": acquisitionService.CandidateGoals(candidate),
			},
		})
	}
	return map[string]any{
		"search_id":         session.ID,
		"request":           session.Request,
		"candidates":        candidates,
		"provider_health":   session.ProviderHealth,
		"warnings":          session.Warnings,
		"created_at":        session.CreatedAt,
		"expires_at":        session.ExpiresAt,
		"confirmation_hint": "必须先把候选结果展示给用户，并获得明确选择后，才能调用 media_acquire。",
	}
}

func queryInt(r *http.Request, key string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func errorPayload(code, message string) map[string]any {
	retryable := true
	nextAction := "retry_or_check_service_logs"
	switch code {
	case "invalid_request":
		retryable = false
		nextAction = "fix_arguments"
	case "confirmation_required":
		retryable = false
		nextAction = "show_candidate_and_confirm"
	case "candidate_not_found", "job_not_found", "candidate_expired", "search_session_expired":
		retryable = false
		nextAction = "search_again_or_list_jobs"
	case "downloader_unavailable":
		retryable = false
		nextAction = "inspect_capabilities_or_configuration"
	case "idempotency_conflict":
		retryable = false
		nextAction = "use_a_new_idempotency_key"
	case "target_profile_required":
		retryable = false
		nextAction = "inspect_capabilities_and_choose_a_target_profile"
	case "unknown_target_profile", "goal_unsupported":
		retryable = false
		nextAction = "inspect_capabilities_or_choose_a_supported_goal"
	case "job_not_cancellable":
		retryable = false
		nextAction = "inspect_current_job_state"
	case "remote_task_not_managed":
		retryable = false
		nextAction = "inspect_the_downloader_directly"
	case "reconcile_unavailable":
		retryable = false
		nextAction = "inspect_the_external_service_directly"
	case "reconcile_failed":
		nextAction = "retry_reconciliation_without_resubmitting"
	case "unauthorized":
		retryable = false
		nextAction = "provide_valid_bearer_token"
	}
	return map[string]any{
		"code":        code,
		"message":     message,
		"retryable":   retryable,
		"next_action": nextAction,
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": errorPayload(code, message)})
}
