// runs.go: the durable twin of api.InMemoryRunStore. It exposes the same
// Save/Get/List surface so the API handlers are agnostic to which backing
// store is active; this one writes through the shared SQL database so
// completed runs, their findings and their artifact references survive a
// restart. Findings persist into the shared findings table (existing
// schema) rather than a second copy.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ensembyte/dtm/internal/api"
	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/report"
)

// PersistentRunStore records api runs in the shared SQL database
// (SQLite or Postgres). Build it from the server's already-open *Store
// so the connection pool lifecycle is unchanged.
type PersistentRunStore struct {
	db *sql.DB
}

// NewPersistentRunStore binds the persistent run store to an open Store.
// The api_runs schema is applied by Store.Open (shared DDL in Migrate),
// so no DDL happens here.
func NewPersistentRunStore(st *Store) *PersistentRunStore {
	return &PersistentRunStore{db: st.DB}
}

// Save upserts a run row and rewrites its findings into the shared
// findings table.
func (s *PersistentRunStore) Save(run *api.StoredRun) error {
	if run == nil {
		return fmt.Errorf("store: save nil run")
	}
	reportJSON, err := runReportJSON(run)
	if err != nil {
		return err
	}
	diffJSON := ""
	if run.Diff != nil {
		b, err := json.Marshal(run.Diff)
		if err != nil {
			return fmt.Errorf("store: save run diff: %w", err)
		}
		diffJSON = string(b)
	}
	artifactRef := run.ArtifactRef
	if artifactRef == "" && run.Report != nil {
		artifactRef = run.Report.Meta.ArtifactURL
	}

	_, err = s.db.Exec(`
INSERT INTO api_runs (
  id, project_id, repo_url, commit_sha, runner_mode, executed,
  result_class, advisory_feed, report_json, diff_json, artifact_ref, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  project_id    = excluded.project_id,
  repo_url      = excluded.repo_url,
  commit_sha    = excluded.commit_sha,
  runner_mode   = excluded.runner_mode,
  executed      = excluded.executed,
  result_class  = excluded.result_class,
  advisory_feed = excluded.advisory_feed,
  report_json   = excluded.report_json,
  diff_json     = excluded.diff_json,
  artifact_ref  = excluded.artifact_ref`,
		run.ID, run.ProjectID, run.RepoURL, run.CommitSHA, run.RunnerMode,
		boolToInt(run.Executed), run.ResultClass, run.AdvisoryFeed,
		reportJSON, diffJSON, artifactRef, nowISO())
	if err != nil {
		return fmt.Errorf("store: save run: %w", err)
	}
	return s.rewriteFindings(run)
}

// rewriteFindings upserts the run's findings rows into the shared
// findings table, replacing any prior rows for the same run id so a
// replayed run converges on the same set.
func (s *PersistentRunStore) rewriteFindings(run *api.StoredRun) error {
	if _, err := s.db.Exec(`DELETE FROM findings WHERE run_id = ?`, run.ID); err != nil {
		return fmt.Errorf("store: clear findings: %w", err)
	}
	if run.Report == nil {
		return nil
	}
	for i, f := range run.Report.Findings {
		evidence, _ := json.Marshal(f.Evidence)
		// Stable per-run finding ids, so re-saves do not grow the table.
		fid := fmt.Sprintf("%s-f%d", run.ID, i)
		if _, err := s.db.Exec(`
INSERT INTO findings (
  id, run_id, dimension, severity, pkg_identity, summary, confidence, evidence_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fid, run.ID, f.Dimension, f.Severity, f.Package,
			f.Summary, f.Confidence, string(evidence)); err != nil {
			return fmt.Errorf("store: save finding: %w", err)
		}
	}
	return nil
}

// runReportJSON returns the report's machine JSON: the pre-rendered
// payload when present (what the report endpoint serves), else a fresh
// marshal of the report itself.
func runReportJSON(run *api.StoredRun) (string, error) {
	if len(run.JSONPayload) > 0 {
		return string(run.JSONPayload), nil
	}
	if run.Report == nil {
		return "{}", nil
	}
	b, err := run.Report.JSON()
	if err != nil {
		return "", fmt.Errorf("store: render report JSON: %w", err)
	}
	return string(b), nil
}

// Get retrieves and re-hydrates a run by id.
func (s *PersistentRunStore) Get(id string) (*api.StoredRun, bool) {
	row := s.db.QueryRow(`
SELECT id, project_id, repo_url, commit_sha, runner_mode, executed,
       result_class, advisory_feed, report_json, diff_json, artifact_ref
FROM api_runs WHERE id = ?`, id)
	var run api.StoredRun
	var executed int
	var reportJSON, diffJSON string
	err := row.Scan(&run.ID, &run.ProjectID, &run.RepoURL, &run.CommitSHA,
		&run.RunnerMode, &executed, &run.ResultClass, &run.AdvisoryFeed,
		&reportJSON, &diffJSON, &run.ArtifactRef)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		// A read error surfaces as "not found" to the caller: the API's
		// 404 path is what handlers expect for an absent run.
		return nil, false
	}
	run.Executed = executed != 0
	run.JSONPayload = []byte(reportJSON)
	hydrateRunReport(&run, reportJSON)
	if len(diffJSON) > 0 {
		var d core.GraphDiff
		if derr := json.Unmarshal([]byte(diffJSON), &d); derr == nil {
			run.Diff = &d
		}
	}
	return &run, true
}

// List returns up to limit runs, ordered by id (mirrors the in-memory
// twin's deterministic ordering; limit of 0 means "no limit").
func (s *PersistentRunStore) List(limit int) []*api.StoredRun {
	q := `
SELECT id, project_id, repo_url, commit_sha, runner_mode, executed,
       result_class, advisory_feed, report_json, diff_json, artifact_ref
FROM api_runs ORDER BY id ASC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*api.StoredRun
	for rows.Next() {
		var run api.StoredRun
		var executed int
		var reportJSON, diffJSON string
		if err := rows.Scan(&run.ID, &run.ProjectID, &run.RepoURL, &run.CommitSHA,
			&run.RunnerMode, &executed, &run.ResultClass, &run.AdvisoryFeed,
			&reportJSON, &diffJSON, &run.ArtifactRef); err != nil {
			continue
		}
		run.Executed = executed != 0
		run.JSONPayload = []byte(reportJSON)
		hydrateRunReport(&run, reportJSON)
		if len(diffJSON) > 0 {
			var d core.GraphDiff
			if derr := json.Unmarshal([]byte(diffJSON), &d); derr == nil {
				run.Diff = &d
			}
		}
		out = append(out, &run)
	}
	return out
}

// hydrateRunReport re-parses the stored public report JSON back into a
// *report.Report so markdown rendering works after a restart. Best
// effort: on parse failure the caller's JSONPayload stays the
// authoritative report artifact.
func hydrateRunReport(run *api.StoredRun, reportJSON string) {
	if len(reportJSON) == 0 || reportJSON == "{}" {
		return
	}
	rep, err := report.FromJSON([]byte(reportJSON))
	if err == nil {
		run.Report = rep
	}
}

// ---- small helpers ------------------------------------------------------

// boolToInt converts a bool to the INTEGER column type used by the shared
// DDL (both SQLite and Postgres accept it).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
