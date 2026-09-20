package store

import "github.com/Reso1mi/media-dock/internal/domain"

// Store is the persistence boundary shared by search and acquisition services.
// Implementations must preserve the private candidate material (RawURL,
// Password and RawPayload) even though protocol serializers intentionally hide
// it from callers.
type Store interface {
	SaveSearch(domain.SearchSession) error
	GetSearch(string) (domain.SearchSession, error)
	GetCandidate(string) (domain.Candidate, error)

	SaveJob(domain.AcquisitionJob) error
	GetJob(string) (domain.AcquisitionJob, error)
	FindJobByIdempotencyKey(string) (domain.AcquisitionJob, error)
	ListJobs(JobQuery) ([]domain.AcquisitionJob, error)
	UpdateJob(string, func(*domain.AcquisitionJob)) (domain.AcquisitionJob, error)
}
