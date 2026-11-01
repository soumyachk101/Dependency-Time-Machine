// Package relnotes captures release notes and release metadata for
// resolved versions, recording source URL and retrieval time (spec FR-03).
//
// Signals detected from release notes are labelled as "signals, not
// proof" — the risk engine carries that disclaimer verbatim into findings.
package relnotes

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ensembyte/dtm/internal/core"
)

// Client fetches release notes from ecosystem-native endpoints. Offline,
// Callers pass a context that has already been checked against the network
// policy; when no fetcher is available the result carries a
// "retrieval_unavailable" status instead of empty body + "no signals".
type Client struct {
	HTTP *http.Client
}

// NewClient builds a client. An empty DTM_RELEASEROTES_DISABLED env value
// does not exist; disabling is done by omitting network access at the
// call site, which is the only safe configuration.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Fetch returns the release note for one resolved version. The source URL
// is recorded on the note so a report reader can verify it; RetrievedAt is
// the actual retrieval timestamp, or "unretrieved" when offline.
func (c *Client) Fetch(ctx context.Context, eco core.Ecosystem, name, version string) core.ReleaseNote {
	note := core.ReleaseNote{
		Package:     core.Identity{Ecosystem: eco, Name: name},
		Version:     version,
		RetrievedAt: "unretrieved",
	}
	url, ok := releaseNotesURL(eco, name, version)
	if !ok {
		note.Signals = []string{"release_notes_unavailable"}
		return note
	}
	note.SourceURL = url
	resp, err := c.GetContext(ctx, url)
	if err != nil {
		note.Signals = []string{"retrieval_unavailable"}
		return note
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		note.Signals = []string{"release_notes_unavailable"}
		return note
	}
	body := readCapped(resp)
	note.Body = body
	note.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	note.Signals = detectBreakingSignals(body)
	return note
}

// readCapped bounds release-note bodies (log/artifact hygiene, spec §12).
func readCapped(resp *http.Response) string {
	const cap_ = 64 * 1024
	buf := make([]byte, cap_)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

// GetContext is a small helper so the http.Client call stays readable.
func (c *Client) GetContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.HTTP.Do(req)
}

// releaseNotesURL returns the canonical notes URL for an ecosystem version
// (used as the evidence link even when the fetch itself is offline — the
// URL is verifiable by a human, the body is not).
func releaseNotesURL(eco core.Ecosystem, name, version string) (string, bool) {
	switch eco {
	case core.EcoNode:
		// npm packages: registry metadata endpoint carries release notes
		// only for the latest dist-tag; per-version notes live on the
		// package's own repo when published. We record the registry
		// metadata URL as the stable reference.
		return fmt.Sprintf("https://registry.npmjs.org/%s", name), true
	case core.EcoRust:
		if strings.Contains(name, "/") {
			owner, repo := splitPath(name)
			return fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s-%s", owner, repo, owner, version), true
		}
		return fmt.Sprintf("https://crates.io/api/v1/crates/%s/%s", name, version), true
	case core.EcoPython:
		return fmt.Sprintf("https://pypi.org/pypi/%s/%s/json", name, version), true
	}
	return "", false
}

func splitPath(p string) (string, string) {
	i := strings.IndexByte(p, '/')
	return p[:i], p[i+1:]
}

// ---- breaking-change signal detection --------------------------------------
//
// These are heuristics over release-note text. Every hit is emitted as a
// "signal", and the risk engine's finding text explicitly states that a
// signal is not proof of breakage.

var breakingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbreaking change[s]?\b`),
	regexp.MustCompile(`(?i)\bremoved\b.*\b(api|export|method|function)\b`),
	regexp.MustCompile(`(?i)\bno longer (supported|works|accepted|accepted|available)\b`),
	regexp.MustCompile(`(?i)renamed (to|as|from)\b`),
	regexp.MustCompile(`(?i)\bdeprecat\w+\b.*\b\w+(removed|deleted)\b`),
	regexp.MustCompile(`(?i)\brequires node (>=|at least)\b`),
	regexp.MustCompile(`(?i)\bminimum (node|rust|python) (version|requirement)\b`),
}

// detectBreakingSignals returns the set of signal labels present in body.
func detectBreakingSignals(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, re := range breakingPatterns {
		if re.MatchString(body) {
			label := signalLabel(re)
			if !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
		}
	}
	if len(out) == 0 {
		return []string{"no_breaking_signal"}
	}
	return out
}

func signalLabel(re *regexp.Regexp) string {
	// Short deterministic label derived from the first word of the
	// pattern's intent.
	switch {
	case strings.Contains(re.String(), "breaking"):
		return "mentions_breaking_changes"
	case strings.Contains(re.String(), "removed"):
		return "mentions_api_removal"
	case strings.Contains(re.String(), "no longer"):
		return "mentions_removal_of_support"
	case strings.Contains(re.String(), "renamed"):
		return "mentions_renaming"
	case strings.Contains(re.String(), "deprecat"):
		return "mentions_deprecation_removal"
	case strings.Contains(re.String(), "requires node"):
		return "mentions_node_requirement"
	case strings.Contains(re.String(), "minimum"):
		return "mentions_runtime_requirement"
	}
	return "unclassified_signal"
}
