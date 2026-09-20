package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/id"
	"github.com/Reso1mi/media-dock/internal/store"
)

var (
	ErrNoProviders    = errors.New("no search providers are configured")
	ErrInvalidRequest = errors.New("invalid search request")
)

type Provider interface {
	Name() string
	Search(context.Context, domain.SearchRequest) ([]domain.Candidate, error)
}

type SearchResult struct {
	Session domain.SearchSession
}

type Service struct {
	providers []Provider
	store     store.Store
	timeout   time.Duration
	ttl       time.Duration
	now       func() time.Time
}

func NewService(persistence store.Store, providers []Provider, timeout, ttl time.Duration) *Service {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &Service{
		providers: providers,
		store:     persistence,
		timeout:   timeout,
		ttl:       ttl,
		now:       time.Now,
	}
}

func (s *Service) ProviderNames() []string {
	names := make([]string, 0, len(s.providers))
	for _, provider := range s.providers {
		names = append(names, provider.Name())
	}
	sort.Strings(names)
	return names
}

func (s *Service) Capabilities() []domain.ComponentCapability {
	components := make([]domain.ComponentCapability, 0, len(s.providers))
	for _, provider := range s.providers {
		component := domain.ComponentCapability{
			ID:     provider.Name(),
			Type:   provider.Name(),
			State:  "unknown",
			Reason: "connection_not_checked",
		}
		if typed, ok := provider.(interface{ Type() string }); ok && strings.TrimSpace(typed.Type()) != "" {
			component.Type = typed.Type()
		}
		components = append(components, component)
	}
	return components
}

func (s *Service) Search(ctx context.Context, request domain.SearchRequest) (SearchResult, error) {
	request.Query = CleanQuery(request.Query)
	if request.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: query must not be empty", ErrInvalidRequest)
	}
	switch request.MediaType {
	case "", domain.MediaTypeMovie, domain.MediaTypeTV, domain.MediaTypeAnime:
	default:
		return SearchResult{}, fmt.Errorf("%w: media_type must be movie, tv, or anime", ErrInvalidRequest)
	}
	if request.Year != nil && (*request.Year < 1888 || *request.Year > time.Now().Year()+2) {
		return SearchResult{}, fmt.Errorf("%w: year is outside the supported range", ErrInvalidRequest)
	}
	if request.Limit < 0 || request.Limit > 100 {
		return SearchResult{}, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidRequest)
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	if len(s.providers) == 0 {
		return SearchResult{}, ErrNoProviders
	}

	selected := s.selectProviders(request.Sources)
	if len(selected) == 0 {
		return SearchResult{}, fmt.Errorf("none of the requested search providers are configured")
	}

	sessionID := id.New("search")
	startedAt := s.now()
	searchCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	type providerResult struct {
		provider string
		items    []domain.Candidate
		err      error
		duration time.Duration
	}
	results := make(chan providerResult, len(selected))
	var wg sync.WaitGroup
	for _, provider := range selected {
		provider := provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			items, err := provider.Search(searchCtx, request)
			results <- providerResult{
				provider: provider.Name(),
				items:    items,
				err:      err,
				duration: time.Since(started),
			}
		}()
	}
	wg.Wait()
	close(results)

	all := make([]domain.Candidate, 0)
	health := make([]domain.ProviderHealth, 0, len(selected))
	warnings := make([]string, 0)
	for result := range results {
		itemHealth := domain.ProviderHealth{
			Provider:  result.provider,
			Available: result.err == nil,
			Count:     len(result.items),
			Duration:  result.duration.Milliseconds(),
		}
		if result.err != nil {
			itemHealth.Error = result.err.Error()
			warnings = append(warnings, fmt.Sprintf("%s: %s", result.provider, result.err))
		} else {
			all = append(all, result.items...)
		}
		health = append(health, itemHealth)
	}

	if len(all) == 0 && len(warnings) == len(selected) {
		return SearchResult{}, fmt.Errorf("all search providers failed: %s", strings.Join(warnings, "; "))
	}

	candidates := rankAndDeduplicate(all, request, sessionID, s.now())
	if len(candidates) > request.Limit {
		candidates = candidates[:request.Limit]
	}
	for index := range candidates {
		candidates[index].Rank = index + 1
	}

	session := domain.SearchSession{
		ID:             sessionID,
		Request:        request,
		Candidates:     candidates,
		ProviderHealth: health,
		Warnings:       warnings,
		CreatedAt:      startedAt,
		ExpiresAt:      startedAt.Add(s.ttl),
	}
	if err := s.store.SaveSearch(session); err != nil {
		return SearchResult{}, fmt.Errorf("persist search session: %w", err)
	}
	return SearchResult{Session: session}, nil
}

func (s *Service) selectProviders(requested []string) []Provider {
	if len(requested) == 0 {
		return append([]Provider(nil), s.providers...)
	}
	allowed := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		allowed[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	selected := make([]Provider, 0, len(s.providers))
	for _, provider := range s.providers {
		if _, ok := allowed[strings.ToLower(provider.Name())]; ok {
			selected = append(selected, provider)
		}
	}
	return selected
}

func (s *Service) GetSearch(searchID string) (domain.SearchSession, error) {
	session, err := s.store.GetSearch(searchID)
	if err != nil {
		return domain.SearchSession{}, err
	}
	if s.now().After(session.ExpiresAt) {
		return domain.SearchSession{}, fmt.Errorf("search session expired")
	}
	return session, nil
}

func (s *Service) GetCandidate(candidateID string) (domain.Candidate, error) {
	return s.store.GetCandidate(candidateID)
}
