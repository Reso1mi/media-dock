package acquisition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	ErrJobNotCancellable     = errors.New("this acquisition can no longer be cancelled")
	ErrTargetProfileRequired = errors.New("target_profile is required when multiple acquisition profiles match")
	ErrUnknownTargetProfile  = errors.New("target_profile is not configured")
	ErrGoalUnsupported       = errors.New("acquisition goal is not supported for this candidate")
	ErrReconcileUnavailable  = errors.New("this acquisition cannot be reconciled automatically")
)

type AcquisitionRequest struct {
	CandidateID    string
	Goal           domain.AcquisitionGoal
	TargetProfile  string
	TargetDir      string
	Confirmed      bool
	IdempotencyKey string
}

type CopyAcquisitionRequest struct {
	TargetProfile  string
	SourcePath     string
	TargetDir      string
	Options        domain.CopyOptions
	Confirmed      bool
	IdempotencyKey string
}

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
		if downloader.Supports(candidate) && len(supportedGoals(downloader, candidate)) > 0 {
			return true, ""
		}
	}
	for _, downloader := range s.downloaders {
		if downloader.Supports(candidate) {
			return false, "unsupported_goal"
		}
	}
	return false, "unsupported_kind"
}

func (s *Service) CandidateGoals(candidate domain.Candidate) []domain.AcquisitionGoal {
	goals := make([]domain.AcquisitionGoal, 0, 2)
	for _, goal := range []domain.AcquisitionGoal{domain.GoalSaveToCloud, domain.GoalDownloadToLocal} {
		for _, downloader := range s.downloaders {
			if downloader.Supports(candidate) && supportsGoal(downloader, candidate, goal) {
				goals = append(goals, goal)
				break
			}
		}
	}
	return goals
}

func supportedGoals(downloader Downloader, candidate domain.Candidate) []domain.AcquisitionGoal {
	goals := make([]domain.AcquisitionGoal, 0, 2)
	for _, goal := range []domain.AcquisitionGoal{domain.GoalSaveToCloud, domain.GoalDownloadToLocal} {
		if downloader.Supports(candidate) && supportsGoal(downloader, candidate, goal) {
			goals = append(goals, goal)
		}
	}
	return goals
}

