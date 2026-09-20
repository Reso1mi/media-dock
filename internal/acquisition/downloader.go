package acquisition

import (
	"context"

	"github.com/Reso1mi/media-dock/internal/domain"
)

type HandleOwnership string

const (
	HandleOwnershipManaged  HandleOwnership = "managed"
	HandleOwnershipExternal HandleOwnership = "external"
)

type Handle struct {
	RemoteID  string
	Ownership HandleOwnership
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
