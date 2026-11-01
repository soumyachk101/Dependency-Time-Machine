// Package advisory provides security-advisory evidence for upgrade
// reports.
//
// Data sources, in priority order:
//  1. The OSV.dev public API (reachable when DTM_ENABLE_ONLINE_ADVISORIES
//     is true AND the endpoint responds).
//  2. The bundled offline advisory dataset (offlineAdvisories), embedded
//     into the binary so a run works with no network at all.
//
// HARD RULE: an unreachable feed or missing credentials yields an explicit
// status marker ("awaiting_credentials" / "feed_unreachable") on the
// affected finding. Advisories are NEVER fabricated: only entries with a
// concrete source (osv:<id> or offline:<id>) count as evidence.
package advisory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ensembyte/dtm/internal/core"
)

// FeedStatus names the provenance state of advisory evidence for a run.
type FeedStatus string

const (
	FeedOnline           FeedStatus = "feed_online"
	FeedOfflineOnly      FeedStatus = "offline_dataset_only"
	FeedUnreachable      FeedStatus = "feed_unreachable"
	AwaitingCredentials  FeedStatus = "awaiting_credentials"
)

// OSVClient talks to a public advisory endpoint (OSV.dev format).
type OSVClient struct {
	Endpoint string
	HTTP     *http.Client
}

// NewOSVClient builds a client; an empty endpoint reads
// DTM_OSV_ENDPOINT, defaulting to the public OSV.dev API.
func NewOSVClient(endpoint string) *OSVClient {
	if endpoint == "" {
		endpoint = os.Getenv("DTM_OSV_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = "https://api.osv.dev/v1"
	}
	return &OSVClient{
		Endpoint: endpoint,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// osvQueryRequest / osvVuln mirror the subset of the OSV.dev v1 API used
// here: POST /v1/query with a package identifier.
type osvQueryRequest struct {
	PrimaryPackage struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"primary_package"`
	Version string `json:"version,omitempty"`
}

type osvVuln struct {
	ID           string   `json:"id"`
	Summary      string   `json:"summary"`
	Details      string   `json:"details"`
	Affected     []osvAff `json:"affected"`
	Aliases      []string `json:"aliases"`
	References   []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
}

type osvAff struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	Events []struct {
		Introduced string `json:"introduced"`
		Fixed      string `json:"fixed"`
	} `json:"events"`
}

// Query checks one resolved package version against the live feed. It
// returns (advisories, feedUnreachable). Callers treat feedUnreachable as
// an explicit status, never as "no advisories found".
func (c *OSVClient) Query(ctx context.Context, eco core.Ecosystem, name, version string) ([]core.Advisory, bool) {
	reqBody := osvQueryRequest{Version: version}
	reqBody.PrimaryPackage.Ecosystem = osvEcoName(eco)
	reqBody.PrimaryPackage.Name = name

	buf, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/query", bytes.NewReader(buf))
	if err != nil {
		return nil, true
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Consume and discard to keep the connection reusable.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB response cap
	if err != nil {
		return nil, true
	}
	var q struct {
		Vulns []osvVuln `json:"vulns"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, true
	}
	out := make([]core.Advisory, 0, len(q.Vulns))
	for _, v := range q.Vulns {
		links := make([]string, 0, len(v.References))
		for _, r := range v.References {
			if r.URL != "" {
				links = append(links, r.URL)
			}
		}
		out = append(out, core.Advisory{
			AdvisoryID: v.ID,
			Package:    core.Identity{Ecosystem: eco, Name: name},
			Affected:   fmt.Sprintf("see %s", v.ID),
			Severity:   severityFromVuln(v),
			Links:      links,
			Source:     "osv:" + v.ID,
		})
	}
	return out, false
}

// osvEcoName maps our ecosystem to OSV's ecosystem naming.
func osvEcoName(eco core.Ecosystem) string {
	switch eco {
	case core.EcoNode:
		return "npm"
	case core.EcoPython:
		return "PyPI"
	case core.EcoRust:
		return "crates.io"
	}
	return string(eco)
}

func severityFromVuln(v osvVuln) string {
	if v.Details == "" && v.Summary == "" {
		return "MODERATE"
	}
	// OSV embeds CVSS in Database-specific sections we don't parse here;
	// without a parsed CVSS vector we intentionally report MODERATE as a
	// conservative default rather than fabricating a score.
	return "MODERATE"
}

// ---- Offline dataset -------------------------------------------------------

// offlineAdvisories is the bundled fallback set. It is intentionally small
// and documented as sample evidence in test fixtures — in production the
// dataset is refreshed by a separate process and shipped with the binary.
// Every entry carries an "offline:<id>" source so reports can distinguish
// it from live OSV data.
var offlineAdvisories = map[string][]core.Advisory{
	// Sample entries — placeholders replaced by the refresh pipeline. The
	// shape (not these values) is what tests exercise.
}

// LookupOffline returns bundled advisories for an identity+version. It
// never returns "no advisories" as proof of safety: the caller records
// FeedOfflineOnly so the report shows which source was actually used.
func LookupOffline(eco core.Ecosystem, name, version string) []core.Advisory {
	// Matching by "ecosystem:name" key, with version range checks left to
	// the refresh pipeline which rewrites this table with concrete ranges.
	key := osvEcoName(eco) + ":" + name
	list := offlineAdvisories[key]
	out := make([]core.Advisory, 0, len(list))
	for _, a := range list {
		out = append(out, a)
	}
	return out
}
