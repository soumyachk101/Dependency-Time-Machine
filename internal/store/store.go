// Package store is the data layer: projects, snapshots, upgrade runs,
// policies and findings.
//
// Two interchangeable backends share the same SQL:
//   - SQLite (modernc, pure Go) — local dev and CI, zero external deps.
//   - Postgres — production (same schema; migrations live in
//     infrastructure/migrations).
//
// The control plane opens exactly one Store for its lifetime; all
// writes happen on the single-writer path so SQLite locking is not a
// concern in the MVP.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Driver names.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
	DriverMemory   = "memory"
)

// Store wraps *sql.DB with schema bootstrapping.
type Store struct {
	DB *sql.DB
}

// Open connects using the driver name:
//   - "sqlite": dsn is a file path; ":memory:" uses an in-memory db.
//   - "postgres": dsn is a postgres URL (DTM_DATABASE_URL).
func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	var db *sql.DB
	var err error
	switch driver {
	case DriverMemory:
		db, err = sql.Open("sqlite", "file:memdb?mode=memory&cache=shared")
	case DriverSQLite:
		// Ensure the directory exists for a file-backed db.
		if dsn != "" && dsn != ":memory:" {
			if dir := dsn[:len(dsn)-len(lastPathSeg(dsn))]; dir != "" {
				_ = os.MkdirAll(dir, 0o755)
			}
		}
		db, err = sql.Open("sqlite", dsn)
		if err == nil {
			// SQLite pragmas: keep the file usable by one writer without
			// WAL contention surprises.
			if _, err2 := db.Exec("PRAGMA journal_mode=WAL"); err2 != nil {
				_ = err2
			}
		}
	case DriverPostgres:
		db, err = sql.Open("postgres", dsn)
	default:
		return nil, fmt.Errorf("store: unknown driver %q", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// Keep the pool small for local dev; Postgres pools are sized in
	// deployment config.
	db.SetMaxOpenConns(4)
	s := &Store{DB: db}
	if err := s.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// lastPathSeg returns the final path component of dsn.
func lastPathSeg(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == ':' {
			return p[i+1:]
		}
	}
	return p
}

// Migrate applies the schema for both backends. Each statement is
// executed individually because the pure-Go SQLite driver does not
// reliably execute multi-statement batches in one Exec call.
func (s *Store) Migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'local',
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  manifest_path TEXT NOT NULL,
  lockfile_path TEXT NOT NULL,
  lockfile_sha256 TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_project ON snapshots(project_id, commit_sha)`,
		`CREATE TABLE IF NOT EXISTS upgrade_runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  base_snapshot_id TEXT NOT NULL,
  candidate_snapshot_id TEXT NOT NULL,
  state TEXT NOT NULL,
  runner_mode TEXT NOT NULL,
  runner_image_digest TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  result_class TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_project ON upgrade_runs(project_id, started_at)`,
		`CREATE TABLE IF NOT EXISTS findings (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  dimension TEXT NOT NULL,
  severity TEXT NOT NULL,
  pkg_identity TEXT NOT NULL,
  summary TEXT NOT NULL,
  confidence REAL NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '[]'
)`,
		`CREATE INDEX IF NOT EXISTS idx_findings_run ON findings(run_id)`,
		`CREATE TABLE IF NOT EXISTS policies (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  block_on TEXT NOT NULL,
  warn_on TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
		// Finished-run persistence (wired to the API since the run-store
		// rework): the durable twin of api.InMemoryRunStore. Both drivers
		// share this DDL; Postgres accepts it as-is (INTEGER/TEXT are
		// valid Postgres types). Executing it here keeps a long-lived
		// Postgres deployment self-healing without a migration file.
		`CREATE TABLE IF NOT EXISTS api_runs (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  repo_url TEXT NOT NULL DEFAULT '',
  commit_sha TEXT NOT NULL DEFAULT '',
  runner_mode TEXT NOT NULL DEFAULT '',
  executed INTEGER NOT NULL DEFAULT 0,
  result_class TEXT NOT NULL DEFAULT '',
  advisory_feed TEXT NOT NULL DEFAULT '',
  report_json TEXT NOT NULL DEFAULT '',
  diff_json TEXT NOT NULL DEFAULT '',
  artifact_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_api_runs_project ON api_runs(project_id, created_at)`,
	}
	for _, st := range stmts {
		if _, err := s.DB.ExecContext(ctx, st); err != nil {
			return err
		}
	}
	return nil
}

// nowISO is the deterministic-ish timestamp helper (UTC, RFC3339).
func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// Ping reports whether the underlying database is reachable (used by
// /readyz).
func (s *Store) Ping() error {
	if s.DB == nil {
		return nil
	}
	return s.DB.Ping()
}

// Close releases the underlying connection pool.
func (s *Store) Close() error {
	if s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

