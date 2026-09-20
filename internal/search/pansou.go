package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
)

// PansouProvider consumes the small, stable API exposed by pansou-web. It is
// intentionally a search provider only; turning a cloud-drive link into a
// local file belongs to an acquisition adapter.
type PansouProvider struct {
	InstanceID string
	BaseURL    string
	Client     *http.Client
}

func NewPansouProvider(baseURL string, client *http.Client) *PansouProvider {
	return NewNamedPansouProvider("pansou", baseURL, client)
}

func NewNamedPansouProvider(id, baseURL string, client *http.Client) *PansouProvider {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &PansouProvider{
		InstanceID: strings.TrimSpace(id),
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Client:     client,
	}
}

func (p *PansouProvider) Name() string {
	if strings.TrimSpace(p.InstanceID) != "" {
		return strings.TrimSpace(p.InstanceID)
	}
	return "pansou"
}
func (p *PansouProvider) Type() string { return "pansou" }

type pansouResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Results []struct {
			Title   string `json:"title"`
			Channel string `json:"channel"`
			Links   []struct {
				Type     string `json:"type"`
				URL      string `json:"url"`
				Password string `json:"password"`
				Datetime string `json:"datetime"`
			} `json:"links"`
		} `json:"results"`
	} `json:"data"`
}

func (p *PansouProvider) Search(ctx context.Context, request domain.SearchRequest) ([]domain.Candidate, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("pansou base URL is not configured")
	}
	body, err := json.Marshal(map[string]string{
		"kw":  request.Query,
		"res": "all",
	})
	if err != nil {
		return nil, fmt.Errorf("encode pansou request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/api/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create pansou request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "media-dock/0.1")
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pansou request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pansou returned HTTP %d", resp.StatusCode)
	}
	var payload pansouResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode pansou response: %w", err)
	}
	if payload.Code != 0 {
		if payload.Msg == "" {
			payload.Msg = "unknown pansou error"
		}
		return nil, fmt.Errorf("pansou error %d: %s", payload.Code, payload.Msg)
	}

	items := make([]domain.Candidate, 0)
	for _, result := range payload.Data.Results {
		for _, link := range result.Links {
			if strings.TrimSpace(link.URL) == "" {
				continue
			}
			items = append(items, domain.Candidate{
				Provider:     p.Name(),
				Kind:         pansouKind(link.Type, link.URL),
				Title:        strings.TrimSpace(result.Title),
				SourceName:   strings.TrimSpace(result.Channel),
				Password:     strings.TrimSpace(link.Password),
				RawURL:       strings.TrimSpace(link.URL),
				PublishedAt:  link.Datetime,
				Completeness: completenessFromTitle(result.Title),
				RawPayload: map[string]any{
					"link_type": link.Type,
					"channel":   result.Channel,
				},
			})
		}
	}
	return items, nil
}

func pansouKind(linkType, rawURL string) string {
	return classifyResourceKind(linkType, rawURL)
}

func completenessFromTitle(title string) string {
	if strings.Contains(title, "全集") || strings.Contains(title, "全季") || strings.Contains(strings.ToLower(title), "complete") {
		return "complete"
	}
	return "unknown"
}