func (s *Service) TargetProfiles() []TargetProfile {
	profiles := make([]TargetProfile, 0, len(s.downloaders))
	for _, downloader := range s.downloaders {
		profile := TargetProfile{
			ID:                  downloader.Name(),
			Type:                downloader.Name(),
			DisplayName:         downloader.Name(),
			SupportedGoals:      []domain.AcquisitionGoal{domain.GoalDownloadToLocal},
			SupportedOperations: []domain.OperationKind{domain.OperationDownload},
		}
		if typed, ok := downloader.(TargetProfileProvider); ok {
			provided := typed.TargetProfile()
			if strings.TrimSpace(provided.ID) != "" {
				profile.ID = strings.TrimSpace(provided.ID)
			}
			if strings.TrimSpace(provided.Type) != "" {
				profile.Type = strings.TrimSpace(provided.Type)
			}
			if strings.TrimSpace(provided.DisplayName) != "" {
				profile.DisplayName = strings.TrimSpace(provided.DisplayName)
			}
			profile.SupportedKinds = append([]string(nil), provided.SupportedKinds...)
			profile.SupportedGoals = append([]domain.AcquisitionGoal(nil), provided.SupportedGoals...)
			profile.SupportedOperations = append([]domain.OperationKind(nil), provided.SupportedOperations...)
		}
		if len(profile.SupportedKinds) == 0 {
			for _, kind := range []string{domain.CandidateKindMagnet, domain.CandidateKindTorrent, domain.CandidateKindHTTPFile, domain.CandidateKindCloudShare, domain.CandidateKindUnknown} {
				if downloader.Supports(capabilityCandidate(kind)) {
					profile.SupportedKinds = append(profile.SupportedKinds, kind)
				}
			}
		}
		if len(profile.SupportedGoals) == 0 {
			profile.SupportedGoals = []domain.AcquisitionGoal{domain.GoalDownloadToLocal}
		}
		if len(profile.SupportedOperations) == 0 {
			profile.SupportedOperations = []domain.OperationKind{domain.OperationDownload}
		}
		if _, ok := downloader.(CopyDownloader); ok && !slices.Contains(profile.SupportedOperations, domain.OperationOpenListCopy) {
			profile.SupportedOperations = append(profile.SupportedOperations, domain.OperationOpenListCopy)
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func (s *Service) profileSummaries() []domain.TargetProfileSummary {
	profiles := s.TargetProfiles()
	result := make([]domain.TargetProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		goals := make([]string, 0, len(profile.SupportedGoals))
		for _, goal := range profile.SupportedGoals {
			goals = append(goals, string(goal))
		}
		operations := make([]string, 0, len(profile.SupportedOperations))
		for _, operation := range profile.SupportedOperations {
			operations = append(operations, string(operation))
		}
		result = append(result, domain.TargetProfileSummary{
			ID:                  profile.ID,
			Type:                profile.Type,
			DisplayName:         profile.DisplayName,
			SupportedKinds:      append([]string(nil), profile.SupportedKinds...),
			SupportedGoals:      goals,
			SupportedOperations: operations,
		})
	}
	return result
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
			if downloader.Supports(candidate) && len(supportedGoals(downloader, candidate)) > 0 {
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
		TargetProfiles:   s.profileSummaries(),
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
		domain.CandidateKindCloudShare: "https://pan.quark.cn/s/capability",
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
		domain.JobDownloaded: {}, domain.JobTransferred: {}, domain.JobTransferUncertain: {},
		domain.JobSubmissionUncertain: {}, domain.JobFailed: {}, domain.JobCancelled: {},
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
			requestCtx, cancel := context.WithTimeout(ctx, s.requestTimeout(job.Downloader, options.RequestTimeout))
			defer cancel()
			switch job.Status {
			case domain.JobQueued:
				if job.Operation == domain.OperationOpenListCopy {
					_ = s.submitCopyQueued(requestCtx, job)
				} else {
					_ = s.submitQueued(requestCtx, job)
				}
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

func (s *Service) requestTimeout(downloaderName string, fallback time.Duration) time.Duration {
	for _, downloader := range s.downloaders {
		if downloader.Name() != downloaderName {
			continue
		}
		if configured, ok := downloader.(interface{ RequestTimeout() time.Duration }); ok {
			if timeout := configured.RequestTimeout(); timeout > 0 {
				return timeout
			}
		}
		break
	}
	return fallback
}

func (s *Service) submitQueued(ctx context.Context, job domain.AcquisitionJob) error {
	candidate, err := s.store.GetCandidate(job.CandidateID)
	if err != nil {
		return s.markJobFailed(job.ID, "candidate snapshot is unavailable")
	}
	var selected Downloader
	for _, downloader := range s.downloaders {
		if downloaderMatchesJob(downloader, job) && downloader.Supports(candidate) {
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
	goal := job.Goal
	if goal == "" {
		goal = defaultGoal(candidate)
	}
	started := false
	job, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		if current.Status != domain.JobQueued {
			return
		}
		current.Status = domain.JobAcquiring
		current.Phase = domain.PhaseSubmitting
		if goal == domain.GoalSaveToCloud {
			current.Phase = domain.PhaseTransferring
		}
		current.StatusStale = false
		current.Message = "submitting to downloader"
		current.UpdatedAt = s.now()
		started = true
	})
	if err != nil {
		return job, fmt.Errorf("mark acquisition acquiring: %w", err)
	}
	if !started {
		return job, ErrJobNotCancellable
	}

	handle, startErr := s.startDownloader(ctx, selected, StartRequest{
		JobID:         job.ID,
		Candidate:     candidate,
		Goal:          goal,
		TargetProfile: job.TargetProfile,
		TargetDir:     job.TargetDir,
	})
	if startErr != nil {
		failed, saveErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.Status = domain.JobFailed
			current.Phase = domain.PhaseFailed
			current.StatusStale = false
			current.Error = startErr.Error()
			current.Message = "downloader rejected the candidate"
			if errors.Is(startErr, ErrAcquisitionOutcomeUncertain) {
				current.Status = domain.JobTransferUncertain
				current.Phase = domain.PhaseUncertain
				current.StatusStale = true
				current.UncertaintyReason = startErr.Error()
				current.RecoveryAction = recoveryAction(current)
				checkedAt := s.now()
				current.LastCheckedAt = &checkedAt
				current.Message = "OpenList transfer outcome is uncertain; inspect the destination before retrying"
			}
			current.UpdatedAt = s.now()
		})
		if saveErr != nil {
			return failed, fmt.Errorf("persist failed acquisition job: %w", saveErr)
		}
		return failed, startErr
	}

	updated, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.RemoteID = handle.RemoteID
		current.TargetReference = handle.TargetReference
		current.ResultKind = handle.ResultKind
		if current.ResultKind == "" {
			if goal == domain.GoalSaveToCloud {
				current.ResultKind = "cloud_transfer"
			} else {
				current.ResultKind = "downloader"
			}
		}
		current.Progress = 0
		current.StatusStale = false
		current.Error = ""
		current.UncertaintyReason = ""
		current.RecoveryAction = ""
		if handle.Completed {
			current.Ownership = domain.JobOwnershipManaged
			current.Progress = 1
			current.LastCheckedAt = timePtr(s.now())
			if goal == domain.GoalSaveToCloud {
				current.Status = domain.JobTransferred
				current.Phase = domain.PhaseSaved
				current.Message = "OpenList reported that the share transfer completed"
			} else {
				current.Status = domain.JobDownloaded
				current.Phase = domain.PhaseDownloaded
				current.Message = "downloader reported that the media completed"
			}
		} else if goal == domain.GoalSaveToCloud {
			// Keep the public status compatible with existing clients while the
			// explicit phase records that this is still a cloud transfer.
			current.Status = domain.JobDownloading
			current.Phase = domain.PhaseTransferring
			current.Ownership = domain.JobOwnershipManaged
			current.Message = "OpenList transfer is in progress"
		} else if handle.Ownership == HandleOwnershipExternal {
			current.Status = domain.JobDownloading
			current.Phase = domain.PhaseDownloading
			current.Ownership = domain.JobOwnershipExternal
			current.Message = "linked to an existing downloader task; MediaDock will not modify it"
		} else {
			current.Status = domain.JobDownloading
			current.Phase = domain.PhaseDownloading
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

func (s *Service) submitCopyQueued(ctx context.Context, job domain.AcquisitionJob) error {
	var selected Downloader
	for _, downloader := range s.downloaders {
		if downloaderMatchesJob(downloader, job) {
			selected = downloader
			break
		}
	}
	if selected == nil {
		return s.markJobFailed(job.ID, "configured OpenList copy adapter is unavailable")
	}
	return s.submitCopy(ctx, job, selected)
}

func (s *Service) submitCopy(ctx context.Context, job domain.AcquisitionJob, selected Downloader) error {
	copyDownloader, ok := selected.(CopyDownloader)
	if !ok {
		return s.markJobFailed(job.ID, "configured downloader does not support OpenList COPY")
	}
	started := false
	job, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		if current.Status != domain.JobQueued {
			return
		}
		current.Status = domain.JobAcquiring
		current.Phase = domain.PhaseCopying
		current.StatusStale = false
		current.Message = "submitting OpenList COPY"
		current.UpdatedAt = s.now()
		started = true
	})
	if err != nil {
		return fmt.Errorf("mark OpenList copy acquiring: %w", err)
	}
	if !started {
		return ErrJobNotCancellable
	}

	handle, startErr := copyDownloader.StartCopy(ctx, CopyRequest{
		JobID:         job.ID,
		TargetProfile: job.TargetProfile,
		SourcePath:    job.SourcePath,
		TargetDir:     job.TargetDir,
		Options:       job.CopyOptions,
	})
	if startErr != nil {
		_, saveErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.Status = domain.JobFailed
			current.Phase = domain.PhaseFailed
			current.StatusStale = false
			current.Error = startErr.Error()
			current.Message = "OpenList COPY was rejected"
			if errors.Is(startErr, ErrAcquisitionOutcomeUncertain) {
				current.Status = domain.JobSubmissionUncertain
				current.Phase = domain.PhaseUncertain
				current.StatusStale = true
				current.UncertaintyReason = startErr.Error()
				current.RecoveryAction = recoveryAction(current)
				current.Message = "OpenList COPY outcome is uncertain; inspect the target before retrying"
				checkedAt := s.now()
				current.LastCheckedAt = &checkedAt
			}
			current.UpdatedAt = s.now()
		})
		if saveErr != nil {
			return fmt.Errorf("persist failed OpenList COPY job: %w", saveErr)
		}
		return startErr
	}

	_, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.RemoteID = handle.RemoteID
		current.ResultKind = handle.ResultKind
		if current.ResultKind == "" {
			current.ResultKind = "openlist_copy"
		}
		current.TargetReference = handle.TargetReference
		current.Error = ""
		current.UncertaintyReason = ""
		current.RecoveryAction = ""
		current.StatusStale = false
		if handle.Completed {
			current.Status = domain.JobDownloaded
			current.Phase = domain.PhaseDownloaded
			current.Progress = 1
			current.Ownership = domain.JobOwnershipManaged
			current.LastCheckedAt = timePtr(s.now())
			current.Message = "OpenList COPY completed"
		} else {
			current.Status = domain.JobDownloading
			current.Phase = domain.PhaseCopying
			current.Progress = 0
			current.Ownership = domain.JobOwnershipManaged
			current.Message = "OpenList COPY is in progress"
		}
		current.UpdatedAt = s.now()
	})
	if updateErr != nil {
		return fmt.Errorf("persist started OpenList COPY job: %w", updateErr)
	}
	return nil
}

