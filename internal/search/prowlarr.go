package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nas-bot/internal/domain"
)

// ProwlarrProvider follows the common indexer API shape used by NAS media
// tools. Keeping it as a provider instead of embedding Prowlarr in the domain
// lets another indexer be added without changing the LLM-facing API.
type ProwlarrProvider struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewProwlarrProvider(baseURL, apiKey string, client *http.Client) *ProwlarrProvider {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &ProwlarrProvider{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Client:  client,
	}
}

func (p *ProwlarrProvider) Name() string { return "prowlarr" }

func (p *ProwlarrProvider) Search(ctx context.Context, request domain.SearchRequest) ([]domain.Candidate, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("prowlarr base URL is not configured")
	}
	if p.APIKey == "" {
		return nil, fmt.Errorf("prowlarr API key is not configured")
	}
	endpoint, err := url.Parse(p.BaseURL + "/api/v1/search")
	if err != nil {
		return nil, fmt.Errorf("parse prowlarr URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("query", request.Query)
	query.Set("type", "search")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create prowlarr request: %w", err)
	}
	req.Header.Set("X-Api-Key", p.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "media-scout/0.1")
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prowlarr returned HTTP %d", resp.StatusCode)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode prowlarr response: %w", err)
	}
	return parseProwlarrResults(raw, p.Name())
}

func parseProwlarrResults(raw json.RawMessage, providerName string) ([]domain.Candidate, error) {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		var wrapped struct {
			Results []map[string]any `json:"results"`
		}
		if wrappedErr := json.Unmarshal(raw, &wrapped); wrappedErr != nil {
			return nil, fmt.Errorf("prowlarr result is neither an array nor an object with results: %w", err)
		}
		items = wrapped.Results
	}

	result := make([]domain.Candidate, 0, len(items))
	for _, item := range items {
		title := stringValue(item, "title")
		if title == "" {
			continue
		}
		rawURL := firstNonEmpty(
			stringValue(item, "magnetUrl"),
			stringValue(item, "downloadUrl"),
			stringValue(item, "guid"),
		)
		if rawURL == "" {
			continue
		}
		kind := "http"
		if strings.HasPrefix(strings.ToLower(rawURL), "magnet:") {
			kind = "magnet"
		} else if strings.Contains(strings.ToLower(rawURL), ".torrent") {
			kind = "torrent"
		}
		result = append(result, domain.Candidate{
			Provider:     providerName,
			Kind:         kind,
			Title:        title,
			SourceName:   firstNonEmpty(stringValue(item, "indexer"), stringValue(item, "indexerName")),
			RawURL:       rawURL,
			SizeBytes:    int64Value(item, "size"),
			Seeders:      intValue(item, "seeders"),
			Leechers:     intValue(item, "leechers"),
			PublishedAt:  stringValue(item, "publishDate"),
			Completeness: completenessFromTitle(title),
			RawPayload:   item,
		})
	}
	return result, nil
}

func stringValue(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(item map[string]any, key string) int {
	value, ok := item[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	default:
		parsed, _ := strconv.Atoi(fmt.Sprint(value))
		return parsed
	}
}

func int64Value(item map[string]any, key string) int64 {
	value, ok := item[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := strconv.ParseInt(typed.String(), 10, 64)
		return parsed
	default:
		parsed, _ := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		return parsed
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
