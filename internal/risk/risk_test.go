// Tests for the risk engine's classification buckets: candidate-only vs
// pre-existing failure separation, advisory matching, release-signal
// labelling, degraded-mode (no execution) handling, and the hard rule
// that an infrastructure-absent run is never a regression.
package risk

import (
	"reflect"
	"testing"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/core"
)

// ---- helper -----------------------------------------------------------------

func changed(name, from, to string) *core.GraphDiff {
	return &core.GraphDiff{
		Entries: []core.DiffEntry{{
			Identity:    core.Identity{Ecosystem: core.EcoNode, Name: name, Registry: "npmjs"},
			BaseVersion: from,
			CandVersion: to,
			Category:    core.DiffUpgraded,
		}},
	}
}

// ---- tests ------------------------------------------------------------------

// Candidate-only build failure is a confirmed failure.
func TestCandidateOnlyBuildFailure(t *testing.T) {
	rep := Evaluate(Inputs{
		Diff: changed("left-pad", "1.0.0", "2.0.0"),
		Exec: []ExecResult{
			{Side: "base", BuildOK: true, TestOK: true, Executed: true},
			{Side: "candidate", BuildOK: false, TestOK: true, Executed: true, Failures: []string{"build.log: undefined symbol"}},
		},
	})
	if len(rep.Buckets.ConfirmedFailures) == 0 {
		t.Fatalf("expected a confirmed failure, got %+v", rep.Buckets)
	}
	if rep.HighestSeverity != ConfirmedFailure {
		t.Errorf("highest = %s", rep.HighestSeverity)
	}
}

// A test failure present in BOTH base and candidate is pre-existing, not a
// regression: it must not land in ConfirmedFailures.
func TestPreExistingTestFailure(t *testing.T) {
	rep := Evaluate(Inputs{
		Diff: changed("left-pad", "1.0.0", "2.0.0"),
		Exec: []ExecResult{
			{Side: "base", BuildOK: true, TestOK: false, Executed: true, Failures: []string{"test_a fails (env)"}},
			{Side: "candidate", BuildOK: true, TestOK: false, Executed: true, Failures: []string{"test_a fails (env)"}},
		},
	})
	if len(rep.Buckets.ConfirmedFailures) != 0 {
		t.Errorf("pre-existing failure must not be confirmed, got %v", rep.Buckets.ConfirmedFailures)
	}
	if len(rep.Buckets.ReviewSuggested) == 0 {
		t.Errorf("pre-existing failure should be review_suggested, got %+v", rep.Buckets)
	}
}

// Degraded / no-execution run: no ConfirmedFailures, and the engine must
// explicitly record missing execution evidence rather than claim success.
func TestNoExecutionMeansNoRegression(t *testing.T) {
	rep := Evaluate(Inputs{
		Diff: changed("left-pad", "1.0.0", "2.0.0"),
		Exec: []ExecResult{
			{Side: "base", Executed: false},
			{Side: "candidate", Executed: false},
		},
	})
	if len(rep.Buckets.ConfirmedFailures) != 0 {
		t.Errorf("non-executed run must never produce a confirmed failure")
	}
	foundExecMissing := false
	for _, m := range rep.MissingEvidence {
		if m == "build_result" || m == "test_result" {
			foundExecMissing = true
		}
	}
	if !foundExecMissing {
		t.Errorf("expected missing-evidence entries for build/test, got %v", rep.MissingEvidence)
	}
}

// A HIGH-severity advisory on the target version is a confirmed-failure
// bucket entry (the strongest non-execution evidence), still phrased as an
// advisory match, not a fabricated "regression".
func TestAdvisoryMatch(t *testing.T) {
	rep := Evaluate(Inputs{
		Diff: changed("left-pad", "1.0.0", "2.0.0"),
		Advisories: []core.Advisory{{
			AdvisoryID: "GHSA-xxxx",
			Package:    core.Identity{Ecosystem: core.EcoNode, Name: "left-pad", Registry: "npmjs"},
			Severity:   "HIGH",
			Links:      []string{"https://osv.dev/GHSA-xxxx"},
			Source:     "osv:GHSA-xxxx",
		}},
		AdvisoryStatus: advisory.FeedOnline,
		Exec: []ExecResult{
			{Side: "base", Executed: false},
			{Side: "candidate", Executed: false},
		},
	})
	if len(rep.Buckets.ConfirmedFailures) != 1 {
		t.Errorf("HIGH advisory should land in confirmed failures, got %+v", rep.Buckets)
	}
}

// When the advisory feed is unreachable, the security dimension is capped
// (missing-evidence) rather than reported as "no advisories".
func TestAdvisoryFeedUnreachable(t *testing.T) {
	rep := Evaluate(Inputs{
		Diff:           changed("left-pad", "1.0.0", "2.0.0"),
		AdvisoryStatus: advisory.FeedUnreachable,
		Exec: []ExecResult{
			{Side: "base", Executed: false},
			{Side: "candidate", Executed: false},
		},
	})
	secMissing := false
	for _, m := range rep.MissingEvidence {
		if m == "security" {
			secMissing = true
		}
	}
	if !secMissing {
		t.Errorf("unreachable feed should cap security confidence, got %v", rep.MissingEvidence)
	}
}

// A release note that merely mentions "no breaking changes" is
// no-breakage evidence, not a signal.
func TestReleaseSignalLabelling(t *testing.T) {
	notes := []core.ReleaseNote{{
		Package: core.Identity{Ecosystem: core.EcoNode, Name: "left-pad", Registry: "npmjs"},
		Version: "2.0.0",
		Signals: []string{"no_breaking_signal"},
	}}
	rep := Evaluate(Inputs{
		Diff:   changed("left-pad", "1.0.0", "2.0.0"),
		Notes:  notes,
		Exec: []ExecResult{
			{Side: "base", Executed: false},
			{Side: "candidate", Executed: false},
		},
	})
	// No breaking signal: nothing should escalate past review_suggested.
	for _, f := range rep.Findings {
		if f.Dimension == "release_signal" && f.Severity != ReviewSuggested {
			t.Errorf("no-break-signal finding should stay review_suggested, got %s", f.Severity)
		}
	}
}

// Determinism: same inputs, same output.
func TestEvaluateDeterministic(t *testing.T) {
	in := Inputs{
		Diff: changed("left-pad", "1.0.0", "2.0.0"),
		Exec: []ExecResult{
			{Side: "base", BuildOK: true, TestOK: true, Executed: true},
			{Side: "candidate", BuildOK: false, TestOK: true, Executed: true, Failures: []string{"boom"}},
		},
	}
	r1, r2 := Evaluate(in), Evaluate(in)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("finding counts differ")
	}
	for i := range r1.Findings {
		if !reflect.DeepEqual(r1.Findings[i], r2.Findings[i]) {
			t.Errorf("finding %d differs", i)
		}
	}
}
