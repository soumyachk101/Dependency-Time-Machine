// Tests for the Rust Cargo.toml + Cargo.lock parser.
package parser

import (
	"testing"
)

const cargoManifest = `
[package]
name = "demo"
version = "0.1.0"

[dependencies]
serde = "1.0"
tokio = { version = "1.35", features = ["full"] }
libc = { git = "https://example.com/libc", rev = "abc123" }

[dev-dependencies]
criterion = "0.5"
`

const cargoLock = `
[[package]]
name = "demo"
version = "0.1.0"
dependencies = ["serde", "tokio"]

[[package]]
name = "serde"
version = "1.0.193"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "abc"
dependencies = ["serde_derive"]

[[package]]
name = "serde_derive"
version = "1.0.193"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "def"

[[package]]
name = "tokio"
version = "1.35.0"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "ghi"

[[package]]
name = "criterion"
version = "0.5.1"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "jjj"
`

func TestParseRust(t *testing.T) {
	g, err := ParseRust([]byte(cargoManifest), []byte(cargoLock))
	if err != nil {
		t.Fatalf("ParseRust: %v", err)
	}
	if g.Ecosystem != "rust" {
		t.Fatalf("ecosystem = %q", g.Ecosystem)
	}
	byName := map[string]PackageOut{}
	for _, p := range g.Packages {
		byName[p.Name] = p
	}
	if !byName["serde"].Direct {
		t.Errorf("serde is a direct [dependencies] entry, want Direct=true")
	}
	if !byName["criterion"].Direct {
		t.Errorf("criterion is a [dev-dependencies] entry, want Direct=true")
	}
	if byName["serde_derive"].Direct {
		t.Errorf("serde_derive is transitive only, want Direct=false")
	}
	if byName["serde"].Source != "crates.io" {
		t.Errorf("serde source = %q, want crates.io", byName["serde"].Source)
	}
	if byName["serde"].Integrity != "abc" {
		t.Errorf("serde checksum = %q", byName["serde"].Integrity)
	}
	// Demo crate itself is in the lock (the workspace root) but not a
	// dependency: it should not be marked direct.
	if byName["demo"].Direct {
		t.Errorf("root crate should not be direct")
	}
	// Sorted by name.
	for i := 1; i < len(g.Packages); i++ {
		if g.Packages[i].Name < g.Packages[i-1].Name {
			t.Errorf("packages not sorted: %v", namesOf(g))
		}
	}
}

func TestParseRustGitSource(t *testing.T) {
	// A git dependency keeps its source label and checksum is empty.
	lock := `
[[package]]
name = "libc"
version = "0.2.150"
source = "git+https://example.com/libc?rev=abc123"
dependencies = []
`
	g, err := ParseRust([]byte("[package]\nname = \"demo\"\n"), []byte(lock))
	if err != nil {
		t.Fatalf("ParseRust: %v", err)
	}
	var libc PackageOut
	for _, p := range g.Packages {
		if p.Name == "libc" {
			libc = p
		}
	}
	if libc.Source == "" {
		t.Errorf("git-sourced libc should carry a source label")
	}
}

func TestParseRustRejectsOversized(t *testing.T) {
	if _, err := ParseRust(make([]byte, MaxInputBytes+1), nil); err == nil {
		t.Errorf("oversized manifest should be rejected")
	}
}

func namesOf(g *GraphOut) []string {
	out := make([]string, 0, len(g.Packages))
	for _, p := range g.Packages {
		out = append(out, p.Name)
	}
	return out
}
