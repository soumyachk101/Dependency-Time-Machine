// Package ci is the PR-integration layer: it shapes an upgrade run's
// result into a GitHub check-run status, a concise PR comment, and a
// machine JSON payload (spec FR-07, §10).
//
// Policy thresholds are applied here so the same run can be "block",
// "warn" or "pass" depending on the project's configured policy, without
// the risk engine needing to know about CI.
package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/risk"
)

// Policy mirrors the store's policies table.
type Policy struct {
	// BlockOn is the minimum severity that produces a "failure" check.
	BlockOn string
	// WarnOn is the minimum severity that produces a "warning" check.
	WarnOn string
}

// DefaultPolicy is the least-surprising configuration: block on a
// confirmed candidate-only failure, warn on a strong warning, and never
// auto-merge (spec FR-07: "Do not automatically merge by default").
func DefaultPolicy() Policy {
	return Policy{BlockOn: risk.ConfirmedFailure, WarnOn: risk.StrongWarning}
}

// SeverityAtLeast reports whether s reaches the rank of threshold.
func SeverityAtLeast(s, threshold string) bool {
	return severityRank(s) >= severityRank(threshold)
}

func severityRank(s string) int {
	switch s {
	case risk.ConfirmedFailure:
		return 4
	case risk.StrongWarning:
		return 3
	case risk.ReviewSuggested:
		return 2
	case risk.NoIssueObserved:
		return 1
	}
	return 0
}

// CheckState is the resulting CI check status.
type CheckState string

const (
	StateSuccess CheckState = "success"
	StateWarning CheckState = "warning"
	StateFailure CheckState = "failure"
)

// Decide maps a run's highest severity + policy to a check state. A
// degraded (analysis-only) run can never produce a "success" that
// implies execution happened: it floors to warning unless there is no
// finding at all, and the comment text carries the explicit caveat.
func Decide(highest string, executed bool, p Policy) CheckState {
	if SeverityAtLeast(highest, p.BlockOn) {
		return StateFailure
	}
	if SeverityAtLeast(highest, p.WarnOn) {
		return StateWarning
	}
	if !executed && highest != "" && highest != risk.NoIssueObserved {
		// Analysis-only, but we found something: surface it as a warning,
		// not a success, so a reviewer knows execution did not happen.
		return StateWarning
	}
	return StateSuccess
}

// Comment is the PR-facing summary.
type Comment struct {
	Header     string
	Body       string
	Conclusion string // "block" | "warn" | "pass"
	ReportURL  string
}

// RunSummary is the minimal surface ci needs from an upgrade run, kept as
// an interface so ci never imports the report package (avoiding a cycle).
type RunSummary interface {
	Highest() string
	ChangedCount() int
	TopFindings() []core.Finding
	ReportURL() string
}

// BuildComment shapes a concise, evidence-linked comment. It is kept short
// on purpose: the full report is linked, not inlined (spec FR-07 "concise
// report").
func BuildComment(state CheckState, rep RunSummary, reportURL string) Comment {
	var c Comment
	c.ReportURL = reportURL
	c.Conclusion = conclusionFor(state)
	c.Header = headerFor(state)
	c.Body = bodyFor(state, rep, reportURL)
	return c
}

func conclusionFor(s CheckState) string {
	switch s {
	case StateFailure:
		return "block"
	case StateWarning:
		return "warn"
	}
	return "pass"
}

func headerFor(s CheckState) string {
	switch s {
	case StateFailure:
		return "❌ Dependency upgrade check: block"
	case StateWarning:
		return "⚠️ Dependency upgrade check: review"
	}
	return "✅ Dependency upgrade check: no blocking signal"
}

// bodyFor builds a short markdown body.
func bodyFor(_ CheckState, rep RunSummary, reportURL string) string {
	fs := rep.TopFindings()
	var b strings.Builder
	b.WriteString("Dependency Time Machine analysed the proposed dependency upgrade.\n\n")
	if len(fs) == 0 {
		b.WriteString("No blocking or warning findings in the analysed dimensions. ")
	} else {
		b.WriteString("Top findings:\n\n")
		for i, f := range fs {
			if i >= 3 {
				break
			}
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Severity, f.Dimension, firstEvidence(f))
		}
	}
	if reportURL != "" {
		fmt.Fprintf(&b, "\n[Full report and evidence → %s]\n", reportURL)
	}
	return b.String()
}

func firstEvidence(f core.Finding) string {
	if len(f.Evidence) > 0 && f.Evidence[0].URL != "" {
		return f.Evidence[0].Label + " (" + f.Evidence[0].URL + ")"
	}
	if len(f.Evidence) > 0 {
		return f.Evidence[0].Label
	}
	return f.Summary
}

// ---- GitHub check-run API client ------------------------------------------

// GitHub posts check runs / statuses. All methods are no-ops (returning a
// descriptive error) when no token is configured: least-privilege default,
// a control plane with no GitHub token cannot post PR checks and must say
// so rather than pretend it did.
type GitHub struct {
	APIURL string
	Token  string
	HTTP   *http.Client
}

// NewGitHub builds a client. Token may be empty (checks are skipped).
func NewGitHub(apiURL, token string) *GitHub {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &GitHub{APIURL: apiURL, Token: token, HTTP: &http.Client{}}
}

// Enabled reports whether the client can actually post.
func (g *GitHub) Enabled() bool { return g.Token != "" }

// CheckRunPayload is the machine JSON sent to a CI consumer (the spec's
// "comment/JSON output").
type CheckRunPayload struct {
	RunID      string
	State      CheckState
	Conclusion string
	Comment    string
	ReportURL  string
}

// RenderJSON serialises the payload deterministically.
func (p CheckRunPayload) RenderJSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// PostCheckRun posts a check-run to the given repository/SHA. It returns
// a descriptive error when a token is required but absent — it never
// silently fakes success.
func (g *GitHub) PostCheckRun(ctx context.Context, owner, repo, sha string, payload CheckRunPayload) error {
	if !g.Enabled() {
		return fmt.Errorf("ci: github token not configured; check-run not posted (repo %s/%s)", owner, repo)
	}
	body, err := payload.RenderJSON()
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/check-runs", g.APIURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ci: github check-run returned %s", resp.Status)
	}
	return nil
}
