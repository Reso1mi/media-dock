package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nas-bot/internal/acquisition"
	"nas-bot/internal/domain"
	"nas-bot/internal/search"
	"nas-bot/internal/store"
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
	if !bytes.Contains(response.Body.Bytes(), []byte("media_search")) || !bytes.Contains(response.Body.Bytes(), []byte("media_acquire")) {
		t.Fatalf("expected LLM tool definitions: %s", response.Body.String())
	}
}
