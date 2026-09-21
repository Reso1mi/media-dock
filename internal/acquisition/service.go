package acquisition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/id"
	"github.com/Reso1mi/media-dock/internal/store"
)

var (
	ErrConfirmationRequired  = errors.New("explicit confirmation is required before acquisition")
	ErrDownloaderUnavailable = errors.New("no configured downloader supports this candidate")
	ErrSearchSessionExpired  = errors.New("search session expired; search again before acquiring")
	ErrInvalidRequest        = errors.New("invalid acquisition request")
	ErrIdempotencyConflict   = errors.New("idempotency key is already associated with a different acquisition")
	ErrRemoteTaskNotManaged  = errors.New("remote task was not created by MediaDock and is read-only")
)

type Service struct {
	store         store.Store
	downloaders   []Downloader
	incomingDir   string
	now           func() time.Time
	idempotencyMu sync.Mutex
	workerMu      sync.RWMutex
	workerEnabled bool
	workerDone    chan struct{}
}

func NewService(persistence store.Store, downloaders []Downloader, incomingDir string) *Service {
	return &Service{
		store:       persistence,
		downloaders: downloaders,
		incomingDir: incomingDir,
		now:         time.Now,
	}
}

type WorkerOptions struct {
	PollInterval   time.Duration
	Concurrency    int
	RequestTimeout time.Duration
}

func (o WorkerOptions) normalized() WorkerOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	return o
}

// StartWorker enables asynchronous submission and status polling. Start keeps
// its synchronous behavior when no worker is started, which is useful for
// embedding and tests; the main process enables the worker for durable jobs.
func (s *Service) StartWorker(ctx context.Context, options WorkerOptions) {
	options = options.normalized()
	s.workerMu.Lock()
	if s.workerEnabled {
		s.workerMu.Unlock()
		return
	}
	s.workerEnabled = true
	s.workerDone = make(chan struct{})
	s.workerMu.Unlock()
	go s.runWorker(ctx, options)
}

func (s *Service) workerIsEnabled() bool {
	s.workerMu.RLock()
	defer s.workerMu.RUnlock()
	return s.workerEnabled
}

