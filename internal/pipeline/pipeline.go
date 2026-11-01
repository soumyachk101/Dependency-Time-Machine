// Package pipeline is the orchestrator: it takes a base and a candidate
// revision of a repository, runs the full analysis pipeline, and produces
// the report + CI payload that the API and CLI expose.
package pipeline

import (
	"context"
	"fmt"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/parser"
	"github.com/ensembyte/dtm/internal/relnotes"
	"github.com/ensembyte/dtm/internal/report"
	"github.com/ensembyte/dtm/internal/risk"
)

// Revision is one side of a comparison: a commit's file view plus its
// lockfile bytes (for the hash recorded in every report).
type Revision struct {
	CommitSHA string
	Branch    string
	Files     parser.Files
	// LockfileBytes carries the raw bytes of the primary lockfile used to
	// compute the SHA-256 recorded on the run (reproducibility NFR).
	LockfileBytes []byte
}

// Options carries per-run configuration.
type Options struct {
	RepoURL      string
	RunnerMode   string // container | degraded
	RunnerImage  string
	AdvisoryFeed advisory.FeedStatus
	NotesEnabled bool
	// SampleData marks demo runs (UI checklist: demo data is labelled).
	SampleData bool
}

// Result is a finished pipeline run.
type Result struct {
	Report     *report.Report
	Advisory   advisory.FeedStatus
	Notes      []core.ReleaseNote
	Advisories []core.Advisory
	Diff       *core.GraphDiff
}

// Run executes the pipeline. Steps:
//  1. Parse base + candidate into core graphs.
//  2. Diff the graphs (deterministic).
//  3. Match advisories against candidate-resolved changed packages.
//  4. Capture release notes for changed packages (labelled as signals).
//  5. Build the risk report.
//
// Isolated execution is deliberately NOT run here: a pipeline run is the
// analysis half of an upgrade evaluation. Execution happens in the
// runner layer, and its results are fed back into risk.Evaluate by the
// orchestrator that hosts both (the API's run handler). This keeps the
// degraded/container boundary explicit and auditable.
func Run(ctx context.Context, base, cand Revision, opts Options) (*Result, error) {
	bg, err := parsePrimary(base)
	if err != nil {
		return nil, fmt.Errorf("pipeline: base parse: %w", err)
	}
	cg, err := parsePrimary(cand)
	if err != nil {
		return nil, fmt.Errorf("pipeline: candidate parse: %w", err)
	}

	diff := graphDiff(bg, cg)

	// Advisory matching: only changed/candidate-side packages.
	advClient := advisory.NewOSVClient("")
	var advisories []core.Advisory
	feedStatus := advisory.FeedOfflineOnly
	if opts.AdvisoryFeed == advisory.FeedOnline {
		for _, e := range diff.Entries {
			if e.Category == core.DiffUnchanged || e.CandVersion == "" {
				continue
			}
			hits, unreachable := advClient.Query(ctx, e.Identity.Ecosystem, e.Identity.Name, e.CandVersion)
			if unreachable {
				feedStatus = advisory.FeedUnreachable
				break // one failure marks the whole feed as unavailable;
				// we fall back to the offline dataset below and say so.
			}
			advisories = append(advisories, hits...)
		}
		if feedStatus != advisory.FeedUnreachable {
			feedStatus = advisory.FeedOnline
		}
	}
	if feedStatus == advisory.FeedUnreachable {
		// Offline dataset is the only trusted source now.
		advisories = append(advisories, offlineForGraph(cg, diff)...)
	}

	// Release notes: only when the caller opted in (network access is a
	// deliberate, per-run decision, not an always-on side effect).
	var notes []core.ReleaseNote
	if opts.NotesEnabled {
		rl := relnotes.NewClient()
		for _, e := range diff.Entries {
			if e.Category == core.DiffUnchanged || e.CandVersion == "" {
				continue
			}
			notes = append(notes, rl.Fetch(ctx, e.Identity.Ecosystem, e.Identity.Name, e.CandVersion))
		}
	}

	rep := report.New(report.RenderOptions{
		Meta: report.RunMeta{
			RepoURL:     opts.RepoURL,
			CommitSHA:   cand.CommitSHA,
			BaseLockSHA: lockHash(base.LockfileBytes),
			CandLockSHA: lockHash(cand.LockfileBytes),
			RunnerImage: opts.RunnerImage,
			RunnerMode:  opts.RunnerMode,
			SampleData:  opts.SampleData,
		},
	}, diff, risk.Evaluate(risk.Inputs{
		Diff:           diff,
		Notes:          notes,
		Advisories:     advisories,
		AdvisoryStatus: feedStatus,
	}), notes, advisories)

	return &Result{Report: rep, Advisory: feedStatus, Notes: notes, Advisories: advisories, Diff: diff}, nil
}
