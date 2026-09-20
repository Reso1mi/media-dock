package store

import (
	"errors"
	"sort"
	"sync"

	"github.com/Reso1mi/media-dock/internal/domain"
)

var ErrNotFound = errors.New("resource not found")

// MemoryStore is deliberately small for the first vertical slice. The service
// boundaries do not depend on it, so it can later be replaced by SQLite or
// PostgreSQL without changing the HTTP or provider contracts.
type MemoryStore struct {
	mu         sync.RWMutex
	searches   map[string]domain.SearchSession
	candidates map[string]domain.Candidate
	jobs       map[string]domain.AcquisitionJob
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		searches:   make(map[string]domain.SearchSession),
		candidates: make(map[string]domain.Candidate),
		jobs:       make(map[string]domain.AcquisitionJob),
	}
}

func (s *MemoryStore) SaveSearch(session domain.SearchSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches[session.ID] = session
	for _, candidate := range session.Candidates {
		s.candidates[candidate.ID] = candidate
	}
	return nil
}

func (s *MemoryStore) GetSearch(id string) (domain.SearchSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.searches[id]
	if !ok {
		return domain.SearchSession{}, ErrNotFound
	}
	return session, nil
}

func (s *MemoryStore) GetCandidate(id string) (domain.Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	candidate, ok := s.candidates[id]
	if !ok {
		return domain.Candidate{}, ErrNotFound
	}
	return candidate, nil
}

func (s *MemoryStore) SaveJob(job domain.AcquisitionJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryStore) GetJob(id string) (domain.AcquisitionJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	return job, nil
}

func (s *MemoryStore) FindJobByIdempotencyKey(key string) (domain.AcquisitionJob, error) {
	if key == "" {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, job := range s.jobs {
		if job.IdempotencyKey == key {
			return job, nil
		}
	}
	return domain.AcquisitionJob{}, ErrNotFound
}

func (s *MemoryStore) ListJobs(query JobQuery) ([]domain.AcquisitionJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]domain.AcquisitionJob, 0, len(s.jobs))
	allowed := make(map[domain.JobStatus]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		allowed[status] = struct{}{}
	}
	for _, job := range s.jobs {
		if len(allowed) > 0 {
			if _, ok := allowed[job.Status]; !ok {
				continue
			}
		}
		jobs = append(jobs, job)
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	start := query.Offset
	if start < 0 {
		start = 0
	}
	if start >= len(jobs) {
		return []domain.AcquisitionJob{}, nil
	}
	end := len(jobs)
	if query.Limit > 0 && start+query.Limit < end {
		end = start + query.Limit
	}
	return append([]domain.AcquisitionJob(nil), jobs[start:end]...), nil
}

func (s *MemoryStore) UpdateJob(id string, update func(*domain.AcquisitionJob)) (domain.AcquisitionJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	update(&job)
	s.jobs[id] = job
	return job, nil
}
