// Package risk turns a graph diff plus evidence (release notes, advisory
// hits, execution results) into explainable, component-based findings and
// the regression-classification buckets required by the spec's risk model.
//
// Design rule (spec §8): never emit a single opaque "AI risk score". Every
// finding carries a dimension, a severity bucket, a confidence value and at
// least one piece of evidence (or an explicit missing-evidence warning).
package risk

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/core"
)

// Severity levels, in the spec's ordering.
const (
	ConfirmedFailure = "confirmed_failure"
	StrongWarning    = "strong_warning"
	ReviewSuggested  = "review_suggested"
	NoIssueObserved  = "no_issue_observed"
)

// severityRank orders the buckets so "block/warn" policy thresholds can
// compare them without string arithmetic.
func severityRank(s string) int {
	switch s {
	case ConfirmedFailure:
		return 4
	case StrongWarning:
		return 3
	case ReviewSuggested:
		return 2
	case NoIssueObserved:
		return 1
	}
	return 0
}

// ExecResult is the result normaliser's output for one side of a run. It
// is the only execution input the risk engine consumes — raw runner output
// is never mixed in.
type ExecResult struct {
	Side      string
	BuildOK   bool
	TestOK    bool
	Executed  bool // false when runner mode is degraded / infrastructure absent
	Failures  []string
	DurationMS int64
}

// Inputs to Evaluate. Any field may be empty; the engine degrades
// gracefully and records missing evidence rather than guessing.
type Inputs struct {
	Diff          *core.GraphDiff
	Notes         []core.ReleaseNote
	Advisories    []core.Advisory
	AdvisoryStatus advisory.FeedStatus // which advisory source actually produced the above
	Exec          []ExecResult        // base + candidate
	CandidateCommit string
}

// Report is the engine's output.
type Report struct {
	Findings    []core.Finding
	Buckets     core.RiskBuckets
	// HighestSeverity is the max across findings (drives policy block/warn).
	HighestSeverity string
	// MissingEvidence lists dimensions where confidence is capped because
	// an input was absent (spec: confidence + missing-evidence warnings).
	MissingEvidence []string
}

