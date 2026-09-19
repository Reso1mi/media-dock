package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"nas-bot/internal/acquisition"
	"nas-bot/internal/domain"
	"nas-bot/internal/llm"
	"nas-bot/internal/search"
	"nas-bot/internal/store"
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

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/providers", s.handleProviders)
	s.mux.HandleFunc("GET /api/v1/downloaders", s.handleDownloaders)
	s.mux.HandleFunc("GET /api/v1/llm/tools", s.handleTools)
	s.mux.HandleFunc("POST /api/v1/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/v1/searches/{id}", s.handleGetSearch)
	s.mux.HandleFunc("POST /api/v1/acquisitions", s.handleAcquire)
	s.mux.HandleFunc("GET /api/v1/jobs/{id}", s.handleGetJob)
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
		if errors.Is(err, search.ErrNoProviders) {
			status = http.StatusServiceUnavailable
			code = "no_search_provider"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, searchResponse(result.Session))
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
	writeJSON(w, http.StatusOK, searchResponse(session))
}

type acquireRequest struct {
	CandidateID string `json:"candidate_id"`
	Confirmed   bool   `json:"confirmed"`
}

func (s *Server) handleAcquire(w http.ResponseWriter, r *http.Request) {
	var request acquireRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	job, err := s.acquisition.Start(r.Context(), request.CandidateID, request.Confirmed)
	if err != nil {
		status := http.StatusBadRequest
		code := "acquisition_failed"
		switch {
		case errors.Is(err, acquisition.ErrConfirmationRequired):
			status = http.StatusPreconditionRequired
			code = "confirmation_required"
		case errors.Is(err, acquisition.ErrSearchSessionExpired):
			status = http.StatusGone
			code = "search_session_expired"
		case errors.Is(err, acquisition.ErrDownloaderUnavailable):
			status = http.StatusNotImplemented
			code = "downloader_unavailable"
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
			code = "candidate_not_found"
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": err.Error(), "job": job}})
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.acquisition.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job_not_found", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "job_status_failed", err.Error())
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
		writeError(w, http.StatusBadGateway, "job_cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func searchResponse(session domain.SearchSession) map[string]any {
	candidates := make([]map[string]any, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
