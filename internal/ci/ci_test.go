// Tests for the CI module: policy-driven check-state decisions and the
// hard rule that a degraded (analysis-only) run can never report
// "success" as if execution happened.
package ci

import (
	"testing"

	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/risk"
)

func TestDecideBlockOnConfirmedFailure(t *testing.T) {
	p := DefaultPolicy()
	if got := Decide(risk.ConfirmedFailure, true, p); got != StateFailure {
		t.Errorf("Decide(confirmed, executed) = %s, want failure", got)
	}
}

func TestDecideWarnOnStrongWarning(t *testing.T) {
	p := DefaultPolicy()
	if got := Decide(risk.StrongWarning, true, p); got != StateWarning {
		t.Errorf("Decide(strong_warning, executed) = %s, want warning", got)
	}
}

// A degraded run with a review-level finding must floor to warning, never
// success — success would imply execution happened.
func TestDecideDegradedNeverSuccessWithFindings(t *testing.T) {
	p := DefaultPolicy()
	if got := Decide(risk.ReviewSuggested, false, p); got != StateWarning {
		t.Errorf("Decide(review, not-executed) = %s, want warning", got)
	}
	// But a genuinely clean analysis-only run (no findings) can report
	// success, with the caveat text carrying the no-execution note.
	if got := Decide(risk.NoIssueObserved, false, p); got != StateSuccess {
		t.Errorf("Decide(no_issue, not-executed) = %s, want success", got)
	}
}

// A policy that lowers the warn threshold to review_suggested makes a
// review-level run warn instead of pass.
func TestDecidePolicyThresholds(t *testing.T) {
	p := Policy{BlockOn: risk.ConfirmedFailure, WarnOn: risk.ReviewSuggested}
	if got := Decide(risk.ReviewSuggested, true, p); got != StateWarning {
		t.Errorf("Decide with lowered warn threshold = %s, want warning", got)
	}
}

// BuildComment shapes a concise, evidence-linked comment and keeps the top
// findings bounded.
type fakeSummary struct {
	findings []core.Finding
	url      string
	high     string
}

func (f fakeSummary) Highest() string              { return f.high }
func (f fakeSummary) ChangedCount() int            { return len(f.findings) }
func (f fakeSummary) TopFindings() []core.Finding  { return f.findings }
func (f fakeSummary) ReportURL() string            { return f.url }

func TestBuildCommentBoundedFindings(t *testing.T) {
	var fs []core.Finding
	for i := 0; i < 10; i++ {
		fs = append(fs, core.Finding{Severity: risk.ReviewSuggested, Dimension: "dependency_reach", Package: "pkg"})
	}
	c := BuildComment(StateWarning, fakeSummary{findings: fs, url: "https://example.test/report"}, "https://example.test/report")
	if c.Conclusion != "warn" {
		t.Errorf("conclusion = %s", c.Conclusion)
	}
	// Body must not list more than 3 findings.
	if countOccurrences(c.Body, "- [") > 3 {
		t.Errorf("comment body lists too many findings: %q", c.Body)
	}
	if c.ReportURL == "" {
		t.Errorf("report URL missing from comment struct")
	}
}

func countOccurrences(s, sub string) int {
	n := 0
	i := 0
	for {
		j := indexOf(s[i:], sub)
		if j < 0 {
			break
		}
		n++
		i += j + len(sub)
	}
	return n
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