func (s *Service) WaitWorker(ctx context.Context) error {
	s.workerMu.RLock()
	done := s.workerDone
	enabled := s.workerEnabled
	s.workerMu.RUnlock()
	if !enabled || done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runWorker(ctx context.Context, options WorkerOptions) {
	defer func() {
		s.workerMu.Lock()
		s.workerEnabled = false
		close(s.workerDone)
		s.workerMu.Unlock()
	}()
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	s.processJobs(ctx, options)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processJobs(ctx, options)
		}
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

func (s *Service) CandidateAvailability(candidate domain.Candidate) (bool, string) {
	if len(s.downloaders) == 0 {
		return false, "missing_acquirer"
	}
	for _, downloader := range s.downloaders {
		if downloader.Supports(candidate) {
			return true, ""
		}
	}
	return false, "unsupported_kind"
}

func (s *Service) Capabilities() (domain.AcquisitionCapabilities, []domain.ComponentCapability) {
	knownKinds := []string{
		domain.CandidateKindMagnet,
		domain.CandidateKindTorrent,
		domain.CandidateKindHTTPFile,
		domain.CandidateKindCloudShare,
		domain.CandidateKindUnknown,
	}
	supported := make([]string, 0, len(knownKinds))
	for _, kind := range knownKinds {
		candidate := capabilityCandidate(kind)
		for _, downloader := range s.downloaders {
			if downloader.Supports(candidate) {
				supported = append(supported, kind)
				break
			}
		}
	}
	unavailable := make([]domain.UnavailableKind, 0)
	for _, kind := range knownKinds {
		found := slices.Contains(supported, kind)
		if !found {
			reason := "unsupported_kind"
			if len(s.downloaders) == 0 {
				reason = "missing_acquirer"
			}
			unavailable = append(unavailable, domain.UnavailableKind{Kind: kind, Reason: reason})
		}
	}
	components := make([]domain.ComponentCapability, 0, len(s.downloaders))
	for _, downloader := range s.downloaders {
		component := domain.ComponentCapability{
			ID:     downloader.Name(),
			Type:   downloader.Name(),
			State:  "unknown",
			Reason: "connection_not_checked",
		}
		if typed, ok := downloader.(interface{ Type() string }); ok && strings.TrimSpace(typed.Type()) != "" {
			component.Type = typed.Type()
		}
		components = append(components, component)
	}
	capabilities := domain.AcquisitionCapabilities{
		SupportedKinds:   supported,
		UnavailableKinds: unavailable,
	}
	if len(s.downloaders) > 0 {
		capabilities.DefaultDownloaderID = s.downloaders[0].Name()
	}
	return capabilities, components
}

func capabilityCandidate(kind string) domain.Candidate {
	values := map[string]string{
		domain.CandidateKindMagnet:     "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		domain.CandidateKindTorrent:    "https://example.invalid/capability.torrent",
		domain.CandidateKindHTTPFile:   "https://example.invalid/capability.mp4",
		domain.CandidateKindCloudShare: "https://example.invalid/share",
		domain.CandidateKindUnknown:    "https://example.invalid/resource",
	}
	return domain.Candidate{Kind: kind, RawURL: values[kind]}
}

func (s *Service) ListJobs(query store.JobQuery) ([]domain.AcquisitionJob, error) {
	if query.Limit <= 0 {
		query.Limit = 20
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return s.store.ListJobs(query)
}

func ParseJobStatuses(values []string) ([]domain.JobStatus, error) {
	if len(values) == 0 {
		return nil, nil
	}
	valid := map[domain.JobStatus]struct{}{
		domain.JobQueued: {}, domain.JobAcquiring: {}, domain.JobDownloading: {},
		domain.JobDownloaded: {}, domain.JobFailed: {}, domain.JobCancelled: {},
		domain.JobUnsupported: {},
	}
	seen := make(map[domain.JobStatus]struct{})
	statuses := make([]domain.JobStatus, 0)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			status := domain.JobStatus(strings.TrimSpace(part))
			if status == "" {
				continue
			}
			if _, ok := valid[status]; !ok {
				return nil, fmt.Errorf("unknown job status %q", status)
			}
			if _, ok := seen[status]; ok {
				continue
			}
			seen[status] = struct{}{}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func (s *Service) processJobs(ctx context.Context, options WorkerOptions) {
	jobs, err := s.ListJobs(store.JobQuery{
		Limit:    100,
		Statuses: []domain.JobStatus{domain.JobQueued, domain.JobAcquiring, domain.JobDownloading},
	})
	if err != nil {
		return
	}
	semaphore := make(chan struct{}, options.Concurrency)
	var waitGroup sync.WaitGroup
	for _, job := range jobs {
		job := job
		select {
		case <-ctx.Done():
			return
		default:
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			requestCtx, cancel := context.WithTimeout(ctx, options.RequestTimeout)
			defer cancel()
			switch job.Status {
			case domain.JobQueued:
				_ = s.submitQueued(requestCtx, job)
			case domain.JobAcquiring:
				// The downloader may have accepted the request immediately before
				// a crash. Without a remote reconciliation API, never submit it a
				// second time blindly.
				_ = s.markSubmissionUncertain(job.ID)
			case domain.JobDownloading:
				_, _ = s.Get(requestCtx, job.ID)
			}
		}()
	}
	waitGroup.Wait()
}

func (s *Service) submitQueued(ctx context.Context, job domain.AcquisitionJob) error {
	candidate, err := s.store.GetCandidate(job.CandidateID)
	if err != nil {
		return s.markJobFailed(job.ID, "candidate snapshot is unavailable")
	}
	var selected Downloader
	for _, downloader := range s.downloaders {
		if downloader.Name() == job.Downloader && downloader.Supports(candidate) {
			selected = downloader
			break
		}
	}
	if selected == nil {
		return s.markJobFailed(job.ID, "configured downloader is unavailable or no longer supports this candidate")
	}
	_, err = s.submit(ctx, job, candidate, selected)
	return err
}

func (s *Service) submit(ctx context.Context, job domain.AcquisitionJob, candidate domain.Candidate, selected Downloader) (domain.AcquisitionJob, error) {
	job, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.Status = domain.JobAcquiring
		current.StatusStale = false
		current.Message = "submitting to downloader"
		current.UpdatedAt = s.now()
	})
	if err != nil {
		return job, fmt.Errorf("mark acquisition acquiring: %w", err)
	}
	handle, startErr := selected.Start(ctx, job.ID, candidate, job.TargetDir)
	if startErr != nil {
		failed, saveErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.Status = domain.JobFailed
			current.Error = startErr.Error()
			current.Message = "downloader rejected the candidate"
			current.UpdatedAt = s.now()
		})
		if saveErr != nil {
			return failed, fmt.Errorf("persist failed acquisition job: %w", saveErr)
		}
		return failed, startErr
	}
	updated, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.RemoteID = handle.RemoteID
		current.Status = domain.JobDownloading
		current.Progress = 0
		current.StatusStale = false
		if handle.Ownership == HandleOwnershipExternal {
			current.Ownership = domain.JobOwnershipExternal
			current.Message = "linked to an existing downloader task; MediaDock will not modify it"
		} else {
			current.Ownership = domain.JobOwnershipManaged
			current.Message = "download started"
		}
		current.UpdatedAt = s.now()
	})
	if updateErr != nil {
		return updated, fmt.Errorf("persist started acquisition job: %w", updateErr)
	}
	return updated, nil
}

