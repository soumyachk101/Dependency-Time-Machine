// Package parser, rust.go: Cargo.toml + Cargo.lock.
//
// Cargo.lock is a TOML array-of-tables:
//
//   [[package]]
//   name = "libc"
//   version = "0.2.0"
//   source = "registry+https://github.com/rust-lang/crates.io-index"
//   checksum = "..."
//
// Direct set comes from [dependencies] / [dev-dependencies] /
// [build-dependencies] in Cargo.toml.
package parser

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

// DetectRust reports the presence of rust manifest/lock files.
func DetectRust(files map[string][]byte) (manifest, lock string, ok bool) {
	if _, exists := files["Cargo.toml"]; exists {
		manifest = "Cargo.toml"
	}
	if _, exists := files["Cargo.lock"]; exists {
		lock = "Cargo.lock"
	}
	return manifest, lock, manifest != ""
}

// CargoManifest is the subset of Cargo.toml the parser needs. The
// dependency tables accept either string ("1.0") or table
// ({version, features, git, ...}) entries — we only read the keys, so
// the value is typed any (BurntSushi rejects a table-typed value for a
// string scalar, but any accepts both shapes).
type CargoManifest struct {
	Project    struct{} `toml:"project"` // may be present, unused
	Package    struct {
		Name    string `toml:"name"`
		Version string `toml:"version"`
		Authors []string `toml:"authors"`
	} `toml:"package"`
	Dependencies     map[string]any `toml:"dependencies"`
	DevDependencies  map[string]any `toml:"dev-dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
	Features         map[string][]string `toml:"features"`
}

// CargoDep documents the table form of a dependency entry
// ({ version, features, git, branch, rev }); the string form is also
// legal in Cargo.toml. The parser only reads dependency names (map
// keys), so this struct is kept for reference and not wired into the
// manifest type.
type CargoDep struct {
	Version string `toml:"version"`
	Path    string `toml:"path"`
	Git     string `toml:"git"`
	Branch  string `toml:"branch"`
	Rev     string `toml:"rev"`
}

// CargoLock mirrors the Cargo.lock array of tables.
type CargoLock struct {
	Packages []CargoLockPkg `toml:"package"`
}

type CargoLockPkg struct {
	Name      string   `toml:"name"`
	Version   string   `toml:"version"`
	Source    string   `toml:"source"`
	Checksum  string   `toml:"checksum"`
	DependsOn []string `toml:"dependencies"`
}

// ParseRust builds a GraphOut from a Cargo.toml + Cargo.lock pair.
func ParseRust(manifestTOML, lockTOML []byte) (*GraphOut, error) {
	if err := guard("manifest", manifestTOML); err != nil {
		return nil, err
	}
	if err := guard("lockfile", lockTOML); err != nil {
		return nil, err
	}
	var man CargoManifest
	if err := toml.Unmarshal(manifestTOML, &man); err != nil {
		return nil, fmt.Errorf("parser: Cargo.toml: %w", err)
	}
	var lock CargoLock
	if err := toml.Unmarshal(lockTOML, &lock); err != nil {
		return nil, fmt.Errorf("parser: Cargo.lock: %w", err)
	}

	direct := map[string]bool{}
	for name := range man.Dependencies {
		direct[name] = true
	}
	for name := range man.DevDependencies {
		direct[name] = true
	}
	for name := range man.BuildDependencies {
		direct[name] = true
	}

	out := &GraphOut{Ecosystem: "rust"}
	// Index package name -> version so we can label edge endpoints.
	byName := map[string]string{}
	for _, p := range lock.Packages {
		byName[p.Name] = p.Version
		out.Packages = append(out.Packages, PackageOut{
			Name:      p.Name,
			Version:   p.Version,
			Integrity: p.Checksum,
			Direct:    direct[p.Name],
			Source:    sourceLabel(p.Source),
			DependsOn: p.DependsOn,
		})
		for _, dep := range p.DependsOn {
			out.Edges = append(out.Edges, EdgeOut{
				From:     p.Name,
				FromVer:  p.Version,
				To:       dep,
				ToVer:    byName[dep],
				Direct:   direct[dep],
			})
		}
	}
	sort.Slice(out.Packages, func(i, j int) bool { return out.Packages[i].Name < out.Packages[j].Name })
	return out, nil
}

// sourceLabel normalises a Cargo source string to a compact label.
func sourceLabel(src string) string {
	switch {
	case src == "":
		return "local"
	case src == "registry+https://github.com/rust-lang/crates.io-index":
		return "crates.io"
	case len(src) >= 9 && src[:9] == "registry+":
		return src[9:]
	case len(src) >= 4 && src[:4] == "git+":
		return "git:" + src[4:]
	case len(src) >= 5 && src[:5] == "path+":
		return "path:" + src[5:]
	}
	return src
}
