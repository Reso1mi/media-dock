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
	RemoteID        string
	RemoteOperation domain.OperationKind
	Ownership       HandleOwnership
	Completed       bool
	ResultKind      string
	TargetReference *domain.TargetReference
}

type StartRequest struct {
	JobID         string
	Candidate     domain.Candidate
	Goal          domain.AcquisitionGoal
	TargetProfile string
	TargetDir     string
}

type CopyRequest struct {
	JobID         string
	TargetProfile string
	SourcePath    string
	TargetDir     string
	Options       domain.CopyOptions
}

type RemoteStatusRequest struct {
	RemoteID  string
	Operation domain.OperationKind
	TargetDir string
}

type RemoteCancelRequest struct {
	RemoteID  string
	Operation domain.OperationKind
}

type TargetProfile struct {
	ID                  string
	Type                string
	DisplayName         string
	SupportedKinds      []string
	SupportedGoals      []domain.AcquisitionGoal
	SupportedOperations []domain.OperationKind
}

// GoalAwareDownloader can opt into the explicit MediaDock acquisition goals.
// Legacy downloaders continue to receive the original Start call and are
// treated as local-download adapters only.
type GoalAwareDownloader interface {
	SupportsGoal(domain.Candidate, domain.AcquisitionGoal) bool
	StartRequest(context.Context, StartRequest) (Handle, error)
}

type TargetProfileProvider interface {
	TargetProfile() TargetProfile
}

type TargetReferenceProvider interface {
	TargetReference(string) *domain.TargetReference
}

type CopyDownloader interface {
	StartCopy(context.Context, CopyRequest) (Handle, error)
}

type OperationAwareDownloader interface {
	StatusRequest(context.Context, RemoteStatusRequest) (RemoteStatus, error)
	CancelRequest(context.Context, RemoteCancelRequest) error
}

type RemoteStatus struct {
	Status          string
	Progress        float64
	Message         string
	Error           string
	TargetReference *domain.TargetReference
}

type ReconcileResult struct {
	State           string
	Message         string
	TargetReference *domain.TargetReference
}

type Reconciler interface {
	Reconcile(context.Context, domain.TargetReference) (ReconcileResult, error)
}

type Downloader interface {
	Name() string
	Supports(domain.Candidate) bool
	Start(context.Context, string, domain.Candidate, string) (Handle, error)
	Status(context.Context, string) (RemoteStatus, error)
	Cancel(context.Context, string) error
}
