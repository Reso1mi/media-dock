package acquisition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestCancelQueuedAcquisitionPreventsSubmission(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	job := domain.AcquisitionJob{
		ID:          "job-cancel-before-submit",
		CandidateID: "candidate-cancel-before-submit",
		Downloader:  "openlist",
		Status:      domain.JobQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := memoryStore.SaveJob(job); err != nil {
		t.Fatalf("save queued job: %v", err)
	}
	downloader := &startCountDownloader{}
	service := NewService(memoryStore, []Downloader{downloader}, "")

	cancelled, err := service.Cancel(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("cancel queued job: %v", err)
	}
	if cancelled.Status != domain.JobCancelled {
		t.Fatalf("cancelled status = %q, want cancelled", cancelled.Status)
	}
	if _, err := service.submit(context.Background(), job, domain.Candidate{}, downloader); !errors.Is(err, ErrJobNotCancellable) {
		t.Fatalf("submit after cancellation error = %v, want ErrJobNotCancellable", err)
	}
	if downloader.startCalls != 0 {
		t.Fatalf("downloader Start calls = %d, want 0", downloader.startCalls)
	}
}

func TestAcquiringJobsCannotBeCancelled(t *testing.T) {
	job := domain.AcquisitionJob{Downloader: "openlist", Status: domain.JobAcquiring}
	if CanCancel(job) {
		t.Fatal("OpenList job in acquiring state was reported cancellable")
	}
	if CanCancel(domain.AcquisitionJob{Downloader: "openlist", Operation: domain.OperationShareTransfer, Status: domain.JobDownloading}) {
		t.Fatal("OpenList share transfer was reported cancellable")
	}
	if !CanCancel(domain.AcquisitionJob{Downloader: "openlist", Operation: domain.OperationOpenListCopy, Status: domain.JobDownloading}) {
		t.Fatal("OpenList COPY was not reported cancellable")
	}
}

func TestOpenListUsesLongSubmissionTimeoutWithoutChangingOtherAdapters(t *testing.T) {
	openList, err := NewOpenListDownloader("http://127.0.0.1:5244", "token", "/Quark/MediaDock", nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewMemoryStore(), []Downloader{openList, &targetDirDownloader{}}, "")
	if got := service.requestTimeout("openlist", 30*time.Second); got != OpenListRequestTimeout {
		t.Fatalf("OpenList timeout = %s, want %s", got, OpenListRequestTimeout)
	}
	if got := service.requestTimeout("test-api-only", 30*time.Second); got != 30*time.Second {
		t.Fatalf("other adapter timeout = %s, want 30s", got)
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

type startCountDownloader struct {
	startCalls int
}

func (*startCountDownloader) Name() string { return "openlist" }

func (*startCountDownloader) Supports(domain.Candidate) bool { return true }

func (d *startCountDownloader) Start(context.Context, string, domain.Candidate, string) (Handle, error) {
	d.startCalls++
	return Handle{Completed: true}, nil
}

func (*startCountDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{}, nil
}

func (*startCountDownloader) Cancel(context.Context, string) error { return nil }

func TestOpenListGoalStoresCloudTransferPhaseAndReference(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	if err := memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-goal-openlist",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-goal-openlist",
			SearchID: "search-goal-openlist",
			Provider: "test",
			Kind:     domain.CandidateKindCloudShare,
			RawURL:   "https://pan.quark.cn/s/share-goal",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
	}))
	defer server.Close()
	openList, err := NewOpenListDownloaderWithProfile(server.URL, "token", "/夸克/MediaDock", "cloud-library", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{openList}, "")
	job, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID:   "candidate-goal-openlist",
		Goal:          domain.GoalSaveToCloud,
		TargetProfile: "cloud-library",
		Confirmed:     true,
	})
	if err != nil {
		t.Fatalf("start OpenList transfer: %v", err)
	}
	if job.Status != domain.JobTransferred || job.Phase != domain.PhaseSaved || job.ResultKind != "openlist_transfer" {
		t.Fatalf("unexpected completed job: %#v", job)
	}
	if job.TargetDir != "/夸克/MediaDock" || job.TargetReference == nil || job.TargetReference.Kind != "directory" || job.TargetReference.ProfileID != "cloud-library" || job.TargetReference.Operation != domain.OperationShareTransfer {
		t.Fatalf("target directory/reference = %q/%#v", job.TargetDir, job.TargetReference)
	}
	if job.TargetReference.Path != "/夸克/MediaDock" {
		t.Fatalf("target reference path = %q", job.TargetReference.Path)
	}
}

