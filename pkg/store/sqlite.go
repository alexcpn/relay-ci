package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS builds (
	id            TEXT PRIMARY KEY,
	repo_url      TEXT NOT NULL DEFAULT '',
	repo_full_name TEXT NOT NULL DEFAULT '',
	commit_sha    TEXT NOT NULL DEFAULT '',
	branch        TEXT NOT NULL DEFAULT '',
	pr_number     TEXT NOT NULL DEFAULT '',
	triggered_by  TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'queued',
	created_at    DATETIME NOT NULL,
	started_at    DATETIME,
	finished_at   DATETIME
);

CREATE TABLE IF NOT EXISTS tasks (
	id            TEXT NOT NULL,
	build_id      TEXT NOT NULL REFERENCES builds(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'pending',
	exit_code     INTEGER NOT NULL DEFAULT 0,
	error_message TEXT NOT NULL DEFAULT '',
	started_at    DATETIME,
	finished_at   DATETIME,
	PRIMARY KEY (id, build_id)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor    TEXT NOT NULL DEFAULT '',
	action   TEXT NOT NULL DEFAULT '',
	resource TEXT NOT NULL DEFAULT '',
	detail   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS builds_created_at ON builds(created_at DESC);
CREATE INDEX IF NOT EXISTS audit_ts ON audit_log(ts DESC);
`

// SQLiteStore is a durable Store backed by a local SQLite file.
type SQLiteStore struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given path and applies
// the schema. Use ":memory:" for an in-process ephemeral store.
func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite with WAL supports one writer
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) SaveBuild(b *BuildRecord) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO builds
			(id,repo_url,repo_full_name,commit_sha,branch,pr_number,triggered_by,state,created_at,started_at,finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.RepoURL, b.RepoFullName, b.CommitSHA, b.Branch, b.PRNumber,
		b.TriggeredBy, b.State,
		nullTime(b.CreatedAt), nullTime(b.StartedAt), nullTime(b.FinishedAt),
	)
	return err
}

func (s *SQLiteStore) UpdateBuildState(id, state string, finishedAt time.Time) error {
	_, err := s.db.Exec(
		`UPDATE builds SET state=?, finished_at=? WHERE id=?`,
		state, nullTime(finishedAt), id,
	)
	return err
}

func (s *SQLiteStore) UpdateTaskState(t *TaskRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO tasks (id,build_id,name,state,exit_code,error_message,started_at,finished_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id,build_id) DO UPDATE SET
			state=excluded.state,
			exit_code=excluded.exit_code,
			error_message=excluded.error_message,
			started_at=excluded.started_at,
			finished_at=excluded.finished_at`,
		t.ID, t.BuildID, t.Name, t.State, t.ExitCode, t.ErrorMessage,
		nullTime(t.StartedAt), nullTime(t.FinishedAt),
	)
	return err
}

func (s *SQLiteStore) ListBuilds(limit int) ([]*BuildRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT id,repo_url,repo_full_name,commit_sha,branch,pr_number,triggered_by,
		       state,created_at,started_at,finished_at
		FROM   builds
		ORDER  BY created_at DESC
		LIMIT  ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var builds []*BuildRecord
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

func (s *SQLiteStore) GetBuild(id string) (*BuildRecord, bool, error) {
	row := s.db.QueryRow(`
		SELECT id,repo_url,repo_full_name,commit_sha,branch,pr_number,triggered_by,
		       state,created_at,started_at,finished_at
		FROM   builds WHERE id=?`, id)
	b, err := scanBuild(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	// Load tasks.
	taskRows, err := s.db.Query(`
		SELECT id,build_id,name,state,exit_code,error_message,started_at,finished_at
		FROM   tasks WHERE build_id=?`, id)
	if err != nil {
		return nil, false, err
	}
	defer taskRows.Close()
	for taskRows.Next() {
		t, err := scanTask(taskRows)
		if err != nil {
			return nil, false, err
		}
		b.Tasks = append(b.Tasks, *t)
	}
	return b, true, taskRows.Err()
}

func (s *SQLiteStore) AppendAudit(e *AuditEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO audit_log (ts,actor,action,resource,detail) VALUES (?,?,?,?,?)`,
		time.Now().UTC(), e.Actor, e.Action, e.Resource, e.Detail,
	)
	return err
}

func (s *SQLiteStore) ListAudit(limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id,ts,actor,action,resource,detail FROM audit_log ORDER BY ts DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Resource, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteBuildsBefore(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM builds WHERE created_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// --- helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanBuild(row scanner) (*BuildRecord, error) {
	b := &BuildRecord{}
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(
		&b.ID, &b.RepoURL, &b.RepoFullName, &b.CommitSHA, &b.Branch,
		&b.PRNumber, &b.TriggeredBy, &b.State,
		&b.CreatedAt, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		b.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		b.FinishedAt = finishedAt.Time
	}
	return b, nil
}

func scanTask(row scanner) (*TaskRecord, error) {
	t := &TaskRecord{}
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(
		&t.ID, &t.BuildID, &t.Name, &t.State, &t.ExitCode, &t.ErrorMessage,
		&startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		t.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		t.FinishedAt = finishedAt.Time
	}
	return t, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
