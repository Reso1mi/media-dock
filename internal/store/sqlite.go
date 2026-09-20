package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
	_ "modernc.org/sqlite"
)

const sqliteSchemaVersion = 1

// SQLiteStore persists the minimum state needed to resume a search or
// acquisition after a process restart. It is intentionally local to the
// MediaDock configuration directory; media files are not stored here.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens or creates a local SQLite database and applies the current
// schema. The pure-Go driver keeps the CGO-disabled production image usable.
func OpenSQLite(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("sqlite path must not be empty")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// A single writer connection avoids SQLITE_BUSY surprises for the small
	// embedded deployment while WAL still lets read transactions coexist.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) configure() error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", statement, err)
		}
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (
			version INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS searches (
			id TEXT PRIMARY KEY,
			request_json TEXT NOT NULL,
			provider_health_json TEXT NOT NULL,
			warnings_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS candidates (
			id TEXT PRIMARY KEY,
			search_id TEXT NOT NULL REFERENCES searches(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			kind TEXT NOT NULL,
			title TEXT NOT NULL,
			source_name TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			quality TEXT NOT NULL,
			codec TEXT NOT NULL,
			audio TEXT NOT NULL,
			subtitles_json TEXT NOT NULL,
			seeders INTEGER NOT NULL,
			leechers INTEGER NOT NULL,
			completeness TEXT NOT NULL,
			published_at TEXT NOT NULL,
			tags_json TEXT NOT NULL,
			score REAL NOT NULL,
			rank INTEGER NOT NULL,
			raw_url TEXT NOT NULL,
			password TEXT NOT NULL,
			raw_payload_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS candidates_search_id_idx ON candidates(search_id)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			search_id TEXT NOT NULL,
			candidate_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			downloader TEXT NOT NULL,
			status TEXT NOT NULL,
			ownership TEXT NOT NULL DEFAULT 'unknown',
			remote_id TEXT NOT NULL,
			target_dir TEXT NOT NULL,
			idempotency_key TEXT NOT NULL DEFAULT '',
			status_stale INTEGER NOT NULL DEFAULT 0,
			last_checked_at TEXT NOT NULL DEFAULT '',
			progress REAL NOT NULL,
			message TEXT NOT NULL,
			error TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS jobs_updated_at_idx ON jobs(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate sqlite: %w", err)
		}
	}
	for column, definition := range map[string]string{
		"ownership":       "TEXT NOT NULL DEFAULT 'unknown'",
		"idempotency_key": "TEXT NOT NULL DEFAULT ''",
		"status_stale":    "INTEGER NOT NULL DEFAULT 0",
		"last_checked_at": "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.ensureColumn("jobs", column, definition); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS jobs_idempotency_key_idx
		ON jobs(idempotency_key) WHERE idempotency_key <> ''`); err != nil {
		return fmt.Errorf("create sqlite idempotency index: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_meta(version)
		SELECT ? WHERE NOT EXISTS (SELECT 1 FROM schema_meta)`, sqliteSchemaVersion); err != nil {
		return fmt.Errorf("record sqlite schema version: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect sqlite table %q: %w", table, err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan sqlite table info: %w", err)
		}
		if name == column {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite table info: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("add sqlite column %q.%q: %w", table, column, err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) SaveSearch(session domain.SearchSession) error {
	requestJSON, err := json.Marshal(session.Request)
	if err != nil {
		return fmt.Errorf("encode search request: %w", err)
	}
	providerHealthJSON, err := json.Marshal(session.ProviderHealth)
	if err != nil {
		return fmt.Errorf("encode provider health: %w", err)
	}
	warningsJSON, err := json.Marshal(session.Warnings)
	if err != nil {
		return fmt.Errorf("encode search warnings: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin search transaction: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO searches
		(id, request_json, provider_health_json, warnings_json, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		request_json=excluded.request_json,
		provider_health_json=excluded.provider_health_json,
		warnings_json=excluded.warnings_json,
		created_at=excluded.created_at,
		expires_at=excluded.expires_at`,
		session.ID, string(requestJSON), string(providerHealthJSON), string(warningsJSON),
		formatTime(session.CreatedAt), formatTime(session.ExpiresAt))
	if err != nil {
		return fmt.Errorf("save search: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM candidates WHERE search_id = ?", session.ID); err != nil {
		return fmt.Errorf("replace search candidates: %w", err)
	}
	for _, candidate := range session.Candidates {
		if err := insertCandidate(tx, candidate); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit search: %w", err)
	}
	return nil
}

func insertCandidate(tx *sql.Tx, candidate domain.Candidate) error {
	subtitlesJSON, err := json.Marshal(candidate.Subtitles)
	if err != nil {
		return fmt.Errorf("encode candidate subtitles: %w", err)
	}
	tagsJSON, err := json.Marshal(candidate.Tags)
	if err != nil {
		return fmt.Errorf("encode candidate tags: %w", err)
	}
	rawPayloadJSON, err := json.Marshal(candidate.RawPayload)
	if err != nil {
		return fmt.Errorf("encode candidate payload: %w", err)
	}
	_, err = tx.Exec(`INSERT INTO candidates
		(id, search_id, provider, kind, title, source_name, size_bytes, quality, codec, audio,
		subtitles_json, seeders, leechers, completeness, published_at, tags_json, score, rank,
		raw_url, password, raw_payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.SearchID, candidate.Provider, candidate.Kind, candidate.Title,
		candidate.SourceName, candidate.SizeBytes, candidate.Quality, candidate.Codec, candidate.Audio,
		string(subtitlesJSON), candidate.Seeders, candidate.Leechers, candidate.Completeness,
		candidate.PublishedAt, string(tagsJSON), candidate.Score, candidate.Rank, candidate.RawURL,
		candidate.Password, string(rawPayloadJSON), formatTime(candidate.CreatedAt))
	if err != nil {
		return fmt.Errorf("save candidate %q: %w", candidate.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetSearch(id string) (domain.SearchSession, error) {
	var session domain.SearchSession
	var requestJSON, providerHealthJSON, warningsJSON, createdAt, expiresAt string
	row := s.db.QueryRow(`SELECT id, request_json, provider_health_json, warnings_json, created_at, expires_at
		FROM searches WHERE id = ?`, id)
	if err := row.Scan(&session.ID, &requestJSON, &providerHealthJSON, &warningsJSON, &createdAt, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SearchSession{}, ErrNotFound
		}
		return domain.SearchSession{}, fmt.Errorf("load search %q: %w", id, err)
	}
	if err := json.Unmarshal([]byte(requestJSON), &session.Request); err != nil {
		return domain.SearchSession{}, fmt.Errorf("decode search request: %w", err)
	}
	if err := json.Unmarshal([]byte(providerHealthJSON), &session.ProviderHealth); err != nil {
		return domain.SearchSession{}, fmt.Errorf("decode provider health: %w", err)
	}
	if err := json.Unmarshal([]byte(warningsJSON), &session.Warnings); err != nil {
		return domain.SearchSession{}, fmt.Errorf("decode search warnings: %w", err)
	}
	var err error
	if session.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.SearchSession{}, fmt.Errorf("decode search created_at: %w", err)
	}
	if session.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return domain.SearchSession{}, fmt.Errorf("decode search expires_at: %w", err)
	}

	rows, err := s.db.Query(`SELECT id, search_id, provider, kind, title, source_name, size_bytes,
		quality, codec, audio, subtitles_json, seeders, leechers, completeness, published_at,
		tags_json, score, rank, raw_url, password, raw_payload_json, created_at
		FROM candidates WHERE search_id = ? ORDER BY rank ASC, id ASC`, id)
	if err != nil {
		return domain.SearchSession{}, fmt.Errorf("load search candidates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return domain.SearchSession{}, err
		}
		session.Candidates = append(session.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return domain.SearchSession{}, fmt.Errorf("iterate search candidates: %w", err)
	}
	if session.Candidates == nil {
		session.Candidates = []domain.Candidate{}
	}
	if session.ProviderHealth == nil {
		session.ProviderHealth = []domain.ProviderHealth{}
	}
	if session.Warnings == nil {
		session.Warnings = []string{}
	}
	return session, nil
}

func (s *SQLiteStore) GetCandidate(id string) (domain.Candidate, error) {
	row := s.db.QueryRow(`SELECT id, search_id, provider, kind, title, source_name, size_bytes,
		quality, codec, audio, subtitles_json, seeders, leechers, completeness, published_at,
		tags_json, score, rank, raw_url, password, raw_payload_json, created_at
		FROM candidates WHERE id = ?`, id)
	candidate, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Candidate{}, ErrNotFound
	}
	if err != nil {
		return domain.Candidate{}, fmt.Errorf("load candidate %q: %w", id, err)
	}
	return candidate, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCandidate(row scanner) (domain.Candidate, error) {
	var candidate domain.Candidate
	var subtitlesJSON, tagsJSON, rawPayloadJSON, createdAt string
	err := row.Scan(
		&candidate.ID, &candidate.SearchID, &candidate.Provider, &candidate.Kind, &candidate.Title,
		&candidate.SourceName, &candidate.SizeBytes, &candidate.Quality, &candidate.Codec, &candidate.Audio,
		&subtitlesJSON, &candidate.Seeders, &candidate.Leechers, &candidate.Completeness,
		&candidate.PublishedAt, &tagsJSON, &candidate.Score, &candidate.Rank, &candidate.RawURL,
		&candidate.Password, &rawPayloadJSON, &createdAt,
	)
	if err != nil {
		return domain.Candidate{}, err
	}
	if err := json.Unmarshal([]byte(subtitlesJSON), &candidate.Subtitles); err != nil {
		return domain.Candidate{}, fmt.Errorf("decode candidate subtitles: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &candidate.Tags); err != nil {
		return domain.Candidate{}, fmt.Errorf("decode candidate tags: %w", err)
	}
	if rawPayloadJSON != "" && rawPayloadJSON != "null" {
		if err := json.Unmarshal([]byte(rawPayloadJSON), &candidate.RawPayload); err != nil {
			return domain.Candidate{}, fmt.Errorf("decode candidate payload: %w", err)
		}
	}
	var parseErr error
	if candidate.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return domain.Candidate{}, fmt.Errorf("decode candidate created_at: %w", parseErr)
	}
	return candidate, nil
}

func (s *SQLiteStore) SaveJob(job domain.AcquisitionJob) error {
	_, err := s.db.Exec(`INSERT INTO jobs
		(id, search_id, candidate_id, provider, downloader, status, ownership, remote_id, target_dir,
		idempotency_key, status_stale, last_checked_at, progress, message, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		search_id=excluded.search_id,
		candidate_id=excluded.candidate_id,
		provider=excluded.provider,
		downloader=excluded.downloader,
		status=excluded.status,
		ownership=excluded.ownership,
		remote_id=excluded.remote_id,
		target_dir=excluded.target_dir,
		idempotency_key=excluded.idempotency_key,
		status_stale=excluded.status_stale,
		last_checked_at=excluded.last_checked_at,
		progress=excluded.progress,
		message=excluded.message,
		error=excluded.error,
		created_at=excluded.created_at,
		updated_at=excluded.updated_at`,
		job.ID, job.SearchID, job.CandidateID, job.Provider, job.Downloader, job.Status,
		job.Ownership, job.RemoteID, job.TargetDir, job.IdempotencyKey, job.StatusStale,
		formatOptionalTime(job.LastCheckedAt), job.Progress, job.Message, job.Error,
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save job %q: %w", job.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetJob(id string) (domain.AcquisitionJob, error) {
	row := s.db.QueryRow(`SELECT id, search_id, candidate_id, provider, downloader, status,
		ownership, remote_id, target_dir, idempotency_key, status_stale, last_checked_at, progress, message, error, created_at, updated_at
		FROM jobs WHERE id = ?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	if err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("load job %q: %w", id, err)
	}
	return job, nil
}

func (s *SQLiteStore) FindJobByIdempotencyKey(key string) (domain.AcquisitionJob, error) {
	if strings.TrimSpace(key) == "" {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	row := s.db.QueryRow(`SELECT id, search_id, candidate_id, provider, downloader, status,
		ownership, remote_id, target_dir, idempotency_key, status_stale, last_checked_at, progress, message, error, created_at, updated_at
		FROM jobs WHERE idempotency_key = ?`, strings.TrimSpace(key))
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	if err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("load idempotent job: %w", err)
	}
	return job, nil
}

func (s *SQLiteStore) ListJobs(query JobQuery) ([]domain.AcquisitionJob, error) {
	statement := `SELECT id, search_id, candidate_id, provider, downloader, status,
		ownership, remote_id, target_dir, idempotency_key, status_stale, last_checked_at, progress, message, error, created_at, updated_at FROM jobs`
	args := make([]any, 0, len(query.Statuses)+2)
	if len(query.Statuses) > 0 {
		placeholders := make([]string, len(query.Statuses))
		for index, status := range query.Statuses {
			placeholders[index] = "?"
			args = append(args, status)
		}
		statement += " WHERE status IN (" + strings.Join(placeholders, ",") + ")"
	}
	statement += " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]domain.AcquisitionJob, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed jobs: %w", err)
	}
	return jobs, nil
}

func (s *SQLiteStore) UpdateJob(id string, update func(*domain.AcquisitionJob)) (domain.AcquisitionJob, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("begin job update: %w", err)
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRow(`SELECT id, search_id, candidate_id, provider, downloader, status,
		ownership, remote_id, target_dir, idempotency_key, status_stale, last_checked_at, progress, message, error, created_at, updated_at
		FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AcquisitionJob{}, ErrNotFound
	}
	if err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("load job %q for update: %w", id, err)
	}
	update(&job)
	_, err = tx.Exec(`UPDATE jobs SET search_id=?, candidate_id=?, provider=?, downloader=?, status=?,
		ownership=?, remote_id=?, target_dir=?, idempotency_key=?, status_stale=?, last_checked_at=?,
		progress=?, message=?, error=?, created_at=?, updated_at=? WHERE id=?`,
		job.SearchID, job.CandidateID, job.Provider, job.Downloader, job.Status, job.Ownership,
		job.RemoteID, job.TargetDir, job.IdempotencyKey, job.StatusStale, formatOptionalTime(job.LastCheckedAt),
		job.Progress, job.Message, job.Error, formatTime(job.CreatedAt), formatTime(job.UpdatedAt), job.ID)
	if err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("update job %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("commit job update: %w", err)
	}
	return job, nil
}

func scanJob(row scanner) (domain.AcquisitionJob, error) {
	var job domain.AcquisitionJob
	var status, ownership, idempotencyKey, lastCheckedAt, createdAt, updatedAt string
	var statusStale int
	err := row.Scan(&job.ID, &job.SearchID, &job.CandidateID, &job.Provider, &job.Downloader,
		&status, &ownership, &job.RemoteID, &job.TargetDir, &idempotencyKey, &statusStale,
		&lastCheckedAt, &job.Progress, &job.Message, &job.Error, &createdAt, &updatedAt)
	if err != nil {
		return domain.AcquisitionJob{}, err
	}
	job.Status = domain.JobStatus(status)
	job.Ownership = domain.JobOwnership(ownership)
	job.IdempotencyKey = idempotencyKey
	job.StatusStale = statusStale != 0
	if lastCheckedAt != "" {
		checkedAt, err := parseTime(lastCheckedAt)
		if err != nil {
			return domain.AcquisitionJob{}, fmt.Errorf("decode job last_checked_at: %w", err)
		}
		job.LastCheckedAt = &checkedAt
	}
	var parseErr error
	if job.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("decode job created_at: %w", parseErr)
	}
	if job.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return domain.AcquisitionJob{}, fmt.Errorf("decode job updated_at: %w", parseErr)
	}
	return job, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
