// Package graphd implements deterministic dependency-graph diffing:
// direct/transitive separation, lifecycle classification
// (added/removed/upgraded/downgraded) and integrity/source tracking.
//
// All comparisons are total-order and input-order independent: results are
// emitted sorted by (ecosystem, name), so two runs over the same pair of
// lockfiles always produce byte-identical diffs.
package graphd

import (
	"sort"

	"github.com/ensembyte/dtm/internal/core"
)

// nodeKey is the comparison key for a package within a graph.
type nodeKey struct {
	eco  string
	name string
}

func keyOf(i core.Identity) nodeKey { return nodeKey{eco: string(i.Ecosystem), name: i.Name} }

func (k nodeKey) String() string { return k.eco + ":" + k.name }

// nodeInfo summarises one package's position in a graph.
type nodeInfo struct {
	pv        *core.PackageVersion
	dependants int // count of packages that (transitively) depend on this node
}

// indexGraph builds lookup and transitive-dependant counts for a graph.
// Transitive reach is computed as a reverse BFS over the edge set so that
// "dependency reach" in the risk engine reflects the whole closure, not
// just direct edges.
func indexGraph(g *core.Graph) map[nodeKey]*nodeInfo {
	infos := make(map[nodeKey]*nodeInfo, len(g.Packages))
	for i := range g.Packages {
		k := keyOf(g.Packages[i].Identity)
		infos[k] = &nodeInfo{pv: &g.Packages[i]}
	}

	// reverse adjacency: for each node, the set of nodes that depend on it
	rev := make(map[nodeKey]map[nodeKey]struct{})
	for _, e := range g.Edges {
		fk, tk := keyOf(e.From), keyOf(e.To)
		if rev[tk] == nil {
			rev[tk] = make(map[nodeKey]struct{})
		}
		rev[tk][fk] = struct{}{}
	}

	// For each package, count its transitive dependants (the size of its
	// closure in the rev graph) — this is the "dependency reach" number
	// the risk engine scores against.
	keys := sortedKeys(rev)
	for _, k := range keys {
		if _, ok := infos[k]; !ok {
			continue
		}
		seen := map[nodeKey]struct{}{}
		queue := make([]nodeKey, 0, len(rev[k]))
		for d := range rev[k] {
			seen[d] = struct{}{}
			queue = append(queue, d)
		}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for up := range rev[cur] {
				if _, ok := seen[up]; !ok {
					seen[up] = struct{}{}
					queue = append(queue, up)
				}
			}
		}
		infos[k].dependants = len(seen)
	}
	return infos
}

func sortedKeys(m map[nodeKey]map[nodeKey]struct{}) []nodeKey {
	out := make([]nodeKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].eco != out[j].eco {
			return out[i].eco < out[j].eco
		}
		return out[i].name < out[j].name
	})
	return out
}

