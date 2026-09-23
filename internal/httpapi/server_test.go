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
		Password: "share-code",
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

func TestSearchSourcesEndpointReturnsRawSourceForWebUI(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, []search.Provider{apiFakeProvider{}}, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, nil, t.TempDir())
	server := NewServer(searchService, acquisitionService, nil)

	searchRequest := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewBufferString(`{"query":"信号"}`))
	searchResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(searchResponse, searchRequest)
	if searchResponse.Code != http.StatusOK {
		t.Fatalf("search returned %d: %s", searchResponse.Code, searchResponse.Body.String())
	}
	var searchPayload struct {
		SearchID   string `json:"search_id"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &searchPayload); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if searchPayload.SearchID == "" || len(searchPayload.Candidates) != 1 {
		t.Fatalf("unexpected search response: %#v", searchPayload)
	}
	if bytes.Contains(searchResponse.Body.Bytes(), []byte("private")) || bytes.Contains(searchResponse.Body.Bytes(), []byte("share-code")) {
		t.Fatal("raw source leaked through the normal search response")
	}

	sourceRequest := httptest.NewRequest(http.MethodGet, "/api/v1/searches/"+searchPayload.SearchID+"/sources", nil)
	sourceResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(sourceResponse, sourceRequest)
	if sourceResponse.Code != http.StatusOK {
		t.Fatalf("sources returned %d: %s", sourceResponse.Code, sourceResponse.Body.String())
	}
	if !bytes.Contains(sourceResponse.Body.Bytes(), []byte("magnet:?xt=urn:btih:private")) || !bytes.Contains(sourceResponse.Body.Bytes(), []byte("share-code")) {
		t.Fatalf("raw source details missing: %s", sourceResponse.Body.String())
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
	if len(payload.Tools) != 8 {
		t.Fatalf("tool definition count = %d, want 8", len(payload.Tools))
	}
	seen := make(map[string]bool, len(payload.Tools))
	for _, tool := range payload.Tools {
		seen[tool.Function.Name] = true
	}
	for _, name := range []string{"media_search", "media_acquire", "media_copy", "media_job_status", "media_job_reconcile", "media_job_cancel", "media_jobs_list", "media_capabilities"} {
		if !seen[name] {
			t.Errorf("missing tool definition %q", name)
		}
	}
}

func TestCopyAPIRequiresConfirmationAndCreatesJob(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	copyCalls := 0
	var request struct {
		SourceDir string   `json:"src_dir"`
		TargetDir string   `json:"dst_dir"`
		Names     []string `json:"names"`
		Skip      bool     `json:"skip_existing"`
	}
	openListServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/copy" {
			http.NotFound(w, r)
			return
		}
		copyCalls++
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OpenList COPY request: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
	}))
	defer openListServer.Close()
	openList, err := acquisition.NewOpenListDownloaderForProfile(openListServer.URL, "token", "openlist-http", openListServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, []acquisition.Downloader{openList}, "")
	server := NewServer(searchService, acquisitionService, nil)

	unconfirmed := httptest.NewRequest(http.MethodPost, "/api/v1/copies", bytes.NewBufferString(`{"target_profile":"openlist-http","source_path":"/Quark/source/movie.mkv","target_dir":"/Quark/library","confirmed":false}`))
	unconfirmedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unconfirmedResponse, unconfirmed)
	if unconfirmedResponse.Code != http.StatusPreconditionRequired {
		t.Fatalf("unconfirmed COPY returned %d: %s", unconfirmedResponse.Code, unconfirmedResponse.Body.String())
	}
	if copyCalls != 0 {
		t.Fatalf("OpenList COPY calls before confirmation = %d, want 0", copyCalls)
	}

	confirmed := httptest.NewRequest(http.MethodPost, "/api/v1/copies", bytes.NewBufferString(`{"target_profile":"openlist-http","source_path":"/Quark/source/movie.mkv","target_dir":"/Quark/library","confirmed":true,"idempotency_key":"copy-http-1"}`))
	confirmedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(confirmedResponse, confirmed)
	if confirmedResponse.Code != http.StatusAccepted {
		t.Fatalf("confirmed COPY returned %d: %s", confirmedResponse.Code, confirmedResponse.Body.String())
	}
	var job domain.AcquisitionJob
	if err := json.Unmarshal(confirmedResponse.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode COPY job: %v", err)
	}
	if job.Operation != domain.OperationOpenListCopy || job.Status != domain.JobDownloaded || job.Phase != domain.PhaseDownloaded || job.TargetProfile != "openlist-http" {
		t.Fatalf("unexpected COPY job: %#v", job)
	}
	if copyCalls != 1 || request.SourceDir != "/Quark/source" || request.TargetDir != "/Quark/library" || len(request.Names) != 1 || request.Names[0] != "movie.mkv" || !request.Skip {
		t.Fatalf("COPY calls/request = %d/%#v", copyCalls, request)
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

func TestAcquireAPIReportsOpenListUncertainResultAsJob(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	if err := memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-openlist",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-openlist",
			SearchID: "search-openlist",
			Provider: "fake",
			Kind:     domain.CandidateKindCloudShare,
			RawURL:   "https://pan.quark.cn/s/share123",
		}},
	}); err != nil {
		t.Fatalf("save search: %v", err)
	}

	openListServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":500,"message":"transfer failed after submission"}`))
	}))
	defer openListServer.Close()
	openList, err := acquisition.NewOpenListDownloader(openListServer.URL, "token", "/Quark/MediaDock", openListServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	searchService := search.NewService(memoryStore, nil, time.Second, time.Minute)
	acquisitionService := acquisition.NewService(memoryStore, []acquisition.Downloader{openList}, "")
	server := NewServer(searchService, acquisitionService, nil)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/acquisitions", bytes.NewBufferString(`{"candidate_id":"candidate-openlist","confirmed":true}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("acquisition returned %d: %s", response.Code, response.Body.String())
	}
	var job domain.AcquisitionJob
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode acquisition job: %v", err)
	}
	if job.Status != domain.JobTransferUncertain || job.Phase != domain.PhaseUncertain || job.Goal != domain.GoalSaveToCloud || job.TargetProfile != "openlist_default" || !job.StatusStale || job.ID == "" {
		t.Fatalf("unexpected uncertain acquisition job: %#v", job)
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
