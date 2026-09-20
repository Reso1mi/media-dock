package store

import (
	"errors"
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

func (s *MemoryStore) SaveSearch(session domain.SearchSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches[session.ID] = session
	for _, candidate := range session.Candidates {
		s.candidates[candidate.ID] = candidate
	}
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

func (s *MemoryStore) SaveJob(job domain.AcquisitionJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
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