func TestOpenListRejectsLocalDownloadGoalWithoutDownloadExecutor(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-goal-local",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-goal-local",
			SearchID: "search-goal-local",
			Provider: "test",
			Kind:     domain.CandidateKindCloudShare,
			RawURL:   "https://pan.quark.cn/s/share-local",
		}},
	})
	transferCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/transfer" {
			transferCalls++
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"can_transfer":true}}`))
	}))
	defer server.Close()
	openList, err := NewOpenListDownloader(server.URL, "token", "/夸克/MediaDock", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{openList}, "")
	job, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID: "candidate-goal-local",
		Goal:        domain.GoalDownloadToLocal,
		Confirmed:   true,
	})
	if !errors.Is(err, ErrGoalUnsupported) {
		t.Fatalf("error = %v, want ErrGoalUnsupported", err)
	}
	if job.Status != domain.JobUnsupported || job.Phase != domain.PhaseUnsupported {
		t.Fatalf("unexpected unsupported job: %#v", job)
	}
	if transferCalls != 0 {
		t.Fatalf("OpenList transfer calls = %d, want 0", transferCalls)
	}
}

func TestMultipleMatchingProfilesRequireExplicitTarget(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-profiles",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-profiles",
			SearchID: "search-profiles",
			Provider: "test",
			Kind:     domain.CandidateKindMagnet,
			RawURL:   "magnet:?xt=urn:btih:profiles",
		}},
	})
	first := &profileTestDownloader{name: "bt-a", profile: "bt-a-profile"}
	second := &profileTestDownloader{name: "bt-b", profile: "bt-b-profile"}
	service := NewService(memoryStore, []Downloader{first, second}, "")
	if _, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID: "candidate-profiles",
		Confirmed:   true,
	}); !errors.Is(err, ErrTargetProfileRequired) {
		t.Fatalf("error = %v, want ErrTargetProfileRequired", err)
	}
	job, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID:   "candidate-profiles",
		TargetProfile: "bt-b-profile",
		Confirmed:     true,
	})
	if err != nil {
		t.Fatalf("explicit profile acquisition failed: %v", err)
	}
	if job.TargetProfile != "bt-b-profile" || second.starts != 1 || first.starts != 0 {
		t.Fatalf("profile selection = job=%#v first=%d second=%d", job, first.starts, second.starts)
	}
}

type profileTestDownloader struct {
	name    string
	profile string
	starts  int
}

func (d *profileTestDownloader) Name() string                 { return d.name }
func (*profileTestDownloader) Supports(domain.Candidate) bool { return true }
func (d *profileTestDownloader) Start(context.Context, string, domain.Candidate, string) (Handle, error) {
	d.starts++
	return Handle{RemoteID: d.name + "-remote", Ownership: HandleOwnershipManaged}, nil
}
func (*profileTestDownloader) Status(context.Context, string) (RemoteStatus, error) {
	return RemoteStatus{Status: "downloading"}, nil
}
func (*profileTestDownloader) Cancel(context.Context, string) error { return nil }
func (d *profileTestDownloader) TargetProfile() TargetProfile {
	return TargetProfile{
		ID:             d.profile,
		Type:           "test",
		DisplayName:    d.profile,
		SupportedKinds: []string{domain.CandidateKindMagnet},
		SupportedGoals: []domain.AcquisitionGoal{domain.GoalDownloadToLocal},
	}
}

func TestAsyncOpenListTransferAdvancesOnlyAfterRemoteCompletion(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	if err := memoryStore.SaveSearch(domain.SearchSession{
		ID:        "search-async-openlist",
		ExpiresAt: now.Add(time.Hour),
		Candidates: []domain.Candidate{{
			ID:       "candidate-async-openlist",
			SearchID: "search-async-openlist",
			Provider: "test",
			Kind:     domain.CandidateKindCloudShare,
			RawURL:   "https://pan.quark.cn/s/async-service",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	transferCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/transfer":
			transferCalls++
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"op-service","status":"transferring","progress":0.4}}`))
		case "/api/fs/transfer/status":
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"op-service","status":"completed","progress":1,"files":[{"path":"/Quark/Requested/service.mkv","size_bytes":456}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderWithProfile(server.URL, "token", "/Quark/MediaDock", "openlist-async", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{downloader}, "")
	job, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID:   "candidate-async-openlist",
		Goal:          domain.GoalSaveToCloud,
		TargetProfile: "openlist-async",
		TargetDir:     "/Quark/Requested",
		Confirmed:     true,
	})
	if err != nil {
		t.Fatalf("start async OpenList acquisition: %v", err)
	}
	if job.Status != domain.JobDownloading || job.Phase != domain.PhaseTransferring || job.RemoteID != "op-service" || job.TargetReference == nil || job.TargetReference.Path != "/Quark/Requested" {
		t.Fatalf("pending job = %#v, want downloading/transferring with operation id", job)
	}
	if transferCalls != 1 {
		t.Fatalf("transfer calls = %d, want 1", transferCalls)
	}

	completed, err := service.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("poll async OpenList acquisition: %v", err)
	}
	if completed.Status != domain.JobTransferred || completed.Phase != domain.PhaseSaved || completed.Progress != 1 {
		t.Fatalf("completed job = %#v, want transferred/saved", completed)
	}
	if completed.TargetReference == nil || completed.TargetReference.Path != "/Quark/Requested" || len(completed.TargetReference.Files) != 1 || completed.TargetReference.Files[0].Path != "/Quark/Requested/service.mkv" {
		t.Fatalf("completed target reference = %#v", completed.TargetReference)
	}
	if transferCalls != 1 {
		t.Fatalf("transfer calls after status poll = %d, want no resubmission", transferCalls)
	}
}

