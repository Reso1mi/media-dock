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

	"github.com/Reso1mi/media-dock/internal/domain"
)

// ProwlarrProvider follows the common indexer API shape used by NAS media
// tools. Keeping it as a provider instead of embedding Prowlarr in the domain
// lets another indexer be added without changing the LLM-facing API.
type ProwlarrProvider struct {
	InstanceID string
	BaseURL    string
	APIKey     string
	Client     *http.Client
}

func NewProwlarrProvider(baseURL, apiKey string, client *http.Client) *ProwlarrProvider {
	return NewNamedProwlarrProvider("prowlarr", baseURL, apiKey, client)
}

func NewNamedProwlarrProvider(id, baseURL, apiKey string, client *http.Client) *ProwlarrProvider {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &ProwlarrProvider{
		InstanceID: strings.TrimSpace(id),
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		Client:     client,
	}
}

func (p *ProwlarrProvider) Name() string {
	if strings.TrimSpace(p.InstanceID) != "" {
		return strings.TrimSpace(p.InstanceID)
	}
	return "prowlarr"
}
func (p *ProwlarrProvider) Type() string { return "prowlarr" }

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
	req.Header.Set("User-Agent", "media-dock/0.1")
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
	items, err := parseProwlarrResults(raw, p.Name())
	if err != nil {
		return nil, err
	}
	// Prowlarr may wrap magnetUrl in an authenticated /download redirect.
	// Resolve only same-origin proxy URLs and never follow the redirect: the
	// Location is the magnet itself, including trackers needed by private BT.
	for i := range items {
		proxyURL := stringValue(items[i].RawPayload, "magnetUrl")
		if magnet := p.resolveMagnetProxy(ctx, proxyURL); magnet != "" {
			items[i].RawURL = magnet
			items[i].Kind = domain.CandidateKindMagnet
		}
	}
	return items, nil
}

func (p *ProwlarrProvider) resolveMagnetProxy(ctx context.Context, rawURL string) string {
	proxyURL, err := url.Parse(rawURL)
	if err != nil || !isHTTPURL(rawURL) {
		return ""
	}
	baseURL, err := url.Parse(p.BaseURL)
	if err != nil || proxyURL.Scheme != baseURL.Scheme || proxyURL.Host != baseURL.Host || proxyURL.User != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL.String(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-Api-Key", p.APIKey)
	client := *p.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.HasPrefix(strings.ToLower(location), "magnet:?") {
		return location
	}
	return ""
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
		kind := classifyResourceKind(stringValue(item, "protocol"), rawURL)
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