func downloaderMatchesJob(downloader Downloader, job domain.AcquisitionJob) bool {
	if downloader == nil || downloader.Name() != job.Downloader {
		return false
	}
	requestedProfile := strings.TrimSpace(job.TargetProfile)
	if requestedProfile == "" {
		return true
	}
	if provider, ok := downloader.(TargetProfileProvider); ok {
		profile := provider.TargetProfile()
		if strings.TrimSpace(profile.ID) != "" {
			return strings.TrimSpace(profile.ID) == requestedProfile
		}
	}
	return strings.TrimSpace(downloader.Name()) == requestedProfile
}

func (s *Service) startDownloader(ctx context.Context, downloader Downloader, request StartRequest) (Handle, error) {
	if goalAware, ok := downloader.(GoalAwareDownloader); ok {
		if !goalAware.SupportsGoal(request.Candidate, request.Goal) {
			return Handle{}, fmt.Errorf("%w: %s does not support %q", ErrGoalUnsupported, downloader.Name(), request.Goal)
		}
		return goalAware.StartRequest(ctx, request)
	}
	if request.Goal != domain.GoalDownloadToLocal {
		return Handle{}, fmt.Errorf("%w: adapter %q only supports %q", ErrGoalUnsupported, downloader.Name(), domain.GoalDownloadToLocal)
	}
	return downloader.Start(ctx, request.JobID, request.Candidate, request.TargetDir)
}

func defaultGoal(candidate domain.Candidate) domain.AcquisitionGoal {
	if candidate.Kind == domain.CandidateKindCloudShare {
		return domain.GoalSaveToCloud
	}
	return domain.GoalDownloadToLocal
}

func timePtr(value time.Time) *time.Time { return &value }

func recoveryAction(job *domain.AcquisitionJob) string {
	if job.Operation == domain.OperationOpenListCopy {
		return "inspect_openlist_copy_target_before_retry"
	}
	if job.Goal == domain.GoalSaveToCloud || job.Downloader == "openlist" {
		return "inspect_openlist_destination_before_retry"
	}
	return "reconcile_downloader_before_retry"
}

func (s *Service) markJobFailed(jobID, message string) error {
	_, err := s.store.UpdateJob(jobID, func(current *domain.AcquisitionJob) {
		if current.Status != domain.JobQueued && current.Status != domain.JobAcquiring {
			return
		}
		current.Status = domain.JobFailed
		current.Phase = domain.PhaseFailed
		current.Message = message
		current.UpdatedAt = s.now()
	})
	return err
}

