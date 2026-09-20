package store

import "github.com/Reso1mi/media-dock/internal/domain"

// JobQuery describes the bounded task-list query exposed by the application
// layer. Status filters are optional and are matched exactly.
type JobQuery struct {
	Limit    int
	Offset   int
	Statuses []domain.JobStatus
}
