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
	RuntimeReq  string   `json:"runtimeRequirement,omitempty"`
	PeerDeps    []string `json:"peerDependencies,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	License     string   `json:"license,omitempty"`
	DependsOn   []Identity `json:"dependsOn,omitempty"` // one hop; full graph lives in edges
}

// Edge is a directed dependency relationship inside one snapshot graph.
type Edge struct {
	From     Identity `json:"from"`
	FromVer  string   `json:"fromVersion"`
	To       Identity `json:"to"`
	ToVer    string   `json:"toVersion"`
	Direct   bool     `json:"direct"`
}

// Graph is the resolved dependency graph of one manifest+lockfile pair.
type Graph struct {
	SnapshotID  string
	LockfileSHA string   `json:"lockfileSha"`
	CommitSHA   string   `json:"commitSha,omitempty"`
	EcoName     string   `json:"ecosystem"` // raw ecosystem label (node|python|rust)
	Packages    []PackageVersion `json:"packages"`
	Edges       []Edge           `json:"edges"`
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
	Identity      Identity     `json:"identity"`
	BaseVersion   string       `json:"baseVersion"` // "" if absent in base
	CandVersion   string       `json:"candVersion"` // "" if absent in candidate
	Category      DiffCategory `json:"category"`
	BaseDirect    bool         `json:"baseDirect"`
	CandDirect    bool         `json:"candDirect"`
	BaseIntegrity string       `json:"baseIntegrity,omitempty"`
	CandIntegrity string       `json:"candIntegrity,omitempty"`
	// TransitiveImpact is the count of other packages (transitive reach)
	// that depend on this identity, computed from the base graph.
	TransitiveImpact int `json:"transitiveImpact"`
	// ChangedDirect is true when a direct requirement constraint changed
	// even if the resolved version did not.
	ChangedDirect bool `json:"changedDirect"`
}

// GraphDiff is the deterministic comparison of a base and a candidate graph.
type GraphDiff struct {
	Entries []DiffEntry `json:"entries"`
	// ReachableChanged is the set of identities whose transitive closure
	// contains at least one changed package (used for "dependency reach"
	// risk scoring).
	ReachableChanged int `json:"reachableChanged"`
}

// Finding is a single explainable risk component in a report.
type Finding struct {
	// Dimension, e.g. "graph_reach", "security", "release_signal".
	Dimension  string     `json:"dimension"`
	// Severity: confirmed_failure | strong_warning | review_suggested |
	// no_issue_observed.
	Severity   string     `json:"severity"`
	Package    string     `json:"package,omitempty"` // Identity string; "" run-level
	Summary    string     `json:"summary"`
	Evidence   []Evidence `json:"evidence,omitempty"`
	// Confidence is 0..1: quality/completeness of the evidence.
	Confidence float64  `json:"confidence"`
}

// Evidence links a finding to concrete, inspectable material.
// Kind: advisory | release_note | build_log | test_result | registry | integrity | graph.
type Evidence struct {
	Kind  string `json:"kind"`
	URL   string `json:"url,omitempty"`
	Label string `json:"label"`
}

// ReleaseNote is a maintainer-published note, captured with provenance.
type ReleaseNote struct {
	Package Identity `json:"identity"`
	Version string   `json:"version"`
	SourceURL string  `json:"sourceUrl"`
	// RetrievedAt is RFC3339, or "unretrieved" when offline.
	RetrievedAt string   `json:"retrievedAt"`
	Body        string   `json:"body,omitempty"`
	// Signals are detected breaking-change labels: signals, not proof.
	Signals []string `json:"signals,omitempty"`
}

// Advisory is a security advisory matched against a resolved version.
type Advisory struct {
	AdvisoryID string   `json:"advisoryId"`
	Package    Identity `json:"package"`
	// Affected version range expression.
	Affected string   `json:"affected,omitempty"`
	// LOW|MODERATE|HIGH|CRITICAL.
	Severity string   `json:"severity,omitempty"`
	Links    []string `json:"links,omitempty"`
	// Source records provenance: "osv:<id>" or "offline:<dataset>". An
	// unreachable feed yields status markers, never invented advisories.
	Source string `json:"source"`
}

// PackageTransition is the version move of one identity between two
// snapshots, used by the risk engine and report renderer.
type PackageTransition struct {
	Identity Identity `json:"identity"`
	From     string   `json:"from"`
	To       string   `json:"to"`
}

// RiskBuckets are the regression classification buckets a run ends in.
type RiskBuckets struct {
	// Candidate-only, evidenced failures.
	ConfirmedFailures []string `json:"confirmedFailures"`
	StrongWarnings    []string `json:"strongWarnings"`
	ReviewSuggested   []string `json:"reviewSuggested"`
	NoIssueObserved   bool     `json:"noIssueObserved"`
}