func (s *Service) markSubmissionUncertain(jobID string) error {
	checkedAt := s.now()
	_, err := s.store.UpdateJob(jobID, func(current *domain.AcquisitionJob) {
		current.StatusStale = true
		current.Phase = domain.PhaseUncertain
		current.LastCheckedAt = &checkedAt
		current.UncertaintyReason = "MediaDock restarted while the external submission was in progress"
		if current.Operation == domain.OperationOpenListCopy {
			current.Status = domain.JobSubmissionUncertain
			current.Message = "OpenList COPY outcome is uncertain; inspect the target before retrying"
		} else if current.Downloader == "openlist" || current.Goal == domain.GoalSaveToCloud {
			current.Status = domain.JobTransferUncertain
			current.Message = "OpenList transfer outcome is uncertain; inspect the destination before retrying"
		} else {
			current.Status = domain.JobSubmissionUncertain
			current.Message = "submission outcome is uncertain; reconcile the downloader before retry"
		}
		current.RecoveryAction = recoveryAction(current)
		current.UpdatedAt = checkedAt
	})
	return err
}

func (s *Service) Start(ctx context.Context, candidateID string, confirmed bool) (domain.AcquisitionJob, error) {
	return s.StartRequest(ctx, AcquisitionRequest{CandidateID: candidateID, Confirmed: confirmed})
}

func (s *Service) StartWithIdempotency(ctx context.Context, candidateID string, confirmed bool, idempotencyKey string) (domain.AcquisitionJob, error) {
	return s.StartRequest(ctx, AcquisitionRequest{
		CandidateID:    candidateID,
		Confirmed:      confirmed,
		IdempotencyKey: idempotencyKey,
	})
}