func (s *Service) markJobFailed(jobID, message string) error {
	_, err := s.store.UpdateJob(jobID, func(current *domain.AcquisitionJob) {
		current.Status = domain.JobFailed
		current.Message = message
		current.UpdatedAt = s.now()
	})
	return err
}

func (s *Service) markSubmissionUncertain(jobID string) error {
	checkedAt := s.now()
	_, err := s.store.UpdateJob(jobID, func(current *domain.AcquisitionJob) {
		current.StatusStale = true
		current.LastCheckedAt = &checkedAt
		current.Message = "submission outcome is uncertain; reconciliation required before retry"
		current.UpdatedAt = checkedAt
	})
	return err
}

func (s *Service) Start(ctx context.Context, candidateID string, confirmed bool) (domain.AcquisitionJob, error) {
	return s.StartWithIdempotency(ctx, candidateID, confirmed, "")
}

func (s *Service) StartWithIdempotency(ctx context.Context, candidateID string, confirmed bool, idempotencyKey string) (domain.AcquisitionJob, error) {
	if !confirmed {
		return domain.AcquisitionJob{}, ErrConfirmationRequired
	}
	if strings.TrimSpace(candidateID) == "" {
		return domain.AcquisitionJob{}, fmt.Errorf("%w: candidate_id must not be empty", ErrInvalidRequest)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		existing, findErr := s.store.FindJobByIdempotencyKey(idempotencyKey)
		if findErr == nil {
			if existing.CandidateID != candidateID {
				return existing, ErrIdempotencyConflict
			}
			return existing, nil
		}
		if !errors.Is(findErr, store.ErrNotFound) {
			return domain.AcquisitionJob{}, fmt.Errorf("look up idempotent acquisition: %w", findErr)
		}
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
	jobID := id.New("job")
	targetDir := ""
	if strings.TrimSpace(s.incomingDir) != "" {
		targetDir = filepath.Join(s.incomingDir, jobID)
	}
	var selected Downloader
	for _, downloader := range s.downloaders {
		if downloader.Supports(candidate) {
			selected = downloader
			break
		}
	}
	job := domain.AcquisitionJob{
		ID:             jobID,
		SearchID:       candidate.SearchID,
		CandidateID:    candidate.ID,
		Provider:       candidate.Provider,
		Status:         domain.JobQueued,
		Ownership:      domain.JobOwnershipUnknown,
		IdempotencyKey: idempotencyKey,
		TargetDir:      targetDir,
		CreatedAt:      s.now(),
		UpdatedAt:      s.now(),
	}
	if selected == nil {
		job.Status = domain.JobUnsupported
		job.Error = ErrDownloaderUnavailable.Error()
		job.Message = fmt.Sprintf("candidate kind %q is not supported by a configured downloader", candidate.Kind)
		if saveErr := s.store.SaveJob(job); saveErr != nil {
			return job, fmt.Errorf("persist unsupported acquisition job: %w", saveErr)
		}
		return job, ErrDownloaderUnavailable
	}
	if targetDir != "" {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return job, fmt.Errorf("create local download directory: %w", err)
		}
	}
	job.Downloader = selected.Name()
	if err := s.store.SaveJob(job); err != nil {
		return job, fmt.Errorf("persist acquisition job: %w", err)
	}
	if s.workerIsEnabled() {
		return job, nil
	}
	return s.submit(ctx, job, candidate, selected)
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
			checkedAt := s.now()
			updated, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
				current.StatusStale = true
				current.LastCheckedAt = &checkedAt
				current.Message = "downloader status unavailable; status is stale"
				current.UpdatedAt = checkedAt
			})
			if updateErr != nil {
				return job, updateErr
			}
			return updated, statusErr
		}
		checkedAt := s.now()
		_, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.StatusStale = false
			current.LastCheckedAt = &checkedAt
			current.Progress = status.Progress
			current.Message = status.Message
			current.Error = status.Error
			current.UpdatedAt = checkedAt
			switch status.Status {
			case "completed", "seeding":
				current.Status = domain.JobDownloaded
			case "failed", "error":
				current.Status = domain.JobFailed
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
	if job.Ownership == domain.JobOwnershipExternal {
		return job, ErrRemoteTaskNotManaged
	}
	if job.RemoteID != "" {
		found := false
		for _, downloader := range s.downloaders {
			if downloader.Name() != job.Downloader {
				continue
			}
			found = true
			if err := downloader.Cancel(ctx, job.RemoteID); err != nil {
				return job, err
			}
			break
		}
		if !found {
			return job, fmt.Errorf("downloader %q is not configured", job.Downloader)
		}
	}
	updated, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.Status = domain.JobCancelled
		current.Message = "cancelled by user"
		current.UpdatedAt = s.now()
	})
	return updated, err
}
