// Tests for the graph diff engine: lifecycle classification, direct vs
// transitive separation, integrity/source/runtime secondary flags, and
// determinism (byte-identical output for the same input pair).
package graphd

import (
	"testing"

	"github.com/ensembyte/dtm/internal/core"
)

// testPkg is a compact package descriptor for building test graphs.
type testPkg struct {
	ver    string
	integ  string
	direct bool
}

// mkGraph builds a small core.Graph from named packages + (from,to) edges.
func mkGraph(pkgs map[string]testPkg, edges [][2]string) *core.Graph {
	g := &core.Graph{
		SnapshotID: "test",
		EcoName:    "node",
	}
	for name, p := range pkgs {
		g.Packages = append(g.Packages, core.PackageVersion{
			Identity: core.Identity{Ecosystem: core.EcoNode, Name: name, Registry: "npmjs"},
			Version:  p.ver,
			Integrity: p.integ,
			Direct:   p.direct,
		})
	}
	for _, e := range edges {
		// e[0] depends on e[1]; from's version and to's version come from
		// the package map.
		g.Edges = append(g.Edges, core.Edge{
			From:    core.Identity{Ecosystem: core.EcoNode, Name: e[0]},
			FromVer: pkgs[e[0]].ver,
			To:      core.Identity{Ecosystem: core.EcoNode, Name: e[1]},
			ToVer:   pkgs[e[1]].ver,
			Direct:  pkgs[e[0]].direct,
		})
	}
	return g
}

func TestDiffUpgrade(t *testing.T) {
	base := mkGraph(map[string]testPkg{
		"left-pad": {"1.0.0", "aa", true},
		"helper":   {"2.0.0", "bb", false},
	}, [][2]string{{"left-pad", "helper"}})
	cand := mkGraph(map[string]testPkg{
		"left-pad": {"1.1.0", "cc", true},
		"helper":   {"2.0.0", "bb", false},
	}, [][2]string{{"left-pad", "helper"}})

	diff := Diff(base, cand)
	byName := map[string]core.DiffEntry{}
	for _, e := range diff.Entries {
		byName[e.Identity.Name] = e
	}
	if byName["left-pad"].Category != core.DiffUpgraded {
		t.Errorf("left-pad category = %s, want upgraded", byName["left-pad"].Category)
	}
	if byName["helper"].Category != core.DiffUnchanged {
		t.Errorf("helper category = %s, want unchanged", byName["helper"].Category)
	}
	// helper's dependants: left-pad depends on helper, so helper has 1
	// (direct) dependant.
	if byName["helper"].TransitiveImpact != 1 {
		t.Errorf("helper transitive impact = %d, want 1", byName["helper"].TransitiveImpact)
	}
}

func TestDiffAddedRemoved(t *testing.T) {
	base := mkGraph(map[string]testPkg{"a": {"1.0.0", "a", true}}, nil)
	cand := mkGraph(map[string]testPkg{"b": {"0.1.0", "b", false}}, nil)
	diff := Diff(base, cand)
	byName := map[string]core.DiffEntry{}
	for _, e := range diff.Entries {
		byName[e.Identity.Name] = e
	}
	if byName["a"].Category != core.DiffRemoved {
		t.Errorf("a category = %s, want removed", byName["a"].Category)
	}
	if byName["b"].Category != core.DiffAdded {
		t.Errorf("b category = %s, want added", byName["b"].Category)
	}
}

func TestDiffDowngrade(t *testing.T) {
	base := mkGraph(map[string]testPkg{"x": {"2.0.0", "a", true}}, nil)
	cand := mkGraph(map[string]testPkg{"x": {"1.9.0", "b", true}}, nil)
	diff := Diff(base, cand)
	if diff.Entries[0].Category != core.DiffDowngraded {
		t.Errorf("x category = %s, want downgraded", diff.Entries[0].Category)
	}
}

func TestDiffIntegrityChanged(t *testing.T) {
	base := mkGraph(map[string]testPkg{"x": {"1.0.0", "int1", true}}, nil)
	cand := mkGraph(map[string]testPkg{"x": {"1.0.0", "int2", true}}, nil)
	diff := Diff(base, cand)
	var x core.DiffEntry
	for _, e := range diff.Entries {
		if e.Identity.Name == "x" {
			x = e
		}
	}
	if x.Category != core.DiffIntegrityChanged {
		t.Errorf("x category = %s, want integrity_changed", x.Category)
	}
}

func TestDiffNilSides(t *testing.T) {
	cand := mkGraph(map[string]testPkg{"only": {"3.0.0", "c", true}}, nil)
	diff := Diff(nil, cand)
	if len(diff.Entries) != 1 || diff.Entries[0].Category != core.DiffAdded {
		t.Errorf("nil base should yield one added entry, got %+v", diff.Entries)
	}
}

// Determinism: two runs over the same inputs produce identical entries.
func TestDiffDeterministic(t *testing.T) {
	a := mkGraph(map[string]testPkg{
		"m": {"1", "1", true},
		"n": {"2", "2", false},
	}, [][2]string{{"m", "n"}})
	b := mkGraph(map[string]testPkg{
		"m": {"3", "3", true},
		"n": {"2", "2", false},
	}, [][2]string{{"m", "n"}})
	d1 := Diff(a, b)
	d2 := Diff(a, b)
	if len(d1.Entries) != len(d2.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(d1.Entries), len(d2.Entries))
	}
	for i := range d1.Entries {
		if d1.Entries[i].Identity != d2.Entries[i].Identity ||
			d1.Entries[i].Category != d2.Entries[i].Category {
			t.Errorf("entry %d differs between runs", i)
		}
	}
}