func (s *Service) StartRequest(ctx context.Context, request AcquisitionRequest) (domain.AcquisitionJob, error) {
	if !request.Confirmed {
		return domain.AcquisitionJob{}, ErrConfirmationRequired
	}
	candidateID := strings.TrimSpace(request.CandidateID)
	if candidateID == "" {
		return domain.AcquisitionJob{}, fmt.Errorf("%w: candidate_id must not be empty", ErrInvalidRequest)
	}
	request.Goal = domain.AcquisitionGoal(strings.TrimSpace(string(request.Goal)))
	request.TargetProfile = strings.TrimSpace(request.TargetProfile)
	request.TargetDir = strings.TrimSpace(request.TargetDir)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	locked := false
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		locked = true
		defer func() {
			if locked {
				s.idempotencyMu.Unlock()
			}
		}()
		existing, findErr := s.store.FindJobByIdempotencyKey(request.IdempotencyKey)
		if findErr == nil {
			if !sameAcquisitionRequest(existing, candidateID, request) {
				return existing, ErrIdempotencyConflict
			}
			return existing, replayJobError(existing)
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

	goal := request.Goal
	if goal == "" {
		goal = defaultGoal(candidate)
	}
	if goal != domain.GoalSaveToCloud && goal != domain.GoalDownloadToLocal {
		return domain.AcquisitionJob{}, fmt.Errorf("%w: unsupported goal %q", ErrInvalidRequest, goal)
	}
	targetProfile := request.TargetProfile
	selected, profile, selectErr := s.selectDownloader(candidate, goal, targetProfile)
	if selectErr != nil && !errors.Is(selectErr, ErrDownloaderUnavailable) && !errors.Is(selectErr, ErrGoalUnsupported) {
		return domain.AcquisitionJob{}, selectErr
	}
	if profile.ID != "" {
		targetProfile = profile.ID
	}
	targetDir := request.TargetDir
	operation := operationFor(candidate, goal, selected)
	var initialTargetReference *domain.TargetReference
	if selected != nil && operation == domain.OperationShareTransfer {
		if targetProvider, ok := selected.(TargetReferenceProvider); ok {
			initialTargetReference = targetProvider.TargetReference(targetDir)
			if initialTargetReference != nil {
				targetDir = initialTargetReference.Path
				setTargetReferenceOperation(initialTargetReference, operation)
			}
		}
	}
	digest := acquisitionRequestDigest(candidate, goal, targetProfile, targetDir)

	now := s.now()
	job := domain.AcquisitionJob{
		ID:             id.New("job"),
		SearchID:       candidate.SearchID,
		CandidateID:    candidate.ID,
		Provider:       candidate.Provider,
		Downloader:     "",
		Operation:      operation,
		Goal:           goal,
		TargetProfile:  targetProfile,
		Phase:          domain.PhaseQueued,
		Status:         domain.JobQueued,
		Ownership:      domain.JobOwnershipUnknown,
		IdempotencyKey: request.IdempotencyKey,
		RequestDigest:  digest,
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if selected == nil {
		job.Status = domain.JobUnsupported
		job.Phase = domain.PhaseUnsupported
		if errors.Is(selectErr, ErrGoalUnsupported) {
			job.Error = ErrGoalUnsupported.Error()
			job.Message = fmt.Sprintf("goal %q is not supported for candidate kind %q", goal, candidate.Kind)
		} else {
			job.Error = ErrDownloaderUnavailable.Error()
			job.Message = fmt.Sprintf("candidate kind %q is not supported by a configured profile", candidate.Kind)
		}
		if saveErr := s.store.SaveJob(job); saveErr != nil {
			return job, fmt.Errorf("persist unsupported acquisition job: %w", saveErr)
		}
		if locked {
			s.idempotencyMu.Unlock()
			locked = false
		}
		if errors.Is(selectErr, ErrGoalUnsupported) {
			return job, ErrGoalUnsupported
		}
		return job, ErrDownloaderUnavailable
	}

	if goal == domain.GoalSaveToCloud {
		job.TargetDir = targetDir
	} else if goal == domain.GoalDownloadToLocal && needsLocalTargetDir(selected) && strings.TrimSpace(s.incomingDir) != "" {
		job.TargetDir = filepath.Join(s.incomingDir, job.ID)
		if err := os.MkdirAll(job.TargetDir, 0o755); err != nil {
			return job, fmt.Errorf("create local download directory: %w", err)
		}
	}
	job.Downloader = selected.Name()
	if initialTargetReference != nil {
		job.TargetReference = initialTargetReference
	} else if targetProvider, ok := selected.(TargetReferenceProvider); ok && strings.TrimSpace(job.TargetDir) != "" {
		job.TargetReference = targetProvider.TargetReference(job.TargetDir)
		setTargetReferenceOperation(job.TargetReference, job.Operation)
	}
	if err := s.store.SaveJob(job); err != nil {
		// A second MediaDock process may have won the unique idempotency key
		// between lookup and insert. Return that durable job instead of hiding it
		// behind a raw SQLite constraint error.
		if request.IdempotencyKey != "" {
			if existing, findErr := s.store.FindJobByIdempotencyKey(request.IdempotencyKey); findErr == nil {
				if sameAcquisitionIntent(existing, candidate, goal, targetProfile, targetDir, digest) {
					return existing, replayJobError(existing)
				}
				return existing, ErrIdempotencyConflict
			}
		}
		return job, fmt.Errorf("persist acquisition job: %w", err)
	}
	if locked {
		s.idempotencyMu.Unlock()
		locked = false
	}
	if s.workerIsEnabled() {
		return job, nil
	}
	return s.submit(ctx, job, candidate, selected)
}

func (s *Service) StartCopy(ctx context.Context, request CopyAcquisitionRequest) (domain.AcquisitionJob, error) {
	if !request.Confirmed {
		return domain.AcquisitionJob{}, ErrConfirmationRequired
	}
	sourcePath := strings.TrimSpace(request.SourcePath)
	targetDir := strings.TrimSpace(request.TargetDir)
	if sourcePath == "" || targetDir == "" {
		return domain.AcquisitionJob{}, fmt.Errorf("%w: source_path and target_dir are required", ErrInvalidRequest)
	}
	if request.Options.Overwrite && request.Options.SkipExisting {
		return domain.AcquisitionJob{}, fmt.Errorf("%w: overwrite and skip_existing cannot both be true", ErrInvalidRequest)
	}
	request.TargetProfile = strings.TrimSpace(request.TargetProfile)
	if !request.Options.Overwrite && !request.Options.SkipExisting {
		request.Options.SkipExisting = true
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)

	locked := false
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		locked = true
		defer func() {
			if locked {
				s.idempotencyMu.Unlock()
			}
		}()
		existing, findErr := s.store.FindJobByIdempotencyKey(request.IdempotencyKey)
		if findErr == nil {
			if !sameCopyRequest(existing, request, sourcePath, targetDir) {
				return existing, ErrIdempotencyConflict
			}
			return existing, replayJobError(existing)
		}
		if !errors.Is(findErr, store.ErrNotFound) {
			return domain.AcquisitionJob{}, fmt.Errorf("look up idempotent OpenList COPY: %w", findErr)
		}
	}

	selected, profile, selectErr := s.selectCopyDownloader(request.TargetProfile)
	if selectErr != nil {
		return domain.AcquisitionJob{}, selectErr
	}
	if profile.ID != "" {
		request.TargetProfile = profile.ID
	}
	digest := copyRequestDigest(request.TargetProfile, sourcePath, targetDir, request.Options)

	now := s.now()
	job := domain.AcquisitionJob{
		ID:             id.New("job"),
		Provider:       "openlist",
		Downloader:     selected.Name(),
		Operation:      domain.OperationOpenListCopy,
		Goal:           domain.GoalDownloadToLocal,
		TargetProfile:  request.TargetProfile,
		TargetDir:      targetDir,
		SourcePath:     sourcePath,
		CopyOptions:    request.Options,
		Phase:          domain.PhaseQueued,
		Status:         domain.JobQueued,
		Ownership:      domain.JobOwnershipUnknown,
		IdempotencyKey: request.IdempotencyKey,
		RequestDigest:  digest,
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if targetProvider, ok := selected.(TargetReferenceProvider); ok {
		job.TargetReference = targetProvider.TargetReference(targetDir)
		setTargetReferenceOperation(job.TargetReference, job.Operation)
	}
	if err := s.store.SaveJob(job); err != nil {
		if request.IdempotencyKey != "" {
			if existing, findErr := s.store.FindJobByIdempotencyKey(request.IdempotencyKey); findErr == nil {
				if existing.RequestDigest == digest {
					return existing, replayJobError(existing)
				}
				return existing, ErrIdempotencyConflict
			}
		}
		return job, fmt.Errorf("persist OpenList COPY job: %w", err)
	}
	if locked {
		s.idempotencyMu.Unlock()
		locked = false
	}
	if s.workerIsEnabled() {
		return job, nil
	}
	if err := s.submitCopy(ctx, job, selected); err != nil {
		updated, getErr := s.store.GetJob(job.ID)
		if getErr == nil {
			return updated, err
		}
		return job, err
	}
	return s.store.GetJob(job.ID)
}

func (s *Service) selectCopyDownloader(requestedProfile string) (Downloader, TargetProfile, error) {
	profiles := s.TargetProfiles()
	matches := make([]int, 0, len(s.downloaders))
	known := requestedProfile == ""
	for index, downloader := range s.downloaders {
		if _, ok := downloader.(CopyDownloader); !ok {
			continue
		}
		profile := profiles[index]
		if requestedProfile != "" && profile.ID != requestedProfile && downloader.Name() != requestedProfile {
			continue
		}
		known = true
		matches = append(matches, index)
	}
	if requestedProfile != "" && !known {
		return nil, TargetProfile{}, fmt.Errorf("%w: %s", ErrUnknownTargetProfile, requestedProfile)
	}
	if len(matches) > 1 {
		return nil, TargetProfile{}, ErrTargetProfileRequired
	}
	if len(matches) == 0 {
		return nil, TargetProfile{}, ErrDownloaderUnavailable
	}
	index := matches[0]
	return s.downloaders[index], profiles[index], nil
}

func copyRequestDigest(profile, sourcePath, targetDir string, options domain.CopyOptions) string {
	value := string(domain.OperationOpenListCopy) + "\\x00" + profile + "\\x00" + sourcePath + "\\x00" + targetDir + "\\x00" + fmt.Sprintf("%t\\x00%t\\x00%t", options.Overwrite, options.SkipExisting, options.Merge)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Service) selectDownloader(candidate domain.Candidate, goal domain.AcquisitionGoal, requestedProfile string) (Downloader, TargetProfile, error) {
	profiles := s.TargetProfiles()
	matches := make([]int, 0, len(s.downloaders))
	if requestedProfile != "" {
		known := false
		for index, downloader := range s.downloaders {
			profile := profiles[index]
			if profile.ID != requestedProfile && downloader.Name() != requestedProfile {
				continue
			}
			known = true
			if downloader.Supports(candidate) && supportsGoal(downloader, candidate, goal) {
				matches = append(matches, index)
			}
		}
		if !known {
			return nil, TargetProfile{}, fmt.Errorf("%w: %s", ErrUnknownTargetProfile, requestedProfile)
		}
		if len(matches) == 0 {
			return nil, TargetProfile{}, fmt.Errorf("%w: profile %q cannot handle candidate kind %q for goal %q", ErrGoalUnsupported, requestedProfile, candidate.Kind, goal)
		}
	} else {
		for index, downloader := range s.downloaders {
			if downloader.Supports(candidate) && supportsGoal(downloader, candidate, goal) {
				matches = append(matches, index)
			}
		}
		if len(matches) > 1 {
			return nil, TargetProfile{}, ErrTargetProfileRequired
		}
	}
	if len(matches) == 0 {
		if len(s.downloaders) == 0 {
			return nil, TargetProfile{}, ErrDownloaderUnavailable
		}
		for _, downloader := range s.downloaders {
			if downloader.Supports(candidate) {
				return nil, TargetProfile{}, ErrGoalUnsupported
			}
		}
		return nil, TargetProfile{}, ErrDownloaderUnavailable
	}
	index := matches[0]
	profile := profiles[index]
	return s.downloaders[index], profile, nil
}

func supportsGoal(downloader Downloader, candidate domain.Candidate, goal domain.AcquisitionGoal) bool {
	if goalAware, ok := downloader.(GoalAwareDownloader); ok {
		return goalAware.SupportsGoal(candidate, goal)
	}
	return goal == domain.GoalDownloadToLocal
}

func operationFor(_ domain.Candidate, goal domain.AcquisitionGoal, _ Downloader) domain.OperationKind {
	if goal == domain.GoalSaveToCloud {
		return domain.OperationShareTransfer
	}
	return domain.OperationDownload
}

func setTargetReferenceOperation(reference *domain.TargetReference, operation domain.OperationKind) {
	if reference != nil && operation != "" {
		reference.Operation = operation
	}
}

func acquisitionRequestDigest(candidate domain.Candidate, goal domain.AcquisitionGoal, targetProfile, targetDir string) string {
	value := candidate.ID + "\\x00" + candidate.SearchID + "\\x00" + string(goal) + "\\x00" + targetProfile + "\\x00" + strings.TrimSpace(targetDir)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func sameCopyRequest(job domain.AcquisitionJob, request CopyAcquisitionRequest, sourcePath, targetDir string) bool {
	if job.Operation != "" && job.Operation != domain.OperationOpenListCopy {
		return false
	}
	if strings.TrimSpace(job.SourcePath) != sourcePath || strings.TrimSpace(job.TargetDir) != targetDir || job.CopyOptions != request.Options {
		return false
	}
	if request.TargetProfile != "" && job.TargetProfile != request.TargetProfile && job.Downloader != request.TargetProfile {
		return false
	}
	return true
}

func sameAcquisitionRequest(job domain.AcquisitionJob, candidateID string, request AcquisitionRequest) bool {
	if job.CandidateID != candidateID {
		return false
	}
	if request.Goal != "" && job.Goal != request.Goal {
		return false
	}
	if request.TargetProfile != "" && job.TargetProfile != request.TargetProfile && job.Downloader != request.TargetProfile {
		return false
	}
	if request.TargetDir != "" && strings.TrimSpace(job.TargetDir) != request.TargetDir {
		return false
	}
	return true
}

func sameAcquisitionIntent(job domain.AcquisitionJob, candidate domain.Candidate, goal domain.AcquisitionGoal, targetProfile, targetDir, digest string) bool {
	if job.RequestDigest != "" {
		if job.RequestDigest == digest {
			return true
		}
		// Version 2 digests did not include the interface-provided target dir.
		legacyValue := candidate.ID + "\\x00" + candidate.SearchID + "\\x00" + string(goal) + "\\x00" + targetProfile
		legacySum := sha256.Sum256([]byte(legacyValue))
		return job.RequestDigest == hex.EncodeToString(legacySum[:]) && strings.TrimSpace(job.TargetDir) == strings.TrimSpace(targetDir)
	}
	// Jobs created before request digests were introduced can still be safely
	// replayed only when their complete stored intent matches.
	return job.CandidateID == candidate.ID && job.Goal == goal && job.TargetProfile == targetProfile && strings.TrimSpace(job.TargetDir) == strings.TrimSpace(targetDir)
}

func replayJobError(job domain.AcquisitionJob) error {
	switch job.Status {
	case domain.JobTransferUncertain, domain.JobSubmissionUncertain:
		return ErrAcquisitionOutcomeUncertain
	case domain.JobUnsupported:
		if job.Error == ErrGoalUnsupported.Error() {
			return ErrGoalUnsupported
		}
		return ErrDownloaderUnavailable
	case domain.JobFailed:
		if strings.TrimSpace(job.Error) != "" {
			return errors.New(job.Error)
		}
	}
	return nil
}

func needsLocalTargetDir(downloader Downloader) bool {
	if policy, ok := downloader.(interface{ RequiresLocalTargetDir() bool }); ok {
		return policy.RequiresLocalTargetDir()
	}
	return true
}

func (s *Service) Get(ctx context.Context, jobID string) (domain.AcquisitionJob, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if job.RemoteID == "" || job.Downloader == "" || job.Status == domain.JobDownloaded || job.Status == domain.JobTransferred || job.Status == domain.JobFailed || job.Status == domain.JobCancelled || job.Status == domain.JobUnsupported || job.Status == domain.JobTransferUncertain || job.Status == domain.JobSubmissionUncertain {
		return job, nil
	}
	for _, downloader := range s.downloaders {
		if !downloaderMatchesJob(downloader, job) {
			continue
		}
		status, statusErr := remoteStatus(ctx, downloader, job)
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
			switch current.Status {
			case domain.JobCancelled, domain.JobFailed, domain.JobDownloaded, domain.JobTransferred, domain.JobUnsupported, domain.JobTransferUncertain, domain.JobSubmissionUncertain:
				return
			}
			current.StatusStale = false
			current.LastCheckedAt = &checkedAt
			current.Progress = status.Progress
			current.Message = status.Message
			current.Error = status.Error
			if status.TargetReference != nil {
				reference := *status.TargetReference
				if reference.Path == "" && current.TargetReference != nil {
					reference.Path = current.TargetReference.Path
				}
				current.TargetReference = &reference
			}
			current.UpdatedAt = checkedAt
			applyRemoteStatus(current, status)
		})
		if updateErr != nil {
			return domain.AcquisitionJob{}, updateErr
		}
		return s.store.GetJob(job.ID)
	}
	return job, nil
}

func remoteStatus(ctx context.Context, downloader Downloader, job domain.AcquisitionJob) (RemoteStatus, error) {
	if typed, ok := downloader.(OperationAwareDownloader); ok {
		targetDir := job.TargetDir
		if strings.TrimSpace(targetDir) == "" && job.TargetReference != nil {
			targetDir = job.TargetReference.Path
		}
		return typed.StatusRequest(ctx, RemoteStatusRequest{
			RemoteID:  job.RemoteID,
			Operation: job.Operation,
			TargetDir: targetDir,
		})
	}
	return downloader.Status(ctx, job.RemoteID)
}

func applyRemoteStatus(job *domain.AcquisitionJob, status RemoteStatus) {
	switch {
	case job.Operation == domain.OperationShareTransfer && (status.Status == "transferred" || status.Status == "completed" || status.Status == "success" || status.Status == "succeeded"):
		job.Status = domain.JobTransferred
		job.Phase = domain.PhaseSaved
		job.Progress = 1
	case job.Operation == domain.OperationOpenListCopy && (status.Status == "completed" || status.Status == "copied" || status.Status == "success" || status.Status == "succeeded"):
		job.Status = domain.JobDownloaded
		job.Phase = domain.PhaseDownloaded
		job.Progress = 1
	case status.Status == "completed", status.Status == "seeding":
		job.Status = domain.JobDownloaded
		job.Phase = domain.PhaseDownloaded
	case status.Status == "cancelled", status.Status == "canceled":
		job.Status = domain.JobCancelled
		job.Phase = domain.PhaseCancelled
	case status.Status == "failed", status.Status == "error":
		job.Status = domain.JobFailed
		job.Phase = domain.PhaseFailed
	case status.Status == "transferring":
		job.Status = domain.JobDownloading
		job.Phase = domain.PhaseTransferring
	case status.Status == "copying":
		job.Status = domain.JobDownloading
		job.Phase = domain.PhaseCopying
	case status.Status == "downloading", status.Status == "checking", status.Status == "checking_wait", status.Status == "download_wait", status.Status == "seed_wait":
		job.Status = domain.JobDownloading
		job.Phase = domain.PhaseDownloading
	case status.Status == "stopped":
		if status.Progress >= 1 {
			job.Status = domain.JobDownloaded
			job.Phase = domain.PhaseDownloaded
		}
	}
}

func (s *Service) Reconcile(ctx context.Context, jobID string) (domain.AcquisitionJob, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if job.Status != domain.JobTransferUncertain && job.Status != domain.JobSubmissionUncertain {
		return job, nil
	}
	if job.TargetReference == nil {
		return job, ErrReconcileUnavailable
	}
	for _, downloader := range s.downloaders {
		if !downloaderMatchesJob(downloader, job) {
			continue
		}
		reconciler, ok := downloader.(Reconciler)
		if !ok {
			return job, ErrReconcileUnavailable
		}
		reference := *job.TargetReference
		// The job is the source of truth for the operation. Older jobs created
		// before operation-aware references (and early COPY jobs) may contain a
		// generic or empty operation in their persisted target reference.
		reference.Operation = job.Operation
		result, reconcileErr := reconciler.Reconcile(ctx, reference)
		if reconcileErr != nil {
			checkedAt := s.now()
			updated, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
				current.StatusStale = true
				current.LastCheckedAt = &checkedAt
				if current.Operation == domain.OperationOpenListCopy {
					current.Message = "reconciliation failed; OpenList COPY outcome remains uncertain"
				} else {
					current.Message = "reconciliation failed; transfer outcome remains uncertain"
				}
				current.UpdatedAt = checkedAt
			})
			if updateErr != nil {
				return job, updateErr
			}
			return updated, reconcileErr
		}
		checkedAt := s.now()
		updated, updateErr := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			current.StatusStale = false
			current.LastCheckedAt = &checkedAt
			current.Message = result.Message
			if result.TargetReference != nil {
				reference := *result.TargetReference
				if reference.Path == "" && current.TargetReference != nil {
					reference.Path = current.TargetReference.Path
				}
				current.TargetReference = &reference
			}
			if result.State == "completed" {
				if current.Operation == domain.OperationOpenListCopy {
					current.Status = domain.JobDownloaded
					current.Phase = domain.PhaseDownloaded
					if current.ResultKind == "" {
						current.ResultKind = "openlist_copy"
					}
				} else {
					current.Status = domain.JobTransferred
					current.Phase = domain.PhaseSaved
					if current.ResultKind == "" {
						current.ResultKind = "openlist_transfer"
					}
				}
				current.Ownership = domain.JobOwnershipManaged
				current.Progress = 1
				current.Error = ""
				current.UncertaintyReason = ""
				current.RecoveryAction = ""
			} else if result.State == "failed" {
				current.Status = domain.JobFailed
				current.Phase = domain.PhaseFailed
				current.StatusStale = false
				current.Error = result.Message
				current.UncertaintyReason = ""
				current.RecoveryAction = ""
			} else if result.State == "cancelled" {
				current.Status = domain.JobCancelled
				current.Phase = domain.PhaseCancelled
				current.StatusStale = false
				current.Error = ""
				current.UncertaintyReason = ""
				current.RecoveryAction = ""
			} else {
				current.RecoveryAction = recoveryAction(current)
			}
			current.UpdatedAt = checkedAt
		})
		if updateErr != nil {
			return job, updateErr
		}
		return updated, nil
	}
	return job, ErrReconcileUnavailable
}

