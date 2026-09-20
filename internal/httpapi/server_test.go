package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Reso1mi/media-dock/internal/acquisition"
	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/llm"
	"github.com/Reso1mi/media-dock/internal/search"
	"github.com/Reso1mi/media-dock/internal/store"
)

type apiFakeProvider struct{}

func (apiFakeProvider) Name() string { return "fake" }
func (apiFakeProvider) Search(context.Context, domain.SearchRequest) ([]domain.Candidate, error) {
	return []domain.Candidate{{
		Provider: "fake",
		Kind:     "magnet",
		Title:    "信号 2016 1080p 简中",
		RawURL:   "magnet:?xt=urn:btih:private",
	}}, nil
}

func TestSearchAPIHidesRawCandidatePayloadAndRequiresConfirmation(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, []search.Provider{apiFakeProvider{}}, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, nil, t.TempDir())
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewBufferString(`{"query":"信号","quality":"1080p"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("search returned %d: %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private")) {
		t.Fatal("raw candidate URL leaked through search API")
	}
	var payload struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].ID == "" {
		t.Fatalf("unexpected candidate response: %#v", payload)
	}

	acquireBody := bytes.NewBufferString(`{"candidate_id":"` + payload.Candidates[0].ID + `","confirmed":false}`)
	acquireRequest := httptest.NewRequest(http.MethodPost, "/api/v1/acquisitions", acquireBody)
	acquireResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(acquireResponse, acquireRequest)
	if acquireResponse.Code != http.StatusPreconditionRequired {
		t.Fatalf("unconfirmed acquisition returned %d: %s", acquireResponse.Code, acquireResponse.Body.String())
	}
}

func TestToolsEndpointReturnsLLMDefinitions(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, nil, t.TempDir())
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/llm/tools", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("tools returned %d", response.Code)
	}
	var payload struct {
		Tools []llm.ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tool definitions: %v", err)
	}
	if len(payload.Tools) != 6 {
		t.Fatalf("tool definition count = %d, want 6", len(payload.Tools))
	}
	seen := make(map[string]bool, len(payload.Tools))
	for _, tool := range payload.Tools {
		seen[tool.Function.Name] = true
	}
	for _, name := range []string{"media_search", "media_acquire", "media_job_status", "media_job_cancel", "media_jobs_list", "media_capabilities"} {
		if !seen[name] {
			t.Errorf("missing tool definition %q", name)
		}
	}
}

func TestCapabilitiesEndpointReportsUnavailableKindsWithoutDownloader(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, nil, "")
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Mode        string `json:"mode"`
		Acquisition struct {
			SupportedKinds   []string `json:"supported_kinds"`
			UnavailableKinds []struct {
				Kind   string `json:"kind"`
				Reason string `json:"reason"`
			} `json:"unavailable_kinds"`
		} `json:"acquisition"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.Mode != "search_only" || len(payload.Acquisition.SupportedKinds) != 0 {
		t.Fatalf("unexpected capabilities: %#v", payload)
	}
	if len(payload.Acquisition.UnavailableKinds) == 0 || payload.Acquisition.UnavailableKinds[0].Reason != "missing_acquirer" {
		t.Fatalf("missing-acquirer reason was not reported: %#v", payload.Acquisition.UnavailableKinds)
	}
}

func TestJobsListEndpointSupportsStatusFilter(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	if err := memoryStore.SaveJob(domain.AcquisitionJob{ID: "job-list", Status: domain.JobFailed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save job: %v", err)
	}
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, nil, "")
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?status=failed", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("job-list")) {
		t.Fatalf("unexpected jobs list response: %d %s", response.Code, response.Body.String())
	}
}

func TestDownloadersEndpointReflectsEnabledAdapters(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, []acquisition.Downloader{
		fakeDownloader{name: "transmission"},
	}, t.TempDir())
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloaders", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("transmission")) {
		t.Fatalf("unexpected downloaders response: %d %s", response.Code, response.Body.String())
	}
}

type fakeDownloader struct {
	name string
}

func (d fakeDownloader) Name() string                   { return d.name }
func (d fakeDownloader) Supports(domain.Candidate) bool { return true }
func (d fakeDownloader) Start(context.Context, string, domain.Candidate, string) (acquisition.Handle, error) {
	return acquisition.Handle{RemoteID: "fake"}, nil
}
func (d fakeDownloader) Status(context.Context, string) (acquisition.RemoteStatus, error) {
	return acquisition.RemoteStatus{Status: "downloading"}, nil
}
func (d fakeDownloader) Cancel(context.Context, string) error { return nil }
