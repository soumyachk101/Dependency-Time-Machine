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
	_ "github.com/modernc.org/sqlite"
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

// Migrate applies the schema for both backends (SQL is portable; column
// types chosen to work under SQLite and Postgres alike).
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'local',
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  manifest_path TEXT NOT NULL,
  lockfile_path TEXT NOT NULL,
  lockfile_sha256 TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_project ON snapshots(project_id, commit_sha);

CREATE TABLE IF NOT EXISTS upgrade_runs (
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
);
CREATE INDEX IF NOT EXISTS idx_runs_project ON upgrade_runs(project_id, started_at);

CREATE TABLE IF NOT EXISTS findings (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  dimension TEXT NOT NULL,
  severity TEXT NOT NULL,
  pkg_identity TEXT NOT NULL,
  summary TEXT NOT NULL,
  confidence REAL NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_findings_run ON findings(run_id);

CREATE TABLE IF NOT EXISTS policies (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  block_on TEXT NOT NULL,
  warn_on TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
	)
	return err
}

// nowISO is the deterministic-ish timestamp helper (UTC, RFC3339).
func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

var _ = sql.ErrNoRows // keep the database/sql import alive if query helpers move