func (s *Service) Cancel(ctx context.Context, jobID string) (domain.AcquisitionJob, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	if job.Ownership == domain.JobOwnershipExternal {
		return job, ErrRemoteTaskNotManaged
	}
	if !CanCancel(job) {
		return job, ErrJobNotCancellable
	}
	if job.RemoteID == "" {
		cancelled := false
		updated, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
			if !CanCancel(*current) {
				return
			}
			current.Status = domain.JobCancelled
			current.Phase = domain.PhaseCancelled
			current.Message = "cancelled by user"
			current.UpdatedAt = s.now()
			cancelled = true
		})
		if err != nil {
			return updated, err
		}
		if !cancelled {
			return updated, ErrJobNotCancellable
		}
		return updated, nil
	}
	if job.RemoteID != "" {
		latest, latestErr := s.store.GetJob(job.ID)
		if latestErr != nil {
			return job, latestErr
		}
		if latest.Ownership == domain.JobOwnershipExternal {
			return latest, ErrRemoteTaskNotManaged
		}
		if !CanCancel(latest) {
			return latest, ErrJobNotCancellable
		}
		job = latest
		found := false
		for _, downloader := range s.downloaders {
			if !downloaderMatchesJob(downloader, job) {
				continue
			}
			found = true
			var cancelErr error
			if typed, ok := downloader.(OperationAwareDownloader); ok {
				cancelErr = typed.CancelRequest(ctx, RemoteCancelRequest{RemoteID: job.RemoteID, Operation: job.Operation})
			} else {
				cancelErr = downloader.Cancel(ctx, job.RemoteID)
			}
			if cancelErr != nil {
				return job, cancelErr
			}
			break
		}
		if !found {
			return job, fmt.Errorf("downloader %q is not configured", job.Downloader)
		}
	}
	cancelled := false
	updated, err := s.store.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		if !CanCancel(*current) {
			return
		}
		current.Status = domain.JobCancelled
		current.Phase = domain.PhaseCancelled
		current.Message = "cancelled by user"
		current.UpdatedAt = s.now()
		cancelled = true
	})
	if err != nil {
		return updated, err
	}
	if !cancelled {
		return updated, ErrJobNotCancellable
	}
	return updated, nil
}

func CanCancel(job domain.AcquisitionJob) bool {
	if job.Ownership == domain.JobOwnershipExternal {
		return false
	}
	switch job.Status {
	case domain.JobQueued:
		return true
	case domain.JobDownloading:
		return job.Operation != domain.OperationShareTransfer
	default:
		return false
	}
}
