// Package pipeline, helpers.go: small private helpers backing Run.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/graphd"
	"github.com/ensembyte/dtm/internal/parser"
)

// parsePrimary parses a revision's file view and returns the primary
// ecosystem's graph. The primary ecosystem is the first one present in
// the parsed set (deterministic: parser.Detect sorts by name), which
// matches the spec's "one ecosystem accurately" MVP scope.
func parsePrimary(rev Revision) (*core.Graph, error) {
	gos, err := parser.ParseFiles(rev.Files)
	if err != nil {
		return nil, err
	}
	if len(gos) == 0 {
		return &core.Graph{SnapshotID: rev.CommitSHA}, nil
	}
	g := parser.ToGraph(gos[0], rev.CommitSHA)
	g.CommitSHA = rev.CommitSHA
	return g, nil
}

// graphDiff delegates to graphd.Diff — the single place in the codebase
// where the diff algorithm is invoked, keeping pipeline's imports minimal.
func graphDiff(bg, cg *core.Graph) *core.GraphDiff {
	return graphd.Diff(bg, cg)
}

// offlineForGraph matches the bundled offline advisory dataset against the
// candidate-resolved changed packages. This is the fallback path when the
// live feed is unreachable; it never fabricates entries, it only returns
// what the shipped dataset actually contains.
func offlineForGraph(cg *core.Graph, diff *core.GraphDiff) []core.Advisory {
	var out []core.Advisory
	seen := map[string]bool{}
	for _, e := range diff.Entries {
		if e.Category == core.DiffUnchanged || e.CandVersion == "" {
			continue
		}
		key := e.Identity.String() + "@" + e.CandVersion
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, advisory.LookupOffline(e.Identity.Ecosystem, e.Identity.Name, e.CandVersion)...)
	}
	return out
}

// lockHash is the canonical SHA-256 of a lockfile, hex-encoded.
func lockHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
