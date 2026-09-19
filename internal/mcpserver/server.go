// Package mcpserver exposes the Media Scout application through the official
// Model Context Protocol Go SDK. The MCP layer is deliberately thin: all
// business rules, confirmation gates, provider selection and downloader
// integration remain in the application services.
package mcpserver

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"nas-bot/internal/acquisition"
	"nas-bot/internal/domain"
	"nas-bot/internal/search"
)

const serverVersion = "0.2.0"

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
	CandidateID string `json:"candidate_id" jsonschema:"candidate ID returned by media_search"`
	Confirmed   bool   `json:"confirmed" jsonschema:"must be true only after the user explicitly selected this candidate"`
}

type JobInput struct {
	JobID string `json:"job_id" jsonschema:"acquisition job ID"`
}

type CandidateOutput struct {
	ID                   string   `json:"id"`
	Rank                 int      `json:"rank"`
	Provider             string   `json:"provider"`
	Kind                 string   `json:"kind"`
	Title                string   `json:"title"`
	SourceName           string   `json:"source_name,omitempty"`
	SizeBytes            int64    `json:"size_bytes,omitempty"`
	Quality              string   `json:"quality,omitempty"`
	Codec                string   `json:"codec,omitempty"`
	Audio                string   `json:"audio,omitempty"`
	Subtitles            []string `json:"subtitles,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	Seeders              int      `json:"seeders,omitempty"`
	Leechers             int      `json:"leechers,omitempty"`
	Completeness         string   `json:"completeness,omitempty"`
	PublishedAt          string   `json:"published_at,omitempty"`
	Score                float64  `json:"score"`
	RequiresConfirmation bool     `json:"requires_confirmation"`
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
	JobID       string           `json:"job_id"`
	SearchID    string           `json:"search_id,omitempty"`
	CandidateID string           `json:"candidate_id"`
	Downloader  string           `json:"downloader,omitempty"`
	Status      domain.JobStatus `json:"status"`
	Progress    float64          `json:"progress"`
	Message     string           `json:"message,omitempty"`
	Error       string           `json:"error,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type CapabilitiesOutput struct {
	SearchProviders []string `json:"search_providers"`
	Downloaders     []string `json:"downloaders"`
	Transport       string   `json:"transport"`
	Confirmation    string   `json:"confirmation"`
}

func NewServer(searchService *search.Service, acquisitionService *acquisition.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "nas-bot",
		Title:   "NAS Bot Media Orchestrator",
		Version: serverVersion,
	}, &mcp.ServerOptions{
		Instructions: "Use media_search first. Show candidates to the user and obtain explicit confirmation before calling media_acquire. Raw provider URLs and credentials are intentionally never exposed through MCP.",
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
		Description: "Start acquisition for a candidate only after the user explicitly selected and confirmed it. The candidate must come from media_search.",
		Annotations: &mcp.ToolAnnotations{Title: "Acquire confirmed media", ReadOnlyHint: false, DestructiveHint: &additive, OpenWorldHint: &openWorld},
	}, handlers.acquireMedia)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_job_status",
		Title:       "Get acquisition job status",
		Description: "Read the current status of an acquisition job.",
		Annotations: &mcp.ToolAnnotations{Title: "Get acquisition job status", ReadOnlyHint: readOnly, OpenWorldHint: &openWorld},
	}, handlers.jobStatus)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_job_cancel",
		Title:       "Cancel an acquisition job",
		Description: "Cancel an active acquisition job. This is a user-requested side effect and must not be inferred from a failed status check.",
		Annotations: &mcp.ToolAnnotations{Title: "Cancel acquisition job", ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &openWorld},
	}, handlers.cancelJob)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "media_capabilities",
		Title:       "Inspect Media Scout capabilities",
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

// BearerAuth protects the MCP endpoint when MCP_AUTH_TOKEN is configured. An
// empty token intentionally leaves authentication to the NAS reverse proxy or
// private network, which keeps local development convenient.
func BearerAuth(next http.Handler, token string) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validBearerToken(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nas-bot-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validBearerToken(header, expected string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	provided := []byte(parts[1])
	wanted := []byte(expected)
	if len(provided) != len(wanted) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, wanted) == 1
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
		return nil, SearchOutput{}, err
	}
	return nil, toSearchOutput(result.Session), nil
}

func (h *Handler) acquireMedia(ctx context.Context, _ *mcp.CallToolRequest, input AcquireInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Start(ctx, input.CandidateID, input.Confirmed)
	if err != nil {
		if job.ID != "" {
			return nil, JobOutput{}, fmt.Errorf("%w (job_id=%s)", err, job.ID)
		}
		return nil, JobOutput{}, err
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) jobStatus(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Get(ctx, input.JobID)
	if err != nil {
		return nil, JobOutput{}, err
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) cancelJob(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (*mcp.CallToolResult, JobOutput, error) {
	job, err := h.acquisition.Cancel(ctx, input.JobID)
	if err != nil {
		return nil, JobOutput{}, err
	}
	return nil, toJobOutput(job), nil
}

func (h *Handler) capabilities(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, CapabilitiesOutput, error) {
	return nil, CapabilitiesOutput{
		SearchProviders: h.search.ProviderNames(),
		Downloaders:     h.acquisition.DownloaderNames(),
		Transport:       "streamable-http",
		Confirmation:    "media_acquire requires confirmed=true after explicit user selection",
	}, nil
}

func toSearchOutput(session domain.SearchSession) SearchOutput {
	candidates := make([]CandidateOutput, 0, len(session.Candidates))
	for _, candidate := range session.Candidates {
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

func toJobOutput(job domain.AcquisitionJob) JobOutput {
	return JobOutput{
		JobID:       job.ID,
		SearchID:    job.SearchID,
		CandidateID: job.CandidateID,
		Downloader:  job.Downloader,
		Status:      job.Status,
		Progress:    job.Progress,
		Message:     job.Message,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,
	}
}
