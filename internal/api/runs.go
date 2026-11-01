// Package api, runs.go: the in-memory + persistent run store and the
// StoredRun record that handlers and the CLI both consume.
package api

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/report"
)

// StoredRun is one upgrade run's complete, retrievable record.
type StoredRun struct {
	ID            string
	ProjectID     string
	RepoURL       string
	CommitSHA     string
	RunnerMode    string
	Executed      bool // false in degraded mode — drives the CI conclusion
	ResultClass   string // highest severity bucket
	Report        *report.Report
	Diff          *core.GraphDiff
	AdvisoryFeed  string
	JSONPayload   []byte // the machine report, pre-rendered for GET /report
}

// ---- RunSummary adapter so the ci package can shape comments without
// importing api (and vice versa would be circular).
type RunSummary struct {
	run       *StoredRun
	reportURL string
}

// NewRunSummary wraps a stored run for the ci package's BuildComment.
func NewRunSummary(run *StoredRun, reportURL string) RunSummary {
	return RunSummary{run: run, reportURL: reportURL}
}

func (s RunSummary) Highest() string {
	if s.run == nil || s.run.Report == nil {
		return ""
	}
	return s.run.Report.Highest
}

func (s RunSummary) ChangedCount() int {
	if s.run == nil || s.run.Diff == nil {
		return 0
	}
	n := 0
	for _, e := range s.run.Diff.Entries {
		if e.Category != core.DiffUnchanged {
			n++
		}
	}
	return n
}

func (s RunSummary) TopFindings() []core.Finding {
	if s.run == nil || s.run.Report == nil {
		return nil
	}
	f := s.run.Report.Findings
	if len(f) > 3 {
		return f[:3]
	}
	return f
}

// ReportURL satisfies the ci.RunSummary interface; the concrete URL is
// supplied at construction by the caller that owns routing.
func (s RunSummary) ReportURL() string { return s.reportURL }

// InMemoryRunStore is the MVP persistence: a mutex-guarded map with a
// bounded, deterministic listing. The SQLite-backed variant in store.go
// is what production uses; this one exists so the API is fully usable
// and testable without a database.
type InMemoryRunStore struct {
	mu   sync.RWMutex
	runs map[string]*StoredRun
}

// NewInMemoryRunStore builds an empty store.
func NewInMemoryRunStore() *InMemoryRunStore {
	return &InMemoryRunStore{runs: map[string]*StoredRun{}}
}

// Save upserts a run.
func (s *InMemoryRunStore) Save(run *StoredRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return nil
}

// Get retrieves a run by id.
func (s *InMemoryRunStore) Get(id string) (*StoredRun, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	return r, ok
}

// List returns up to limit runs, most-recently saved first. Ordering is
// by insertion sequence, not wall-clock, so output is deterministic for
// test purposes.
func (s *InMemoryRunStore) List(limit int) []*StoredRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*StoredRun, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	// Stable ordering: by ID, since IDs embed a monotonic component.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ensure json import stays live if a future handler serialises directly.
var _ = json.Marshal
