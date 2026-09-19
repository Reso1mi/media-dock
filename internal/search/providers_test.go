package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nas-bot/internal/domain"
	"nas-bot/internal/store"
)

func TestPansouProviderSearchNormalizesLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["kw"] != "信号" || request["res"] != "all" {
			t.Fatalf("unexpected request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"title":"信号 Signal 2016 1080p 简中 全集","channel":"demo","links":[{"type":"115","url":"https://115.example/share","password":"abcd"},{"type":"magnet","url":"magnet:?xt=urn:btih:test"}]}]}}`))
	}))
	defer server.Close()

	provider := NewPansouProvider(server.URL, server.Client())
	items, err := provider.Search(context.Background(), domain.SearchRequest{Query: "信号"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(items))
	}
	if items[0].Kind != "cloud" || items[0].Password != "abcd" {
		t.Fatalf("unexpected cloud candidate: %#v", items[0])
	}
	if items[1].Kind != "magnet" {
		t.Fatalf("unexpected magnet candidate: %#v", items[1])
	}
}

func TestProwlarrProviderUsesAPIKeyAndParsesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Fatalf("missing API key")
		}
		if r.URL.Query().Get("query") != "信号" || r.URL.Query().Get("type") != "search" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"Signal 2016 1080p","magnetUrl":"magnet:?xt=urn:btih:test","indexer":"tracker","size":1234,"seeders":12}]`))
	}))
	defer server.Close()

	provider := NewProwlarrProvider(server.URL, "secret", server.Client())
	items, err := provider.Search(context.Background(), domain.SearchRequest{Query: "信号"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "magnet" || items[0].Seeders != 12 {
		t.Fatalf("unexpected results: %#v", items)
	}
}

type fakeProvider struct {
	name  string
	items []domain.Candidate
	err   error
}

func (p fakeProvider) Name() string { return p.name }
func (p fakeProvider) Search(context.Context, domain.SearchRequest) ([]domain.Candidate, error) {
	return p.items, p.err
}

func TestServiceDeduplicatesRanksAndStoresOpaqueCandidates(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	service := NewService(memoryStore, []Provider{
		fakeProvider{
			name: "fake",
			items: []domain.Candidate{
				{Provider: "fake", Kind: "magnet", Title: "信号 2016 1080p 简中", RawURL: "magnet:?xt=urn:btih:same", Seeders: 2},
				{Provider: "fake", Kind: "magnet", Title: "信号 2016 1080p 简中", RawURL: "magnet:?xt=urn:btih:same", Seeders: 20},
			},
		},
	}, time.Second, time.Minute)

	result, err := service.Search(context.Background(), domain.SearchRequest{Query: "信号", Quality: "1080p", Subtitles: []string{"zh-CN"}})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(result.Session.Candidates) != 1 {
		t.Fatalf("expected one deduplicated result, got %d", len(result.Session.Candidates))
	}
	candidate := result.Session.Candidates[0]
	if candidate.Rank != 1 || candidate.SearchID != result.Session.ID || candidate.RawURL == "" {
		t.Fatalf("candidate was not bound to search session: %#v", candidate)
	}
	if candidate.Seeders != 20 {
		t.Fatalf("expected higher quality duplicate to win, got %d seeders", candidate.Seeders)
	}
	if time.Now().After(result.Session.ExpiresAt) {
		t.Fatalf("test session unexpectedly expired")
	}
}
