package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Reso1mi/media-dock/internal/acquisition"
	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/search"
	"github.com/Reso1mi/media-dock/internal/store"
)

func TestStreamableHTTPProtocolExposesSafeMediaTools(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	searchService := search.NewService(memoryStore, []search.Provider{protocolProvider{}}, time.Second, time.Minute)
	downloader := &protocolDownloader{}
	incomingDir := t.TempDir()
	acquisitionService := acquisition.NewService(memoryStore, []acquisition.Downloader{downloader}, incomingDir)

	httpServer := httptest.NewServer(NewHTTPHandler(NewServer(searchService, acquisitionService)))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "media-dock-test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL,
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	wantTools := map[string]bool{
		"media_search":       false,
		"media_acquire":      false,
		"media_job_status":   false,
		"media_job_cancel":   false,
		"media_jobs_list":    false,
		"media_capabilities": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := wantTools[tool.Name]; ok {
			wantTools[tool.Name] = true
		}
	}
	for name, found := range wantTools {
		if !found {
			t.Errorf("tools/list did not expose %q", name)
		}
	}

	capabilities := callTool(t, ctx, session, "media_capabilities", map[string]any{})
	if capabilities.IsError {
		t.Fatalf("media_capabilities returned an MCP tool error: %s", contentText(capabilities))
	}
	var capabilityOutput CapabilitiesOutput
	decodeToolOutput(t, capabilities, &capabilityOutput)
	if len(capabilityOutput.SearchProviders) != 1 || capabilityOutput.SearchProviders[0] != "protocol-test" {
		t.Fatalf("unexpected search capabilities: %#v", capabilityOutput)
	}
	if len(capabilityOutput.Downloaders) != 1 || capabilityOutput.Downloaders[0] != "protocol-test-downloader" {
		t.Fatalf("unexpected downloader capabilities: %#v", capabilityOutput)
	}
	if capabilityOutput.Transport != "streamable-http" {
		t.Fatalf("transport = %q, want streamable-http", capabilityOutput.Transport)
	}

	jobsResult := callTool(t, ctx, session, "media_jobs_list", map[string]any{"limit": 10})
	if jobsResult.IsError {
		t.Fatalf("media_jobs_list returned an MCP tool error: %s", contentText(jobsResult))
	}
	var jobsOutput JobsListOutput
	decodeToolOutput(t, jobsResult, &jobsOutput)
	if jobsOutput.Limit != 10 || jobsOutput.Offset != 0 {
		t.Fatalf("unexpected jobs list output: %#v", jobsOutput)
	}

	searchResult := callTool(t, ctx, session, "media_search", map[string]any{
		"query":   "信号",
		"quality": "1080p",
	})
	if searchResult.IsError {
		t.Fatalf("media_search returned an MCP tool error: %s", contentText(searchResult))
	}
	searchWire, _ := json.Marshal(searchResult)
	if bytes.Contains(searchWire, []byte("private-magnet")) {
		t.Fatal("MCP search output leaked the provider's raw URL")
	}
	var searchOutput SearchOutput
	decodeToolOutput(t, searchResult, &searchOutput)
	if searchOutput.SearchID == "" || len(searchOutput.Candidates) != 1 {
		t.Fatalf("unexpected search output: %#v", searchOutput)
	}
	candidate := searchOutput.Candidates[0]
	if candidate.ID == "" || !candidate.RequiresConfirmation {
		t.Fatalf("candidate confirmation metadata is missing: %#v", candidate)
	}

	unconfirmed := callTool(t, ctx, session, "media_acquire", map[string]any{
		"candidate_id": candidate.ID,
		"confirmed":    false,
	})
	if !unconfirmed.IsError {
		t.Fatal("media_acquire unexpectedly accepted an unconfirmed candidate")
	}
	var acquisitionError struct {
		Code       string `json:"code"`
		Retryable  bool   `json:"retryable"`
		NextAction string `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(contentText(unconfirmed)), &acquisitionError); err != nil {
		t.Fatalf("decode structured acquisition error: %v; text=%s", err, contentText(unconfirmed))
	}
	if acquisitionError.Code != "confirmation_required" || acquisitionError.Retryable || acquisitionError.NextAction == "" {
		t.Fatalf("unexpected acquisition error: %#v", acquisitionError)
	}
	if downloader.StartCalls() != 0 {
		t.Fatal("downloader was called before explicit confirmation")
	}

	acquireResult := callTool(t, ctx, session, "media_acquire", map[string]any{
		"candidate_id": candidate.ID,
		"confirmed":    true,
	})
	if acquireResult.IsError {
		t.Fatalf("confirmed media_acquire returned an MCP tool error: %s", contentText(acquireResult))
	}
	var job JobOutput
	decodeToolOutput(t, acquireResult, &job)
	if job.JobID == "" || job.Status != domain.JobDownloading {
		t.Fatalf("unexpected acquisition output: %#v", job)
	}
	jobWire, _ := json.Marshal(acquireResult)
	if bytes.Contains(jobWire, []byte(incomingDir)) {
		t.Fatal("MCP acquisition output leaked an internal target path")
	}

	statusResult := callTool(t, ctx, session, "media_job_status", map[string]any{"job_id": job.JobID})
	if statusResult.IsError {
		t.Fatalf("media_job_status returned an MCP tool error: %s", contentText(statusResult))
	}
	var status JobOutput
	decodeToolOutput(t, statusResult, &status)
	if status.JobID != job.JobID || status.Progress != 0.25 {
		t.Fatalf("unexpected job status: %#v", status)
	}

	cancelResult := callTool(t, ctx, session, "media_job_cancel", map[string]any{"job_id": job.JobID})
	if cancelResult.IsError {
		t.Fatalf("media_job_cancel returned an MCP tool error: %s", contentText(cancelResult))
	}
	var cancelled JobOutput
	decodeToolOutput(t, cancelResult, &cancelled)
	if cancelled.Status != domain.JobCancelled || downloader.CancelCalls() != 1 {
		t.Fatalf("unexpected cancellation output: %#v, cancel calls=%d", cancelled, downloader.CancelCalls())
	}
}

func TestBearerAuthAcceptsBearerSchemeCaseInsensitively(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := BearerAuth(next, "test-token")

	tests := []struct {
		name          string
		authorization string
		status        int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong token", authorization: "Bearer other", status: http.StatusUnauthorized},
		{name: "valid lowercase scheme", authorization: "bearer test-token", status: http.StatusNoContent},
		{name: "valid mixed case scheme", authorization: "BeArEr test-token", status: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func decodeToolOutput(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	if result.StructuredContent != nil {
		wire, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured tool output: %v", err)
		}
		if err := json.Unmarshal(wire, target); err != nil {
			t.Fatalf("decode structured tool output: %v; wire=%s", err, wire)
		}
		return
	}
	if len(result.Content) == 0 {
		t.Fatal("tool result has neither structured content nor text content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool result content type = %T, want *mcp.TextContent", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), target); err != nil {
		t.Fatalf("decode text tool output: %v; text=%s", err, text.Text)
	}
}

func contentText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return "<no content>"
	}
	if text, ok := result.Content[0].(*mcp.TextContent); ok {
		return text.Text
	}
	return "<non-text content>"
}

type protocolProvider struct{}

func (protocolProvider) Name() string { return "protocol-test" }

func (protocolProvider) Search(context.Context, domain.SearchRequest) ([]domain.Candidate, error) {
	return []domain.Candidate{
		{
			Provider: "protocol-test",
			Kind:     "magnet",
			Title:    "信号 2016 1080p 简中",
			Tags:     []string{"protocol-test"},
			RawURL:   "magnet:?xt=urn:btih:private-magnet",
		},
	}, nil
}

type protocolDownloader struct {
	mu      sync.Mutex
	starts  int
	cancels int
}

func (*protocolDownloader) Name() string { return "protocol-test-downloader" }

func (*protocolDownloader) Supports(domain.Candidate) bool { return true }

func (d *protocolDownloader) Start(context.Context, string, domain.Candidate, string) (acquisition.Handle, error) {
	d.mu.Lock()
	d.starts++
	d.mu.Unlock()
	return acquisition.Handle{RemoteID: "remote-protocol-test"}, nil
}

func (*protocolDownloader) Status(context.Context, string) (acquisition.RemoteStatus, error) {
	return acquisition.RemoteStatus{Status: "downloading", Progress: 0.25, Message: "protocol test"}, nil
}

func (d *protocolDownloader) Cancel(context.Context, string) error {
	d.mu.Lock()
	d.cancels++
	d.mu.Unlock()
	return nil
}

func (d *protocolDownloader) StartCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts
}

func (d *protocolDownloader) CancelCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancels
}
