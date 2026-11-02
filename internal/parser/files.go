// Package parser, files.go: unified dispatch across ecosystems.
package parser

import (
	"sort"
	"strings"

	"github.com/ensembyte/dtm/internal/core"
)

// Files is a filename -> raw-content view of a repository checkout.
// The control plane fills it from the Git provider or a local path; the
// parser never touches the filesystem itself.
type Files map[string][]byte

// Detect reports which ecosystems are present in a file set.
func Detect(files Files) []string {
	var out []string
	if _, _, ok := DetectNode(files); ok {
		out = append(out, "node")
	}
	if _, _, ok := DetectPython(files); ok {
		out = append(out, "python")
	}
	if _, _, ok := DetectRust(files); ok {
		out = append(out, "rust")
	}
	sort.Strings(out)
	return out
}

// ParseFiles parses every supported ecosystem present in the file set and
// returns the parsed graphs, one per ecosystem. A partial failure (one
// ecosystem's files malformed, others fine) returns the successful graphs
// plus an *AggregateError so callers can proceed with what they have while
// still knowing something was skipped.
func ParseFiles(files Files) ([]*GraphOut, error) {
	var out []*GraphOut
	var errs []string

	if m, l, ok := DetectNode(files); ok && l != "" {
		g, err := ParseNode(files[m], files[l], l)
		if err != nil {
			errs = append(errs, "node: "+err.Error())
		} else {
			out = append(out, g)
		}
	}

	// Python: the manifest is a requirements-style file; the "lock" may be
	// the same file (requirements.txt pins are already resolved) or a
	// dedicated lock. Either way the parser's input is the manifest
	// content itself.
	if m, _, ok := DetectPython(files); ok {
		g, err := ParsePython(files[m])
		if err != nil {
			errs = append(errs, "python: "+err.Error())
		} else {
			out = append(out, g)
		}
	}

	if m, l, ok := DetectRust(files); ok && l != "" {
		g, err := ParseRust(files[m], files[l])
		if err != nil {
			errs = append(errs, "rust: "+err.Error())
		} else {
			out = append(out, g)
		}
	}

	if len(errs) > 0 {
		return out, &AggregateError{Msg: strings.Join(errs, "; ")}
	}
	return out, nil
}

// AggregateError reports partial parse success (some ecosystems ok, some
// failed). Callers can still use the successful graphs.
type AggregateError struct{ Msg string }

func (e *AggregateError) Error() string { return e.Msg }

// ToGraph converts a GraphOut into a core.Graph, filling edges with
// resolved versions and sorting everything for deterministic output.
func ToGraph(g *GraphOut, snapID string) *core.Graph {
	if g == nil {
		return &core.Graph{SnapshotID: snapID}
	}
	eco := core.Ecosystem(g.Ecosystem)
	out := &core.Graph{SnapshotID: snapID, EcoName: g.Ecosystem}
	versionByName := map[string]string{}
	for _, p := range g.Packages {
		versionByName[p.Name] = p.Version
		out.Packages = append(out.Packages, core.PackageVersion{
			Identity:   core.Identity{Ecosystem: eco, Name: p.Name, Registry: registryFor(eco, p.Source)},
			Version:    p.Version,
			Integrity:  p.Integrity,
			Direct:     p.Direct,
			Source:     p.Source,
			RuntimeReq: p.RuntimeReq,
			DependsOn:  identitiesFor(eco, p.DependsOn),
		})
	}
	for _, e := range g.Edges {
		// Drop edges whose target can't be resolved (partial locks).
		if e.ToVer == "" {
			e.ToVer = versionByName[e.To]
		}
		if e.ToVer == "" {
			continue
		}
		out.Edges = append(out.Edges, core.Edge{
			From:    core.Identity{Ecosystem: eco, Name: e.From, Registry: registryFor(eco, "")},
			FromVer: e.FromVer,
			To:      core.Identity{Ecosystem: eco, Name: e.To, Registry: registryFor(eco, "")},
			ToVer:   e.ToVer,
			Direct:  e.Direct,
		})
	}
	// Deterministic ordering: packages by name, edges by (from,to,ver).
	sort.Slice(out.Packages, func(i, j int) bool {
		return out.Packages[i].Identity.Name < out.Packages[j].Identity.Name
	})
	sort.Slice(out.Edges, func(i, j int) bool {
		a, b := out.Edges[i], out.Edges[j]
		if a.From.Name != b.From.Name {
			return a.From.Name < b.From.Name
		}
		if a.To.Name != b.To.Name {
			return a.To.Name < b.To.Name
		}
		return a.ToVer < b.ToVer
	})
	return out
}

// registryFor maps a source label back to a registry identity component.
func registryFor(eco core.Ecosystem, src string) string {
	switch eco {
	case core.EcoNode:
		return "npmjs"
	case core.EcoPython:
		if src == "pypi" || src == "" {
			return "pypi"
		}
		return src
	case core.EcoRust:
		return src // sourceLabel already normalises to crates.io etc.
	}
	return "default"
}

func identitiesFor(eco core.Ecosystem, names []string) []core.Identity {
	out := make([]core.Identity, 0, len(names))
	for _, n := range names {
		out = append(out, core.Identity{Ecosystem: eco, Name: n})
	}
	return out
}