// Evaluate builds the report. It is pure (no I/O, no randomness) so it is
// trivially deterministic and unit-testable.
func Evaluate(in Inputs) *Report {
	rep := &Report{}
	findings := []core.Finding{}
	missing := map[string]bool{}

	// 1) Security: advisories matched against candidate-resolved versions.
	if len(in.Advisories) > 0 {
		for _, a := range in.Advisories {
			sev := StrongWarning
			if a.Severity == "CRITICAL" || a.Severity == "HIGH" {
				sev = ConfirmedFailure // a HIGH/CRITICAL advisory on the
				// target version is the strongest non-execution evidence
				// we have; still phrased as "confirmed advisory match",
				// not "confirmed regression".
			}
			ev := make([]core.Evidence, 0, len(a.Links)+1)
			ev = append(ev, core.Evidence{Kind: "advisory", Label: a.AdvisoryID, URL: a.Links[0]})
			for _, l := range a.Links[1:] {
				ev = append(ev, core.Evidence{Kind: "advisory", Label: "ref", URL: l})
			}
			findings = append(findings, core.Finding{
				Dimension: "security",
				Severity:  sev,
				Package:   a.Package.String(),
				Summary:   fmt.Sprintf("advisory %s matches resolved candidate version of %s (source %s)", a.AdvisoryID, a.Package.Name, a.Source),
				Evidence:  ev,
				Confidence: 0.9,
			})
		}
	} else {
		// No advisory evidence is itself a confidence limit, and its
		// reason matters: reachable-but-empty vs. unreachable differs.
		switch in.AdvisoryStatus {
		case advisory.FeedUnreachable, advisory.AwaitingCredentials:
			missing["security"] = true
		}
	}

	// 2) Release-note signals on upgraded/downgraded packages.
	changed := map[string]core.PackageTransition{}
	for _, e := range diffEntries(in.Diff) {
		if e.Category == core.DiffUnchanged {
			continue
		}
		changed[e.Identity.Name] = core.PackageTransition{
			Identity: e.Identity, From: e.BaseVersion, To: e.CandVersion,
		}
	}
	byPkg := map[string][]core.ReleaseNote{}
	for _, n := range in.Notes {
		byPkg[n.Package.Name] = append(byPkg[n.Package.Name], n)
	}
	for name, notes := range byPkg {
		tr, isChanged := changed[name]
		if !isChanged {
			continue // notes for an unchanged version carry no upgrade risk
		}
		for _, n := range notes {
			sigLabels := n.Signals
			if len(sigLabels) == 0 {
				sigLabels = []string{"no_breaking_signal"}
			}
			sev := ReviewSuggested
			conf := 0.4
			strong := false
			for _, s := range sigLabels {
				switch s {
				case "mentions_breaking_changes", "mentions_api_removal":
					strong = true
				}
			}
			if strong {
				sev = StrongWarning
				conf = 0.6
			}
			// A runtime-requirement signal raises confidence that an
			// execution mismatch is possible, but is still a signal.
			for _, s := range sigLabels {
				if s == "mentions_node_requirement" || s == "mentions_runtime_requirement" {
					conf = maxF(conf, 0.55)
					break
				}
			}
			ev := []core.Evidence{{Kind: "release_note", Label: strings.Join(sigLabels, ","), URL: n.SourceURL}}
			findings = append(findings, core.Finding{
				Dimension: "release_signal",
				Severity:  sev,
				Package:   tr.Identity.String(),
				Summary:   fmt.Sprintf("release notes for %s%s signal change (signals are not proof of breakage)", tr.Identity.Name, tr.To),
				Evidence:  ev,
				Confidence: conf,
			})
			_ = n.RetrievedAt
		}
	}

	// 3) Execution outcomes: candidate-only failures vs pre-existing.
	//    NFR Reliability: an infrastructure (non-executed) run is never
	//    classified as a dependency regression.
	base := sideResult(in.Exec, "base")
	cand := sideResult(in.Exec, "candidate")
	if base != nil && cand != nil && base.Executed && cand.Executed {
		if !cand.BuildOK && base.BuildOK {
			findings = append(findings, execFinding("build_result", ConfirmedFailure,
				"candidate-only build failure (base build succeeded)", cand.Failures, 0.85))
		}
		if !cand.TestOK && base.TestOK {
			findings = append(findings, execFinding("test_result", ConfirmedFailure,
				"candidate-only test failure (base tests passed)", cand.Failures, 0.85))
		}
		if !base.BuildOK && cand.BuildOK {
			// Pre-existing build problem, improved by the upgrade: note it
			// so a reviewer does not blame the upgrade.
			findings = append(findings, execFinding("build_result", NoIssueObserved,
				"pre-existing build failure in base, not present in candidate", base.Failures, 0.7))
		}
		if !base.TestOK && !cand.TestOK {
			findings = append(findings, execFinding("test_result", ReviewSuggested,
				"test failures present in both base and candidate (pre-existing, not caused by this upgrade)",
				unionFailures(base, cand), 0.7))
		}
	} else {
		// No executed evidence at all (degraded mode) or missing side.
		missing["build_result"] = true
		missing["test_result"] = true
		findings = append(findings, core.Finding{
			Dimension: "execution",
			Severity:  NoIssueObserved,
			Summary:   "no isolated execution evidence available in this run (degraded or infrastructure-absent mode); results are graph/advisory-only and must not be read as build/test verification",
			Confidence: 0.1,
		})
	}

	// 4) Graph reach: a single high-impact transitive change is a review
	//    suggestion even with no other signals.
	changedReach := 0
	var topTransitive []string
	for _, e := range diffEntries(in.Diff) {
		if e.Category == core.DiffUnchanged {
			continue
		}
		if e.TransitiveImpact >= 5 {
			changedReach++
			topTransitive = append(topTransitive, fmt.Sprintf("%s (%d dependants)", e.Identity.Name, e.TransitiveImpact))
		}
	}
	if changedReach > 0 {
		ev := []core.Evidence{{Kind: "graph", Label: fmt.Sprintf("%d high-reach changes", changedReach)}}
		for _, t := range topTransitive {
			ev = append(ev, core.Evidence{Kind: "graph", Label: t})
		}
		findings = append(findings, core.Finding{
			Dimension: "dependency_reach",
			Severity:  ReviewSuggested,
			Summary:   fmt.Sprintf("%d changed package(s) reach 5+ transitive dependants", changedReach),
			Evidence:  ev,
			Confidence: 0.5,
		})
	}

	// Missing-evidence warnings.
	for dim := range missing {
		rep.MissingEvidence = append(rep.MissingEvidence, dim)
	}
	sort.Strings(rep.MissingEvidence)

	// Assemble + order findings deterministically: by dimension, then
	// severity rank desc, then package name.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Dimension != findings[j].Dimension {
			return findings[i].Dimension < findings[j].Dimension
		}
		if severityRank(findings[i].Severity) != severityRank(findings[j].Severity) {
			return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
		}
		return findings[i].Package < findings[j].Package
	})
	rep.Findings = findings

	// Buckets.
	for _, f := range findings {
		switch f.Severity {
		case ConfirmedFailure:
			rep.Buckets.ConfirmedFailures = append(rep.Buckets.ConfirmedFailures, f.Package)
		case StrongWarning:
			rep.Buckets.StrongWarnings = append(rep.Buckets.StrongWarnings, f.Package)
		case ReviewSuggested:
			rep.Buckets.ReviewSuggested = append(rep.Buckets.ReviewSuggested, f.Package)
		}
	}
	if len(findings) == 0 {
		rep.Buckets.NoIssueObserved = true
	}
	for _, f := range findings {
		if severityRank(f.Severity) > severityRank(rep.HighestSeverity) {
			rep.HighestSeverity = f.Severity
		}
	}
	return rep
}

// ---- helpers ----------------------------------------------------------------

// diffEntries is a nil-safe extractor for the diff's entry list.
func diffEntries(d *core.GraphDiff) []core.DiffEntry {
	if d == nil {
		return nil
	}
	return d.Entries
}

// sideResult finds an ExecResult for a side, nil-safe.
func sideResult(exec []ExecResult, side string) *ExecResult {
	for i := range exec {
		if exec[i].Side == side {
			return &exec[i]
		}
	}
	return nil
}

func unionFailures(base, cand *ExecResult) []string {
	set := map[string]bool{}
	for _, f := range append(append([]string{}, base.Failures...), cand.Failures...) {
		set[f] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
