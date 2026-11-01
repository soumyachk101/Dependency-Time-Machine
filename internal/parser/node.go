// Package parser, node.go: npm package-lock.json + pnpm pnpm-lock.yaml,
// with package.json providing the direct-dependency set.
package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// DetectNode inspects a map of filename->bytes and reports which node
// manifest/lockfile pair is present. It returns ("", nil, false) when no
// node ecosystem files exist.
func DetectNode(files map[string][]byte) (manifest, lock string, ok bool) {
	for _, m := range []string{"package.json", "pnpm-workspace.yaml"} {
		if _, exists := files[m]; exists && m == "package.json" {
			manifest = m
		}
	}
	switch {
	case len(files["pnpm-lock.yaml"]) > 0:
		lock = "pnpm-lock.yaml"
	case len(files["package-lock.json"]) > 0:
		lock = "package-lock.json"
	case len(files["yarn.lock"]) > 0:
		// yamny.lock: parsed as a best-effort node lockfile is out of scope
		// for MVP; report unsupported rather than guessing.
		lock = "yarn.lock"
	default:
		return "", "", false
	}
	return manifest, lock, true
}

// ParseNode builds a GraphOut from package.json (direct set) plus either
// package-lock.json or pnpm-lock.yaml.
func ParseNode(manifestJSON, lockfile []byte, lockName string) (*GraphOut, error) {
	if err := guard("manifest", manifestJSON); err != nil {
		return nil, err
	}
	if err := guard("lockfile", lockfile); err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(lockName, "pnpm-lock.yaml") ||
		(strings.HasPrefix(string(lockfile), "lockfileVersion:") && bytes.Contains(lockfile, []byte("packages"))):
		return parsePnpm(manifestJSON, lockfile)
	case bytes.TrimSpace(lockfile)[0:1] == "{":
		return parseNPMLock(manifestJSON, lockfile)
	}
	return nil, ErrUnsupported
}

// ---- package.json ----------------------------------------------------------

type npmManifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
	Engines         struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func directFromManifest(manifestJSON []byte) (map[string]bool, string) {
	var m npmManifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return map[string]bool{}, ""
	}
	direct := map[string]bool{}
	for n := range m.Dependencies {
		direct[n] = true
	}
	for n := range m.DevDependencies {
		direct[n] = true
	}
	for n := range m.PeerDependencies {
		direct[n] = true
	}
	return direct, m.Engines.Node
}

// ---- npm package-lock.json -------------------------------------------------

// lockfile v2/v3 shape: packages["node_modules/name"] = {version,
// integrity, dependencies, ...}. v1 shape: dependencies tree. We support
// v2/v3 (the modern default) and v1 best-effort.
type npmLock struct {
	Name          string
	Version       int
	Packages      map[string]npmLockPkg `json:"packages"`
	Dependencies  map[string]npmLockPkg `json:"dependencies"` // v1
}

