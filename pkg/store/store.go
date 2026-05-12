// Package store provides durable storage for build history, task results,
// and the audit log.
//
// The primary implementation is SQLite (pure Go, no CGO via modernc.org/sqlite).
// A MemStore no-op is provided for tests and for code paths that run before
// a data directory is configured.
package store

import "time"

// BuildRecord is the flat, serialisable view of a build persisted in the DB.
// It is separate from scheduler.Build so the two packages stay decoupled.
type BuildRecord struct {
	ID           string
	RepoURL      string
	RepoFullName string
	CommitSHA    string
	Branch       string
	PRNumber     string
	TriggeredBy  string
	State        string // "queued","running","passed","failed","cancelled"
	CreatedAt    time.Time
	StartedAt    time.Time
	FinishedAt   time.Time
	Tasks        []TaskRecord
}

// TaskRecord is the flat view of one task within a build.
type TaskRecord struct {
	ID           string
	BuildID      string
	Name         string
	State        string
	ExitCode     int
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// AuditEntry is one row in the audit log.
type AuditEntry struct {
	ID       int64
	TS       time.Time
	Actor    string // user/agent/system
	Action   string // "build.submit", "build.cancel", "secret.put", etc.
	Resource string // build ID, secret name, etc.
	Detail   string // JSON or short human note
}

// Store persists build records and audit events.
type Store interface {
	// Build lifecycle
	SaveBuild(b *BuildRecord) error
	UpdateBuildState(id, state string, finishedAt time.Time) error
	UpdateTaskState(t *TaskRecord) error

	// Queries
	ListBuilds(limit int) ([]*BuildRecord, error)
	GetBuild(id string) (*BuildRecord, bool, error)

	// Audit
	AppendAudit(e *AuditEntry) error
	ListAudit(limit int) ([]*AuditEntry, error)

	// Retention
	DeleteBuildsBefore(cutoff time.Time) (int64, error)

	Close() error
}
