package acquisition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/store"
)

func TestStartWithIdempotencyReturnsTheOriginalJob(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-idempotency",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-idempotency",
			SearchID: "search-idempotency",
			Provider: "test",
			Kind:     domain.CandidateKindMagnet,
			Title:    "idempotent",
			RawURL:   "magnet:?xt=urn:btih:idempotency",
		}},
	})
	downloader := &countingDownloader{}
	service := NewService(memoryStore, []Downloader{downloader}, "")

	first, err := service.StartWithIdempotency(context.Background(), "candidate-idempotency", true, "request-1")
	if err != nil {
		t.Fatalf("first acquisition failed: %v", err)
	}
	second, err := service.StartWithIdempotency(context.Background(), "candidate-idempotency", true, "request-1")
	if err != nil {
		t.Fatalf("retry acquisition failed: %v", err)
	}
	if first.ID != second.ID || downloader.starts != 1 {
		t.Fatalf("retry created a new task: first=%#v second=%#v starts=%d", first, second, downloader.starts)
	}
}

func TestExternalRemoteTaskCannotBeCancelled(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-external",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-external",
			SearchID: "search-external",
			Provider: "test",
			Kind:     domain.CandidateKindMagnet,
			Title:    "external",
			RawURL:   "magnet:?xt=urn:btih:external",
		}},
	})
	downloader := &externalDownloader{}
	service := NewService(memoryStore, []Downloader{downloader}, "")
	job, err := service.Start(context.Background(), "candidate-external", true)
	if err != nil {
		t.Fatalf("external acquisition failed: %v", err)
	}
	if job.Ownership != domain.JobOwnershipExternal {
		t.Fatalf("ownership = %q, want external", job.Ownership)
	}
	if _, err := service.Cancel(context.Background(), job.ID); !errors.Is(err, ErrRemoteTaskNotManaged) {
		t.Fatalf("cancel error = %v, want ErrRemoteTaskNotManaged", err)
	}
	if downloader.cancels != 0 {
		t.Fatal("external task was cancelled")
	}
}

type countingDownloader struct {
	starts int
}

func (*countingDownloader) Name() string                   { return "counting" }
func (*countingDownloader) Supports(domain.Candidate) bool { return true }
func (d *countingDownloader) Start(context.Context, string, domain.Candidate, string) (Handle, error) {
	d.starts++
	return Handle{RemoteID: "counting-remote", Ownership: HandleOwnershipManaged}, nil
}
func (*countingDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{Status: "downloading"}, nil
}
func (*countingDownloader) Cancel(context.Context, string) error { return nil }

type externalDownloader struct {
	cancels int
}

func (*externalDownloader) Name() string                   { return "external" }
func (*externalDownloader) Supports(domain.Candidate) bool { return true }
func (*externalDownloader) Start(context.Context, string, domain.Candidate, string) (Handle, error) {
	return Handle{RemoteID: "existing-remote", Ownership: HandleOwnershipExternal}, nil
}
func (*externalDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{Status: "downloading"}, nil
}
func (d *externalDownloader) Cancel(context.Context, string) error {
	d.cancels++
	return nil
}