type npmLockPkg struct {
	Version      string            `json:"version"`
	Integrity    string            `json:"integrity"`
	Resolved     string            `json:"resolved"`
	Dependencies map[string]string `json:"dependencies"`
	Dev          bool              `json:"dev"`
	Optional     bool              `json:"optional"`
	Engines      struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func parseNPMLock(manifestJSON, lockfile []byte) (*GraphOut, error) {
	direct, engine := directFromManifest(manifestJSON)
	var lock npmLock
	if err := json.Unmarshal(lockfile, &lock); err != nil {
		return nil, fmt.Errorf("parser: package-lock.json: %w", err)
	}
	out := &GraphOut{Ecosystem: "node"}
	if lock.Version >= 2 {
		// v2/v3: flat "node_modules/<name>" keys.
		for path, p := range lock.Packages {
			name := strings.TrimPrefix(path, "node_modules/")
			// Scoped names keep their "scope/name" form inside node_modules/.
			if p.Version == "" && p.Resolved == "" {
				continue // workspace-root entry or virtual
			}
			kind := "registry"
			if strings.HasPrefix(p.Resolved, "git") || strings.HasPrefix(p.Resolved, "file:") {
				kind = p.Resolved
			}
			out.Packages = append(out.Packages, PackageOut{
				Name:       name,
				Version:    p.Version,
				Integrity:  p.Integrity,
				Direct:     direct[name] && !p.Dev,
				Source:     kind,
				RuntimeReq: p.Engines.Node,
				DependsOn:  keys(p.Dependencies),
			})
			fromVer := "root"
			if path == "" {
				fromVer = "root"
			}
			for dep := range p.Dependencies {
				// Best-effort: resolved version of the dep is looked up below.
				_ = dep
			}
			out.Edges = append(out.Edges, buildEdges(path, fromVer, p)...)
		}
	} else {
		// v1: nested dependencies — walk recursively.
		var walk func(name, ver string, deps map[string]npmLockPkg, depth int)
		walk = func(name, ver string, deps map[string]npmLockPkg, depth int) {
			for dn, dp := range deps {
				verS := dp.Version
				if verS == "" {
					verS = "0.0.0"
				}
				out.Packages = append(out.Packages, PackageOut{
					Name:       dn,
					Version:    verS,
					Integrity:  dp.Integrity,
					Direct:     direct[dn] && depth == 0,
					Source:     "registry",
					DependsOn:  keys(dp.Dependencies),
				})
				out.Edges = append(out.Edges, EdgeOut{
					From: name, FromVer: ver, To: dn, ToVer: verS, Direct: depth == 0 && direct[dn],
				})
				if dp.Dependencies != nil {
					walk(dn, verS, dp.Dependencies, depth+1)
				}
			}
		}
		walk("(root)", "0.0.0", lock.Dependencies, 0)
	}
	_ = engine
	return dedupeGraph(out), nil
}

// buildEdges emits edges from one v2/v3 lock entry to its dependencies.
func buildEdges(path, fromVer string, p npmLockPkg) []EdgeOut {
	if len(p.Dependencies) == 0 {
		return nil
	}
	// In v2/v3, the path "node_modules/a" depends on packages that live at
	// "node_modules/a/node_modules/b" (nested) or "node_modules/b" (hoisted).
	// We emit edges by name only; the resolver's graph builder joins on
	// name, which is correct for hoisted layouts.
	fromName := strings.TrimPrefix(path, "node_modules/")
	if fromName == "" {
		fromName = "(root)"
	}
	edges := make([]EdgeOut, 0, len(p.Dependencies))
	for dep, ver := range p.Dependencies {
		edges = append(edges, EdgeOut{From: fromName, FromVer: fromVer, To: dep, ToVer: ver})
	}
	return edges
}

// ---- pnpm pnpm-lock.yaml ---------------------------------------------------

// pnpm-lock.yaml v6 shape (lockfileVersion: "6.0"):
//
//   packages:
//     /name@version:
//       resolution: {integrity: sha512-...}
//       dependencies:
//         dep: "1.0.0"
//
// We parse the flat "packages" map; nested "/a@1/node_modules/b@2" paths
// (pnpm's isolated layout) keep the package name in the segment after the
// last "node_modules/".
type pnpmLock struct {
	LockfileVersion string            `yaml:"lockfileVersion"`
	Packages        map[string]pnpmPkg `yaml:"packages"`
}

type pnpmPkg struct {
	Resolution struct {
		Integrity string `yaml:"integrity"`
	} `yaml:"resolution"`
	Dependencies map[string]string `yaml:"dependencies"`
	Dev          bool              `yaml:"dev"`
	Engines      struct {
		Node string `yaml:"node"`
	} `yaml:"engines"`
}

func parsePnpm(manifestJSON, lockfile []byte) (*GraphOut, error) {
	direct, _ := directFromManifest(manifestJSON)
	var lock pnpmLock
	if err := yaml.Unmarshal(lockfile, &lock); err != nil {
		return nil, fmt.Errorf("parser: pnpm-lock.yaml: %w", err)
	}
	out := &GraphOut{Ecosystem: "node"}
	for path, p := range lock.Packages {
		// path is "/name@version" or "/a@1/node_modules/b@2".
		name, ver := splitPnpmPath(path)
		if name == "" {
			continue
		}
		out.Packages = append(out.Packages, PackageOut{
			Name:       name,
			Version:    ver,
			Integrity:  p.Resolution.Integrity,
			Direct:     direct[name] && !strings.Contains(path, "node_modules/"),
			Source:     "registry",
			RuntimeReq: p.Engines.Node,
			DependsOn:  keys(p.Dependencies),
		})
		for dep, depVer := range p.Dependencies {
			out.Edges = append(out.Edges, EdgeOut{From: name, FromVer: ver, To: dep, ToVer: depVer, Direct: !strings.Contains(path, "node_modules/")})
		}
	}
	return dedupeGraph(out), nil
}

// splitPnpmPath extracts (name, version) from a pnpm packages key.
func splitPnpmPath(path string) (string, string) {
	seg := path
	if i := strings.LastIndex(seg, "node_modules/"); i >= 0 {
		seg = seg[i+len("node_modules/"):]
	}
	seg = strings.TrimPrefix(seg, "/")
	at := strings.LastIndex(seg, "@")
	if at <= 0 {
		return "", ""
	}
	name, ver := seg[:at], seg[at+1:]
	if name == "" || ver == "" {
		// Could be a scoped name like "@scope/name@1.0.0" — at is the
		// LAST '@', which is correct; scoped names keep their prefix.
		return "", ""
	}
	return name, ver
}

// ---- shared helpers ----------------------------------------------------------

func keys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// dedupeGraph collapses duplicate package entries (nested layouts can list
// the same name@version twice) keeping the first occurrence, and returns
// the graph with packages sorted by name for determinism.
func dedupeGraph(g *GraphOut) *GraphOut {
	seen := map[string]bool{}
	pkgs := make([]PackageOut, 0, len(g.Packages))
	for _, p := range g.Packages {
		k := p.Name + "@" + p.Version
		if seen[k] {
			continue
		}
		seen[k] = true
		pkgs = append(pkgs, p)
	}
	g.Packages = pkgs
	// Deterministic ordering by name.
	for i := 1; i < len(pkgs); i++ {
		for j := i; j > 0 && pkgs[j].Name < pkgs[j-1].Name; j-- {
			pkgs[j-1], pkgs[j] = pkgs[j], pkgs[j-1]
		}
	}
	return g
}
