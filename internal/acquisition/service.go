package acquisition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nas-bot/internal/domain"
	"nas-bot/internal/id"
	"nas-bot/internal/store"
)

var (
	ErrConfirmationRequired  = errors.New("explicit confirmation is required before acquisition")
	ErrDownloaderUnavailable = errors.New("no configured downloader supports this candidate")
	ErrSearchSessionExpired  = errors.New("search session expired; search again before acquiring")
)

type Service struct {
	store       *store.MemoryStore
	downloaders []Downloader
	incomingDir string
	now         func() time.Time
}

func NewService(memoryStore *store.MemoryStore, downloaders []Downloader, incomingDir string) *Service {
	return &Service{
		store:       memoryStore,
		downloaders: downloaders,
		incomingDir: incomingDir,
		now:         time.Now,
	}
}

func (s *Service) DownloaderNames() []string {
	names := make([]string, 0, len(s.downloaders))
	for _, downloader := range s.downloaders {
		names = append(names, downloader.Name())
	}
	sort.Strings(names)
	return names
}

func (s *Service) Start(ctx context.Context, candidateID string, confirmed bool) (domain.AcquisitionJob, error) {
	if !confirmed {
		return domain.AcquisitionJob{}, ErrConfirmationRequired
	}
	candidate, err := s.store.GetCandidate(candidateID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if candidate.SearchID == "" {
		return domain.AcquisitionJob{}, fmt.Errorf("candidate is not bound to a search session")
	}
	session, err := s.store.GetSearch(candidate.SearchID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if s.now().After(session.ExpiresAt) {
		return domain.AcquisitionJob{}, ErrSearchSessionExpired
	}
	if strings.TrimSpace(s.incomingDir) == "" {
		return domain.AcquisitionJob{}, fmt.Errorf("incoming download directory is not configured")
	}
	jobID := id.New("job")
	targetDir := filepath.Join(s.incomingDir, jobID)
	var selected Downloader
	for _, downloader := range s.downloaders {
		if downloader.Supports(candidate) {
			selected = downloader
			break
		}
	}
	job := domain.AcquisitionJob{
		ID:          jobID,
		SearchID:    candidate.SearchID,
		CandidateID: candidate.ID,
		Provider:    candidate.Provider,
		Status:      domain.JobQueued,
		TargetDir:   targetDir,
		CreatedAt:   s.now(),
		UpdatedAt:   s.now(),
	}
	if selected == nil {
		job.Status = domain.JobUnsupported
		job.Error = ErrDownloaderUnavailable.Error()
		job.Message = fmt.Sprintf("candidate kind %q is not supported by a configured downloader", candidate.Kind)
		s.store.SaveJob(job)
		return job, ErrDownloaderUnavailable
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("create download directory: %w", err)
	}
	job.Downloader = selected.Name()
	s.store.SaveJob(job)

	job.Status = domain.JobAcquiring
	job.UpdatedAt = s.now()
	s.store.SaveJob(job)
	handle, startErr := selected.Start(ctx, job.ID, candidate, targetDir)
	if startErr != nil {
		job.Status = domain.JobFailed
		job.Error = startErr.Error()
		job.Message = "downloader rejected the candidate"
		job.UpdatedAt = s.now()
		s.store.SaveJob(job)
		return job, startErr
	}
	job.RemoteID = handle.RemoteID
	job.Status = domain.JobDownloading
	job.Progress = 0
	job.Message = "download started"
	job.UpdatedAt = s.now()
	s.store.SaveJob(job)
	return job, nil
}

func (s *Service) Get(ctx context.Context, jobID string) (domain.AcquisitionJob, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if job.RemoteID == "" || job.Downloader == "" || job.Status == domain.JobFailed || job.Status == domain.JobCancelled {
		return job, nil
	}
	for _, downloader := range s.downloaders {
		if downloader.Name() != job.Downloader {
			continue
		}
		status, statusErr := downloader.Status(ctx, job.RemoteID)
		if statusErr != nil {
			return job, statusErr
		}
		_, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.Progress = status.Progress
			current.Message = status.Message
			current.Error = status.Error
			current.UpdatedAt = s.now()
			switch status.Status {
			case "completed", "seeding":
				current.Status = domain.JobDownloaded
			case "downloading", "checking", "checking_wait", "download_wait", "seed_wait":
				current.Status = domain.JobDownloading
			case "stopped":
				if status.Progress >= 1 {
					current.Status = domain.JobDownloaded
				}
			}
		})
		if updateErr != nil {
			return domain.AcquisitionJob{}, updateErr
		}
		return s.store.GetJob(job.ID)
	}
	return job, nil
}

func (s *Service) Cancel(ctx context.Context, jobID string) (domain.AcquisitionJob, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if job.RemoteID != "" {
		for _, downloader := range s.downloaders {
			if downloader.Name() == job.Downloader {
				if err := downloader.Cancel(ctx, job.RemoteID); err != nil {
					return job, err
				}
				break
			}
		}
	}
	updated, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.Status = domain.JobCancelled
		current.Message = "cancelled by user"
		current.UpdatedAt = s.now()
	})
	return updated, err
}