// Diff compares base and candidate graphs and returns a deterministic
// GraphDiff. base or candidate may be nil (treated as an empty graph),
// which models "adding a new dependency tree from scratch".
func Diff(base, cand *core.Graph) *core.GraphDiff {
	bi := indexOrEmpty(base)
	ci := indexOrEmpty(cand)

	// Walk the union of package identities.
	type entrySeed struct {
		k     nodeKey
		bInfo *nodeInfo
		cInfo *nodeInfo
	}
	merged := map[nodeKey]*entrySeed{}
	for k, b := range bi {
		merged[k] = &entrySeed{k: k, bInfo: b, cInfo: ci[k]}
	}
	for k, c := range ci {
		if _, ok := merged[k]; !ok {
			merged[k] = &entrySeed{k: k, cInfo: c}
		}
	}
	seeds := make([]nodeKey, 0, len(merged))
	for k := range merged {
		seeds = append(seeds, k)
	}
	sort.Slice(seeds, func(i, j int) bool {
		if seeds[i].eco != seeds[j].eco {
			return seeds[i].eco < seeds[j].eco
		}
		return seeds[i].name < seeds[j].name
	})

	reachableChanged := 0
	for _, k := range seeds {
		s := merged[k]
		var e core.DiffEntry
		e.Identity = core.Identity{Ecosystem: core.Ecosystem(k.eco), Name: k.name}
		if s.bInfo != nil {
			e.BaseVersion = s.bInfo.pv.Version
			e.BaseDirect = s.bInfo.pv.Direct
			e.BaseIntegrity = s.bInfo.pv.Integrity
		}
		if s.cInfo != nil {
			e.CandVersion = s.cInfo.pv.Version
			e.CandDirect = s.cInfo.pv.Direct
			e.CandIntegrity = s.cInfo.pv.Integrity
		}

		switch {
		case s.bInfo == nil:
			e.Category = core.DiffAdded
		case s.cInfo == nil:
			e.Category = core.DiffRemoved
		default:
			verB, verC := e.BaseVersion, e.CandVersion
			// Primary classification is by version delta.
			cmp := compareVersions(verB, verC, e.Identity.Ecosystem)
			switch {
			case cmp > 0:
				e.Category = core.DiffUpgraded
			case cmp < 0:
				e.Category = core.DiffDowngraded
			}
			// Secondary flags: a version-unchanged package may still
			// have integrity, source or requirement changes — record those
			// as their own categories without losing the version result.
			if e.Category == "" {
				e.Category = core.DiffUnchanged
			}
			// Integrity change is recorded on the entry either way (it is
			// additive evidence, not mutually exclusive with version move).
			if e.BaseIntegrity != "" && e.CandIntegrity != "" && e.BaseIntegrity != e.CandIntegrity {
				markIntegrity(&e)
			}
			srcB, srcC := sourceOf(s.bInfo.pv), sourceOf(s.cInfo.pv)
			if srcB != "" && srcC != "" && srcB != srcC {
				markSource(&e)
			}
			reqB, reqC := runtimeOf(s.bInfo.pv), runtimeOf(s.cInfo.pv)
			if reqB != "" && reqC != "" && reqB != reqC {
				markRuntime(&e)
			}
			if s.bInfo.pv.Direct != s.cInfo.pv.Direct {
				e.ChangedDirect = true
			}
		}

		// Transitive impact: count dependants in the base graph for context.
		if s.bInfo != nil {
			e.TransitiveImpact = s.bInfo.dependants
		}

		if e.Category != core.DiffUnchanged {
			reachableChanged++
			// Packages that depend on a changed package are themselves in the
			// "reachable" blast radius for risk scoring.
			if s.bInfo != nil && s.bInfo.dependants > 0 {
				reachableChanged += s.bInfo.dependants
			}
		}
		entries = append(entries, e)
	}

	return &core.GraphDiff{Entries: entries, ReachableChanged: reachableChanged}
}

// indexOrEmpty is a nil-safe indexGraph for Diff's base/candidate sides.
func indexOrEmpty(g *core.Graph) map[nodeKey]*nodeInfo {
	if g == nil {
		return map[nodeKey]*nodeInfo{}
	}
	return indexGraph(g)
}

// sourceOf / runtimeOf are nil-safe extractors used by Diff.
func sourceOf(pv *core.PackageVersion) string {
	if pv == nil {
		return ""
	}
	return pv.Source
}
func runtimeOf(pv *core.PackageVersion) string {
	if pv == nil {
		return ""
	}
	return pv.RuntimeReq
}

// markIntegrity / markSource / markRuntime fold a secondary change into
// the entry while preserving the primary (version) category.
func markIntegrity(e *core.DiffEntry) {
	if e.Category == core.DiffUnchanged {
		e.Category = core.DiffIntegrityChanged
	}
}
func markSource(e *core.DiffEntry) {
	if e.Category == core.DiffUnchanged {
		e.Category = core.DiffSourceChanged
	}
}
func markRuntime(e *core.DiffEntry) {
	if e.Category == core.DiffUnchanged {
		e.Category = core.DiffRuntimeChanged
	}
}
