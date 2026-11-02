-- Dependency Time Machine — Postgres migrations
--
-- Mirror of the SQLite schema executed by internal/store (Migrate).
-- Types are mapped, not invented: the store's SQL is shared verbatim by
-- both drivers, so every column here is the same name and shape as the
-- SQLite DDL, with only the types adjusted to Postgres idiom.
--
-- Apply in order with:  psql "$DTM_DATABASE_URL" -f 0001_init.sql

CREATE TABLE IF NOT EXISTS projects (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  provider       TEXT NOT NULL DEFAULT 'local',
  owner          TEXT NOT NULL,
  repo           TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
  id              TEXT PRIMARY KEY,
  project_id      TEXT NOT NULL REFERENCES projects (id),
  commit_sha      TEXT NOT NULL,
  manifest_path   TEXT NOT NULL,
  lockfile_path   TEXT NOT NULL,
  lockfile_sha256 TEXT NOT NULL,
  ecosystem       TEXT NOT NULL,
  created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshots_project ON snapshots (project_id, commit_sha);

CREATE TABLE IF NOT EXISTS upgrade_runs (
  id                  TEXT PRIMARY KEY,
  project_id          TEXT NOT NULL REFERENCES projects (id),
  base_snapshot_id    TEXT NOT NULL REFERENCES snapshots (id),
  candidate_snapshot_id TEXT NOT NULL REFERENCES snapshots (id),
  state               TEXT NOT NULL,
  runner_mode         TEXT NOT NULL,          -- container | degraded
  runner_image_digest TEXT NOT NULL,
  started_at          TEXT NOT NULL,
  finished_at         TEXT NOT NULL DEFAULT '',
  result_class        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_runs_project ON upgrade_runs (project_id, started_at);

CREATE TABLE IF NOT EXISTS findings (
  id            TEXT PRIMARY KEY,
  run_id        TEXT NOT NULL REFERENCES upgrade_runs (id),
  dimension     TEXT NOT NULL,
  severity      TEXT NOT NULL,
  pkg_identity  TEXT NOT NULL,
  summary       TEXT NOT NULL,
  confidence    DOUBLE PRECISION NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_findings_run ON findings (run_id);

CREATE TABLE IF NOT EXISTS policies (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects (id),
  block_on   TEXT NOT NULL,
  warn_on    TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
