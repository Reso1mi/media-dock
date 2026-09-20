package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
	"github.com/Reso1mi/media-dock/internal/store"
)

func TestWorkerSubmitsPersistedQueuedJob(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-worker",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-worker",
			SearchID: "search-worker",
			Provider: "test",
			Kind:     domain.CandidateKindMagnet,
			Title:    "worker",
			RawURL:   "magnet:?xt=urn:btih:worker",
		}},
	})
	done := make(chan struct{}, 1)
	downloader := &workerTestDownloader{done: done}
	service := NewService(memoryStore, []Downloader{downloader}, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.StartWorker(ctx, WorkerOptions{PollInterval: 5 * time.Millisecond, RequestTimeout: time.Second})

	job, err := service.Start(ctx, "candidate-worker", true)
	if err != nil {
		t.Fatalf("queued acquisition failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not submit queued job")
	}
	updated, err := service.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get worker job: %v", err)
	}
	if updated.Status != domain.JobDownloading {
		t.Fatalf("worker job status = %q, want downloading", updated.Status)
	}
}

func TestStartAllowsAPIOnlyAcquisitionWithoutLocalDirectory(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-api-only",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-api-only",
			SearchID: "search-api-only",
			Provider: "test",
			Kind:     domain.CandidateKindMagnet,
			Title:    "remote acquisition",
			RawURL:   "magnet:?xt=urn:btih:api-only",
		}},
	})

	downloader := &targetDirDownloader{}
	service := NewService(memoryStore, []Downloader{downloader}, "")
	job, err := service.Start(context.Background(), "candidate-api-only", true)
	if err != nil {
		t.Fatalf("API-only acquisition failed: %v", err)
	}
	if job.TargetDir != "" {
		t.Fatalf("target directory = %q, want empty for API-only mode", job.TargetDir)
	}
	if downloader.targetDir != "" {
		t.Fatalf("downloader received local target directory %q", downloader.targetDir)
	}
}

type workerTestDownloader struct {
	done chan<- struct{}
}

func (*workerTestDownloader) Name() string                   { return "worker-test" }
func (*workerTestDownloader) Supports(domain.Candidate) bool { return true }
func (d *workerTestDownloader) Start(context.Context, string, domain.Candidate, string) (Handle, error) {
	select {
	case d.done <- struct{}{}:
	default:
	}
	return Handle{RemoteID: "worker-remote", Ownership: HandleOwnershipManaged}, nil
}
func (*workerTestDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{Status: "downloading"}, nil
}
func (*workerTestDownloader) Cancel(context.Context, string) error { return nil }

type targetDirDownloader struct {
	targetDir string
}

func (*targetDirDownloader) Name() string { return "test-api-only" }

func (*targetDirDownloader) Supports(candidate domain.Candidate) bool {
	return candidate.Kind == domain.CandidateKindMagnet
}

func (d *targetDirDownloader) Start(_ context.Context, _ string, _ domain.Candidate, targetDir string) (Handle, error) {
	d.targetDir = targetDir
	return Handle{RemoteID: "remote-api-only"}, nil
}

func (*targetDirDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{Status: "downloading"}, nil
}

func (*targetDirDownloader) Cancel(context.Context, string) error { return nil }
