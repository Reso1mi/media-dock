package store

import (
	"testing"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
)

func TestSQLiteStorePersistsPrivateCandidateMaterialAndJobsAcrossReopen(t *testing.T) {
	databasePath := t.TempDir() + "/mediadock.db"
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	store, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	candidate := domain.Candidate{
		ID:         "candidate-persisted",
		SearchID:   "search-persisted",
		Provider:   "test",
		Kind:       domain.CandidateKindMagnet,
		Title:      "persisted candidate",
		Subtitles:  []string{"zh-CN"},
		Tags:       []string{"trusted"},
		RawURL:     "magnet:?xt=urn:btih:private",
		Password:   "secret-password",
		RawPayload: map[string]any{"upstream": "private-payload"},
		CreatedAt:  createdAt,
	}
	if err := store.SaveSearch(domain.SearchSession{
		ID:             "search-persisted",
		Request:        domain.SearchRequest{Query: "persisted"},
		Candidates:     []domain.Candidate{candidate},
		ProviderHealth: []domain.ProviderHealth{{Provider: "test", Available: true}},
		Warnings:       []string{"partial result"},
		CreatedAt:      createdAt,
		ExpiresAt:      createdAt.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save search: %v", err)
	}
	job := domain.AcquisitionJob{
		ID:              "job-persisted",
		SearchID:        candidate.SearchID,
		CandidateID:     candidate.ID,
		Provider:        candidate.Provider,
		Downloader:      "downloader-home",
		Goal:            domain.GoalSaveToCloud,
		TargetProfile:   "cloud-library",
		ResultKind:      "openlist_transfer",
		Phase:           domain.PhaseSaved,
		Status:          domain.JobTransferred,
		RequestDigest:   "digest-persisted",
		Attempt:         1,
		TargetReference: &domain.TargetReference{Backend: "openlist", Instance: "openlist:5244", ProfileID: "cloud-library", Kind: "directory", Path: "/夸克/MediaDock", ObservedAt: createdAt},
		RemoteID:        "remote-7",
		TargetDir:       "/remote/downloads/job-persisted",
		Progress:        1,
		Message:         "downloading",
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	copyJob := domain.AcquisitionJob{
		ID:            "job-copy-persisted",
		Provider:      "openlist",
		Downloader:    "openlist",
		Operation:     domain.OperationOpenListCopy,
		Goal:          domain.GoalDownloadToLocal,
		TargetProfile: "cloud-library",
		SourcePath:    "/夸克/Incoming/movie.mkv",
		TargetDir:     "/夸克/MediaDock",
		CopyOptions:   domain.CopyOptions{Overwrite: true, Merge: true},
		Phase:         domain.PhaseCopying,
		Status:        domain.JobDownloading,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	if err := store.SaveJob(copyJob); err != nil {
		t.Fatalf("save copy job: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	reopened, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()
	loadedSearch, err := reopened.GetSearch(candidate.SearchID)
	if err != nil {
		t.Fatalf("load search: %v", err)
	}
	if len(loadedSearch.Candidates) != 1 {
		t.Fatalf("loaded candidates = %d, want 1", len(loadedSearch.Candidates))
	}
	loadedCandidate := loadedSearch.Candidates[0]
	if loadedCandidate.RawURL != candidate.RawURL || loadedCandidate.Password != candidate.Password {
		t.Fatalf("private candidate material was not persisted: %#v", loadedCandidate)
	}
	if loadedCandidate.RawPayload["upstream"] != "private-payload" {
		t.Fatalf("raw payload was not persisted: %#v", loadedCandidate.RawPayload)
	}
	loadedJob, err := reopened.GetJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loadedJob.Status != domain.JobTransferred || loadedJob.Phase != domain.PhaseSaved || loadedJob.Goal != domain.GoalSaveToCloud || loadedJob.TargetProfile != "cloud-library" || loadedJob.RequestDigest != job.RequestDigest {
		t.Fatalf("loaded job intent/state = %#v", loadedJob)
	}
	if loadedJob.TargetReference == nil || loadedJob.TargetReference.Path != "/夸克/MediaDock" {
		t.Fatalf("loaded target reference = %#v", loadedJob.TargetReference)
	}
	loadedCopy, err := reopened.GetJob(copyJob.ID)
	if err != nil {
		t.Fatalf("load copy job: %v", err)
	}
	if loadedCopy.Operation != domain.OperationOpenListCopy || loadedCopy.SourcePath != copyJob.SourcePath || loadedCopy.TargetDir != copyJob.TargetDir || loadedCopy.CopyOptions != copyJob.CopyOptions {
		t.Fatalf("loaded copy intent = %#v", loadedCopy)
	}
}
