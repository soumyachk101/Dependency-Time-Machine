// Package report renders upgrade-run evidence bundles in the formats the
// spec requires: a human/agent-readable markdown report and a machine
// JSON version. Both carry the full provenance chain (source SHA,
// lockfile hashes, runner digest) and are the canonical "attach to the PR"
// artifact.
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/risk"
)

// RunMeta is the provenance header every report must carry (spec §9).
// JSON tags use camelCase to match the frontend's typed report shape.
type RunMeta struct {
	RepoURL       string    `json:"repoUrl"`
	CommitSHA     string    `json:"commitSha"`
	BaseLockSHA   string    `json:"baseLockSha"`
	CandLockSHA   string    `json:"candLockSha"`
	RunnerImage   string    `json:"runnerImage"`
	// RunnerMode: "container" | "degraded".
	RunnerMode string `json:"runnerMode"`
	Commands   []string `json:"commands"`
	StartedAt  time.Time `json:"startedAt"`
	ArtifactURL string   `json:"artifactUrl,omitempty"`
	RetentionDays int   `json:"retentionDays"`
	// SampleData, when true, renders a prominent "SAMPLE DATA — demo"
	// banner on both markdown and JSON output (spec: demo data must be
	// labelled as sample, not presented as live results).
	SampleData bool `json:"sampleData,omitempty"`
}

// RenderOptions controls rendering. SampleData lives on RunMeta so the
// sample banner travels with the provenance block.
type RenderOptions struct {
	Meta RunMeta
}

// Report is the complete bundle.
type Report struct {
	Meta       RunMeta
	Diff       *core.GraphDiff
	Findings   []core.Finding
	Buckets    core.RiskBuckets
	Highest    string
	Missing    []string
	Notes      []core.ReleaseNote
	Advisories []core.Advisory
}

// New assembles a Report from the risk engine's output plus provenance.
func New(opts RenderOptions, diff *core.GraphDiff, rep *risk.Report, notes []core.ReleaseNote, advs []core.Advisory) *Report {
	sort.Slice(notes, func(i, j int) bool { return notes[i].Package.Name < notes[j].Package.Name })
	return &Report{
		Meta:       opts.Meta,
		Diff:       diff,
		Findings:   rep.Findings,
		Buckets:    rep.Buckets,
		Highest:    rep.HighestSeverity,
		Missing:    rep.MissingEvidence,
		Notes:      notes,
		Advisories: advs,
	}
}

