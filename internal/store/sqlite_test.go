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
		ID:          "job-persisted",
		SearchID:    candidate.SearchID,
		CandidateID: candidate.ID,
		Provider:    candidate.Provider,
		Downloader:  "downloader-home",
		Status:      domain.JobDownloading,
		RemoteID:    "remote-7",
		TargetDir:   "/remote/downloads/job-persisted",
		Progress:    0.4,
		Message:     "downloading",
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
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
	if loadedJob.Status != domain.JobDownloading || loadedJob.RemoteID != job.RemoteID || loadedJob.Progress != job.Progress {
		t.Fatalf("loaded job = %#v", loadedJob)
	}
}