func TestStartRequestReplaysIdempotencyBeforeCandidateOrProfileLookup(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	existing := domain.AcquisitionJob{
		ID:             "job-existing-idempotent",
		CandidateID:    "candidate-no-longer-loaded",
		Downloader:     "openlist",
		Operation:      domain.OperationShareTransfer,
		Goal:           domain.GoalSaveToCloud,
		TargetProfile:  "openlist-profile",
		TargetDir:      "/Quark/library",
		Status:         domain.JobDownloading,
		Phase:          domain.PhaseTransferring,
		IdempotencyKey: "replay-after-expiry",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := memoryStore.SaveJob(existing); err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, nil, "")
	job, err := service.StartRequest(context.Background(), AcquisitionRequest{
		CandidateID:    existing.CandidateID,
		Goal:           domain.GoalSaveToCloud,
		TargetProfile:  existing.TargetProfile,
		TargetDir:      existing.TargetDir,
		Confirmed:      true,
		IdempotencyKey: existing.IdempotencyKey,
	})
	if err != nil {
		t.Fatalf("replay existing job: %v", err)
	}
	if job.ID != existing.ID {
		t.Fatalf("replayed job = %#v, want %s", job, existing.ID)
	}
}

func TestOpenListCopyRejectsConflictingOptionsBeforeCreatingJob(t *testing.T) {
	openList, err := NewOpenListDownloaderForProfile("http://127.0.0.1:5244", "token", "copy-profile", nil)
	if err != nil {
		t.Fatal(err)
	}
	memoryStore := store.NewMemoryStore()
	service := NewService(memoryStore, []Downloader{openList}, "")
	_, err = service.StartCopy(context.Background(), CopyAcquisitionRequest{
		TargetProfile: "copy-profile",
		SourcePath:    "/Quark/source.mkv",
		TargetDir:     "/Quark/library",
		Options:       domain.CopyOptions{Overwrite: true, SkipExisting: true},
		Confirmed:     true,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("conflicting COPY options error = %v, want ErrInvalidRequest", err)
	}
	jobs, err := service.ListJobs(store.JobQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("invalid COPY created %d jobs", len(jobs))
	}
}

func TestOpenListCopyServicePersistsCompletedJob(t *testing.T) {
	var copyCalls, statusCalls int
	var request struct {
		SourceDir string   `json:"src_dir"`
		TargetDir string   `json:"dst_dir"`
		Names     []string `json:"names"`
		Skip      bool     `json:"skip_existing"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/copy":
			copyCalls++
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode COPY request: %v", err)
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
		case "/api/fs/copy/status":
			statusCalls++
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","progress":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	openList, err := NewOpenListDownloaderForProfile(server.URL, "token", "copy-profile", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewMemoryStore(), []Downloader{openList}, "")
	job, err := service.StartCopy(context.Background(), CopyAcquisitionRequest{
		TargetProfile:  "copy-profile",
		SourcePath:     "/Quark/Incoming/movie.mkv",
		TargetDir:      "/Quark/Library",
		Confirmed:      true,
		IdempotencyKey: "copy-service-1",
	})
	if err != nil {
		t.Fatalf("start COPY: %v", err)
	}
	if job.Operation != domain.OperationOpenListCopy || job.Status != domain.JobDownloaded || job.Phase != domain.PhaseDownloaded || job.ResultKind != "openlist_copy" {
		t.Fatalf("unexpected completed COPY job: %#v", job)
	}
	if job.TargetReference == nil || job.TargetReference.Operation != domain.OperationOpenListCopy || job.TargetReference.Path != "/Quark/Library" {
		t.Fatalf("COPY target reference = %#v", job.TargetReference)
	}
	if copyCalls != 1 || statusCalls != 0 || request.SourceDir != "/Quark/Incoming" || request.TargetDir != "/Quark/Library" || len(request.Names) != 1 || request.Names[0] != "movie.mkv" || !request.Skip {
		t.Fatalf("COPY calls/status/request = %d/%d/%#v", copyCalls, statusCalls, request)
	}
	if _, err := service.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("get completed COPY: %v", err)
	}
	if statusCalls != 0 {
		t.Fatalf("completed COPY was polled %d times", statusCalls)
	}
}

func TestOpenListCopyIdempotencyDoesNotResubmitOrCrossWireIntent(t *testing.T) {
	copyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/copy" {
			copyCalls++
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	openList, err := NewOpenListDownloaderForProfile(server.URL, "token", "copy-profile", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewMemoryStore(), []Downloader{openList}, "")
	request := CopyAcquisitionRequest{TargetProfile: "copy-profile", SourcePath: "/Quark/source.mkv", TargetDir: "/Quark/library", Confirmed: true, IdempotencyKey: "copy-idempotency"}
	first, err := service.StartCopy(context.Background(), request)
	if err != nil {
		t.Fatalf("first COPY: %v", err)
	}
	second, err := service.StartCopy(context.Background(), request)
	if err != nil {
		t.Fatalf("replay COPY: %v", err)
	}
	if second.ID != first.ID || copyCalls != 1 {
		t.Fatalf("replayed COPY = %#v, calls=%d", second, copyCalls)
	}
	request.SourcePath = "/Quark/other.mkv"
	if _, err := service.StartCopy(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different COPY intent error = %v, want ErrIdempotencyConflict", err)
	}
	if copyCalls != 1 {
		t.Fatalf("conflicting COPY resubmitted %d times", copyCalls)
	}
}

func TestOpenListCopyServiceTracksAsyncStatusAndCancel(t *testing.T) {
	var statusCalls, cancelCalls int
	var cancelledID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"copy-service-op","status":"copying","progress":0.2}}`))
		case "/api/fs/copy/status":
			statusCalls++
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"copy-service-op","status":"completed","progress":1,"files":[{"path":"/Quark/library/movie.mkv"}]}}`))
		case "/api/fs/copy/cancel":
			cancelCalls++
			var payload struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode cancel request: %v", err)
			}
			cancelledID = payload.OperationID
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	openList, err := NewOpenListDownloaderForProfile(server.URL, "token", "copy-profile", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewMemoryStore(), []Downloader{openList}, "")
	job, err := service.StartCopy(context.Background(), CopyAcquisitionRequest{TargetProfile: "copy-profile", SourcePath: "/Quark/source.mkv", TargetDir: "/Quark/library", Confirmed: true})
	if err != nil {
		t.Fatalf("start async COPY: %v", err)
	}
	if job.Status != domain.JobDownloading || job.Phase != domain.PhaseCopying || job.RemoteID != "copy-service-op" {
		t.Fatalf("pending COPY job = %#v", job)
	}
	cancelled, err := service.Cancel(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("cancel COPY: %v", err)
	}
	if cancelled.Status != domain.JobCancelled || cancelled.Phase != domain.PhaseCancelled || cancelCalls != 1 || cancelledID != "copy-service-op" {
		t.Fatalf("cancelled COPY = %#v, calls=%d, id=%q", cancelled, cancelCalls, cancelledID)
	}
	if _, err := service.Get(context.Background(), job.ID); err != nil {
		t.Fatalf("get cancelled COPY: %v", err)
	}
	if statusCalls != 0 {
		t.Fatalf("cancelled COPY was polled %d times", statusCalls)
	}
}

func TestReconcileAsyncOpenListCopyUsesCopyStatus(t *testing.T) {
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/copy/status" {
			http.NotFound(w, r)
			return
		}
		statusCalls++
		_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"copy-reconcile-op","status":"completed","progress":1,"files":[{"path":"/Quark/library/reconciled.mkv"}]}}`))
	}))
	defer server.Close()
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	job := domain.AcquisitionJob{
		ID:            "job-reconcile-copy",
		Downloader:    "openlist",
		Operation:     domain.OperationOpenListCopy,
		TargetProfile: "copy-profile",
		Status:        domain.JobSubmissionUncertain,
		Phase:         domain.PhaseUncertain,
		TargetReference: &domain.TargetReference{
			Backend:     "openlist",
			Instance:    server.URL,
			ProfileID:   "copy-profile",
			Kind:        "directory",
			Operation:   domain.OperationDownload,
			Path:        "/Quark/library",
			OperationID: "copy-reconcile-op",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := memoryStore.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	openList, err := NewOpenListDownloaderForProfile(server.URL, "token", "copy-profile", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{openList}, "")
	updated, err := service.Reconcile(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reconcile COPY: %v", err)
	}
	if updated.Status != domain.JobDownloaded || updated.Phase != domain.PhaseDownloaded || updated.TargetReference == nil || updated.TargetReference.Operation != domain.OperationOpenListCopy || len(updated.TargetReference.Files) != 1 {
		t.Fatalf("reconciled COPY = %#v", updated)
	}
	if statusCalls != 1 {
		t.Fatalf("COPY status calls = %d, want 1", statusCalls)
	}
}

func TestReconcileOpenListFailureMarksJobFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/transfer/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"failed-transfer","status":"failed","error":"destination rejected"}}`))
	}))
	defer server.Close()
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	job := domain.AcquisitionJob{
		ID:            "job-reconcile-failed-transfer",
		Downloader:    "openlist",
		Operation:     domain.OperationShareTransfer,
		Goal:          domain.GoalSaveToCloud,
		TargetProfile: "openlist-failed",
		Status:        domain.JobTransferUncertain,
		Phase:         domain.PhaseUncertain,
		TargetReference: &domain.TargetReference{
			Backend:     "openlist",
			Instance:    server.URL,
			ProfileID:   "openlist-failed",
			Kind:        "directory",
			Path:        "/Quark/library",
			Operation:   domain.OperationShareTransfer,
			OperationID: "failed-transfer",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := memoryStore.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	openList, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-failed", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{openList}, "")
	updated, err := service.Reconcile(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reconcile failed transfer: %v", err)
	}
	if updated.Status != domain.JobFailed || updated.Phase != domain.PhaseFailed || updated.Error != "destination rejected" || updated.StatusStale {
		t.Fatalf("failed transfer job = %#v", updated)
	}
}

func TestReconcileAsyncOpenListTransferCompletesWithoutResubmission(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	now := time.Now()
	job := domain.AcquisitionJob{
		ID:            "job-reconcile-openlist",
		Downloader:    "openlist",
		Goal:          domain.GoalSaveToCloud,
		TargetProfile: "openlist-reconcile",
		Status:        domain.JobTransferUncertain,
		Phase:         domain.PhaseUncertain,
		ResultKind:    "openlist_transfer",
		TargetReference: &domain.TargetReference{
			Backend:     "openlist",
			ProfileID:   "openlist-reconcile",
			Kind:        "directory",
			Path:        "/Quark/MediaDock",
			OperationID: "op-reconcile",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := memoryStore.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	reconcileCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/transfer/status" {
			http.NotFound(w, r)
			return
		}
		reconcileCalls++
		_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","files":[{"path":"/Quark/MediaDock/reconciled.mkv"}]}}`))
	}))
	defer server.Close()
	if _, err := memoryStore.UpdateJob(job.ID, func(current *domain.AcquisitionJob) {
		current.TargetReference.Instance = server.URL
	}); err != nil {
		t.Fatal(err)
	}

	downloader, err := NewOpenListDownloaderWithProfile(server.URL, "token", "/Quark/MediaDock", "openlist-reconcile", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(memoryStore, []Downloader{downloader}, "")
	updated, err := service.Reconcile(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reconcile OpenList transfer: %v", err)
	}
	if updated.Status != domain.JobTransferred || updated.Phase != domain.PhaseSaved || updated.Progress != 1 {
		t.Fatalf("reconciled job = %#v, want transferred/saved", updated)
	}
	if updated.TargetReference == nil || len(updated.TargetReference.Files) != 1 || updated.TargetReference.Files[0].Path != "/Quark/MediaDock/reconciled.mkv" {
		t.Fatalf("reconciled target reference = %#v", updated.TargetReference)
	}
	if reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", reconcileCalls)
	}
}
