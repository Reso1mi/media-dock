package acquisition

import (
	"context"

	"nas-bot/internal/domain"
)

type Handle struct {
	RemoteID string
}

type RemoteStatus struct {
	Status   string
	Progress float64
	Message  string
	Error    string
}

type Downloader interface {
	Name() string
	Supports(domain.Candidate) bool
	Start(context.Context, string, domain.Candidate, string) (Handle, error)
	Status(context.Context, string) (RemoteStatus, error)
	Cancel(context.Context, string) error
}