// RenderMarkdown produces the human-facing report. Every section links to
// concrete evidence; the degraded-mode banner is rendered explicitly so a
// reader can never mistake analysis-only results for executed builds.
func (r *Report) RenderMarkdown() string {
	var b strings.Builder
	if r.Meta.SampleData {
		b.WriteString("> **SAMPLE DATA** — this report was generated from demo fixtures and does not represent a real upgrade run.\n\n")
	}
	if r.Meta.RunnerMode == "degraded" {
		b.WriteString("> **No isolated execution performed.** This run operated in degraded (analysis-only) mode: dependency graph, release notes and advisory matching were computed, but no build or test command was executed. Do not treat the absence of build/test findings here as verification that the candidate compiles or passes tests.\n\n")
	}
	b.WriteString("# Dependency upgrade run\n\n")
	fmt.Fprintf(&b, "- Repository: %s @ %s\n", r.Meta.RepoURL, r.Meta.CommitSHA)
	fmt.Fprintf(&b, "- Base lockfile SHA-256: `%s`\n", r.Meta.BaseLockSHA)
	fmt.Fprintf(&b, "- Candidate lockfile SHA-256: `%s`\n", r.Meta.CandLockSHA)
	fmt.Fprintf(&b, "- Runner: %s (%s)\n\n", r.Meta.RunnerImage, r.Meta.RunnerMode)

	if len(r.Meta.Commands) > 0 {
		b.WriteString("## Commands executed\n\n")
		for _, c := range r.Meta.Commands {
			fmt.Fprintf(&b, "- `%s`\n", c)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Result classification\n\n")
	switch r.Highest {
	case risk.ConfirmedFailure:
		b.WriteString("- **Confirmed candidate-only failure** — block on this run under the default policy.\n")
	case risk.StrongWarning:
		b.WriteString("- **Strong warning** — review recommended before merge.\n")
	case risk.ReviewSuggested:
		b.WriteString("- **Review suggested** — no hard failure; investigate the findings below.\n")
	default:
		if len(r.Findings) == 0 {
			b.WriteString("- No issue observed in the analysed dimensions.\n")
		} else {
			b.WriteString("- No blocking signal; findings below are informational.\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("## Changed packages\n\n")
	if r.Diff == nil || len(r.Diff.Entries) == 0 {
		b.WriteString("_No package-level changes detected between base and candidate._\n\n")
	} else {
		b.WriteString("| package | from | to | category | transitive dependants |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, e := range r.Diff.Entries {
			if e.Category == core.DiffUnchanged {
				continue
			}
			from := orDash(e.BaseVersion)
			to := orDash(e.CandVersion)
			direct := ""
			if e.CandDirect {
				direct = " (direct)"
			}
			fmt.Fprintf(&b, "| %s%s | %s | %s | %s | %d |\n", e.Identity.Name, direct, from, to, e.Category, e.TransitiveImpact)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Findings\n\n")
	if len(r.Findings) == 0 {
		b.WriteString("_No findings._\n\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "### [%s] %s — %s\n", f.Severity, f.Dimension, f.Package)
		fmt.Fprintf(&b, "Confidence %.2f. %s\n", f.Confidence, f.Summary)
		if len(f.Evidence) > 0 {
			b.WriteString("\nEvidence:\n")
			for _, ev := range f.Evidence {
				if ev.URL != "" {
					fmt.Fprintf(&b, "- %s %s — %s\n", ev.Kind, ev.Label, ev.URL)
				} else {
					fmt.Fprintf(&b, "- %s %s\n", ev.Kind, ev.Label)
				}
			}
		}
		b.WriteString("\n")
	}

	if len(r.Missing) > 0 {
		b.WriteString("## Missing evidence\n\n")
		b.WriteString("Confidence is capped on these dimensions because evidence was not available in this run:\n\n")
		for _, m := range r.Missing {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Release notes referenced\n\n")
	if len(r.Notes) == 0 {
		b.WriteString("_None captured._\n")
	} else {
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s@%s — %s (retrieved %s)\n", n.Package.Name, n.Version, n.SourceURL, n.RetrievedAt)
		}
	}
	b.WriteString("\n## Advisories referenced\n\n")
	if len(r.Advisories) == 0 {
		b.WriteString("_None matched (this is not a vulnerability scan)._\n")
	} else {
		for _, a := range r.Advisories {
			links := a.Links
			if len(links) > 0 {
				fmt.Fprintf(&b, "- %s [%s] %s — %s\n", a.AdvisoryID, a.Severity, a.Package.Name, strings.Join(links, " "))
			} else {
				fmt.Fprintf(&b, "- %s [%s] %s\n", a.AdvisoryID, a.Severity, a.Package.Name)
			}
		}
	}
	if r.Meta.ArtifactURL != "" {
		fmt.Fprintf(&b, "\nRaw artifacts: %s (retention %d days)\n", r.Meta.ArtifactURL, r.Meta.RetentionDays)
	}
	return b.String()
}

// JSON returns the machine-facing report for CI consumption.
func (r *Report) JSON() ([]byte, error) {
	type public struct {
		Meta       RunMeta        `json:"meta"`
		Diff       *core.GraphDiff `json:"diff"`
		Findings   []core.Finding  `json:"findings"`
		Buckets    core.RiskBuckets `json:"buckets"`
		Highest    string          `json:"highest"`
		Missing    []string        `json:"missing"`
		Notes      []core.ReleaseNote `json:"notes"`
		Advisories []core.Advisory    `json:"advisories"`
	}
	p := public{
		Meta: r.Meta, Diff: r.Diff, Findings: r.Findings,
		Buckets: r.Buckets, Highest: r.Highest, Missing: r.Missing,
		Notes: r.Notes, Advisories: r.Advisories,
	}
	return json.MarshalIndent(p, "", "  ")
}

// orDash renders empty versions as "-" in tables.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// FromJSON rehydrates a *Report from the public JSON shape produced by
// JSON() (camelCase, the artifact the API serves and the persistent
// store rehydrates after a restart). Distinct from encoding/json's
// default unmarshal of *Report, which would require untagged field
// names matching the Go identifiers.
func FromJSON(b []byte) (*Report, error) {
	type public struct {
		Meta       RunMeta        `json:"meta"`
		Diff       *core.GraphDiff `json:"diff"`
		Findings   []core.Finding  `json:"findings"`
		Buckets    core.RiskBuckets `json:"buckets"`
		Highest    string          `json:"highest"`
		Missing    []string        `json:"missing"`
		Notes      []core.ReleaseNote `json:"notes"`
		Advisories []core.Advisory    `json:"advisories"`
	}
	var p public
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &Report{
		Meta: p.Meta, Diff: p.Diff, Findings: p.Findings,
		Buckets: p.Buckets, Highest: p.Highest, Missing: p.Missing,
		Notes: p.Notes, Advisories: p.Advisories,
	}, nil
}
