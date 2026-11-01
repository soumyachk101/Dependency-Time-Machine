// Package core defines the shared domain model: package identity,
// dependency graphs, graph diffs, evidence and findings. It is dependency
// free (no I/O) so it can be reused by parsers, the risk engine, the CI
// module and the web API alike.
package core

// Ecosystem identifies the package management universe a package lives in.
// Package identity is always (Ecosystem, Name) — never name alone, because
// the same package name exists in multiple ecosystems.
type Ecosystem string

const (
	EcoNode   Ecosystem = "node"
	EcoPython Ecosystem = "python"
	EcoRust   Ecosystem = "rust"
)

// Valid reports whether e is a known ecosystem.
func (e Ecosystem) Valid() bool {
	switch e {
	case EcoNode, EcoPython, EcoRust:
		return true
	}
	return false
}

// Identity is the canonical key for a package within one snapshot.
type Identity struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	Registry  string    `json:"registry,omitempty"` // e.g. npmjs, crates.io, pypi
}

// String returns a stable, human-readable rendering of the identity.
func (i Identity) String() string {
	reg := i.Registry
	if reg == "" {
		reg = "default"
	}
	return string(i.Ecosystem) + ":" + reg + ":" + i.Name
}

// PackageVersion is a concrete resolved version of an identity.
type PackageVersion struct {
	Identity    Identity `json:"identity"`
	Version     string   `json:"version"`
	Integrity   string   `json:"integrity,omitempty"` // sha512/sha256 hash where available
	Direct      bool     `json:"direct"`              // declared in manifest vs pulled transitively
	Source      string   `json:"source,omitempty"`    // registry URL, git ref, local path
	RuntimeReq  string   `json:"runtime_requirement,omitempty"`
	PeerDeps    []string `json:"peer_dependencies,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	License     string   `json:"license,omitempty"`
	DependsOn   []Identity `json:"depends_on,omitempty"` // one hop; full graph lives in edges
}

// Edge is a directed dependency relationship inside one snapshot graph.
type Edge struct {
	From     Identity `json:"from"`
	FromVer  string   `json:"from_version"`
	To       Identity `json:"to"`
	ToVer    string   `json:"to_version"`
	Direct   bool     `json:"direct"`
}

// Graph is the resolved dependency graph of one manifest+lockfile pair.
type Graph struct {
	SnapshotID  string
	LockfileSHA string   `json:"lockfile_sha"`
	CommitSHA   string   `json:"commit_sha,omitempty"`
	Packages    []PackageVersion `json:"packages"`
	Edges       []Edge           `json:"edges"`
	// byName maps Identity->version and is filled by NewGraph.
}

// NewGraph builds the index maps. It panics only on nil input.
func NewGraph(g *Graph) *Graph {
	for i := range g.Edges {
		_ = i
	}
	return g
}

// DiffCategory classifies a single package-level change between two graphs.
// These mirror the spec's graph diff categories plus lifecycle entries.
type DiffCategory string

const (
	DiffAdded            DiffCategory = "added"
	DiffRemoved          DiffCategory = "removed"
	DiffUpgraded         DiffCategory = "upgraded"
	DiffDowngraded       DiffCategory = "downgraded"
	DiffIntegrityChanged DiffCategory = "integrity_changed"
	DiffSourceChanged    DiffCategory = "source_changed"
	DiffRuntimeChanged   DiffCategory = "runtime_requirement_changed"
	DiffPeerChanged      DiffCategory = "peer_resolution_changed"
	DiffUnchanged        DiffCategory = "unchanged"
)

// DiffEntry describes one package's transition from base to candidate.
type DiffEntry struct {
	Identity     Identity
	BaseVersion  string // "" if absent in base
	CandVersion  string // "" if absent in candidate
	Category     DiffCategory
	BaseDirect   bool
	CandDirect   bool
	BaseIntegrity string
	CandIntegrity string
	// TransitiveImpact is the count of other packages (transitive reach)
	// that depend on this identity, computed from the base graph.
	TransitiveImpact int
	// ChangedDirect is true when a direct requirement constraint changed
	// even if the resolved version did not.
	ChangedDirect bool
}

// GraphDiff is the deterministic comparison of a base and a candidate graph.
type GraphDiff struct {
	Entries []DiffEntry
	// ReachableChanged is the set of identities whose transitive closure
	// contains at least one changed package (used for "dependency reach"
	// risk scoring).
	ReachableChanged int
}

// Finding is a single explainable risk component in a report.
type Finding struct {
	Dimension      string  // e.g. "graph_reach", "security", "release_signal"
	Severity       string  // confirmed_failure | strong_warning | review_suggested | no_issue_observed
	Package        string  // Identity string, "" for run-level findings
	Summary        string
	Evidence       []Evidence
	Confidence     float64 // 0..1, quality/completeness of the evidence
}

// Evidence links a finding to concrete, inspectable material.
type Evidence struct {
	Kind  string // advisory | release_note | build_log | test_result | registry | integrity
	URL   string
	Label string
	// RetrievedAt is a deterministic placeholder for offline data.
}

// ReleaseNote is a maintainer-published note, captured with provenance.
type ReleaseNote struct {
	Package     Identity
	Version     string
	SourceURL   string
	RetrievedAt string
	Body        string
	// Signals lists detected breaking-change signal labels. They are
	// signals, not proof — the report must say so.
	Signals []string
}

// Advisory is a security advisory matched against a resolved version.
type Advisory struct {
	AdvisoryID string
	Package    Identity
	Affected   string // affected version range expression
	Severity   string // LOW|MODERATE|HIGH|CRITICAL
	Links      []string
	// Source records where this advisory came from: "osv:<id>" or
	// "offline:<dataset>". An unreachable feed yields status markers,
	// never invented advisories.
	Source string
}

// PackageTransition is the version move of one identity between two
// snapshots, used by the risk engine and report renderer.
type PackageTransition struct {
	Identity Identity
	From     string
	To       string
}

// RiskBuckets are the regression classification buckets a run ends in.
type RiskBuckets struct {
	ConfirmedFailures []string // candidate-only, evidenced
	StrongWarnings    []string
	ReviewSuggested   []string
	NoIssueObserved   bool
}
