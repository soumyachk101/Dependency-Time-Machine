// Tests for the node (npm/pnpm) parsers, using golden lockfile fixtures
// that exercise both lockfile v3 and pnpm v6 shapes plus edge cases.
package parser

import (
	"testing"
)

const npmManifest = `{
  "name": "demo",
  "dependencies": {
    "left-pad": "1.3.0",
    "express": "4.18.2"
  },
  "devDependencies": {
    "typescript": "5.3.0"
  }
}`

// v3 lockfile: two packages, one nested override.
const npmLockV3 = `{
  "name": "demo",
  "lockfileVersion": 3,
  "packages": {
    "node_modules/left-pad": {
      "version": "1.3.0",
      "integrity": "sha512-AAAA",
      "resolved": "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"
    },
    "node_modules/left-pad/node_modules/is-odd": {
      "version": "2.0.0",
      "integrity": "sha512-BBBB",
      "resolved": "https://registry.npmjs.org/is-odd/-/is-odd-2.0.0.tgz",
      "dependencies": { "is-number": "^3.0.0" }
    },
    "node_modules/express": {
      "version": "4.18.2",
      "integrity": "sha512-CCCC",
      "dependencies": { "left-pad": "1.3.0" }
    }
  }
}`

func TestParseNPMLockV3(t *testing.T) {
	g, err := ParseNode([]byte(npmManifest), []byte(npmLockV3), "package-lock.json")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}
	if g.Ecosystem != "node" {
		t.Fatalf("ecosystem = %q", g.Ecosystem)
	}
	byName := map[string]PackageOut{}
	for _, p := range g.Packages {
		byName[p.Name+"@"+p.Version] = p
	}
	if _, ok := byName["left-pad@1.3.0"]; !ok {
		t.Errorf("missing left-pad@1.3.0 in %v", keysOf(g))
	}
	lp := byName["left-pad@1.3.0"]
	if !lp.Direct {
		t.Errorf("left-pad should be direct (declared in manifest)")
	}
	if lp.Integrity != "sha512-AAAA" {
		t.Errorf("integrity = %q", lp.Integrity)
	}
	ts := byName["typescript@5.3.0"]
	if ts.Version != "" {
		t.Errorf("dev dep typescript absent from lockfile; got %v", ts)
	}
}

func TestParseNPMLockV1(t *testing.T) {
	lock := `{
  "name": "demo",
  "lockfileVersion": 1,
  "dependencies": {
    "left-pad": {
      "version": "1.3.0",
      "dependencies": { "is-odd": { "version": "2.0.0" } }
    }
  }
}`
	g, err := ParseNode([]byte(npmManifest), []byte(lock), "package-lock.json")
	if err != nil {
		t.Fatalf("ParseNode v1: %v", err)
	}
	if len(g.Packages) != 2 {
		t.Errorf("expected 2 packages (left-pad + transitive is-odd), got %d", len(g.Packages))
	}
	// Determinism: package list sorted by name.
	for i := 1; i < len(g.Packages); i++ {
		if g.Packages[i].Name < g.Packages[i-1].Name {
			t.Errorf("packages not sorted: %v", keysOf(g))
		}
	}
}

func TestParsePnpm(t *testing.T) {
	lock := `lockfileVersion: "6.0"

packages:

  /left-pad@1.3.0:
    resolution: {integrity: sha512-AAAA}
    dependencies:
      is-odd: 2.0.0

  /is-odd@2.0.0:
    resolution: {integrity: sha512-BBBB}

  /express@4.18.2:
    resolution: {integrity: sha512-CCCC}
    dependencies:
      left-pad: 1.3.0
`
	g, err := ParseNode([]byte(npmManifest), []byte(lock), "pnpm-lock.yaml")
	if err != nil {
		t.Fatalf("ParseNode pnpm: %v", err)
	}
	byName := map[string]PackageOut{}
	for _, p := range g.Packages {
		byName[p.Name] = p
	}
	if byName["left-pad"].Version != "1.3.0" {
		t.Errorf("left-pad version = %q", byName["left-pad"].Version)
	}
	if !byName["left-pad"].Direct {
		t.Errorf("left-pad should be direct in pnpm layout")
	}
	if byName["left-pad"].Integrity != "sha512-AAAA" {
		t.Errorf("left-pad integrity = %q", byName["left-pad"].Integrity)
	}
	if byName["express"].Version != "4.18.2" {
		t.Errorf("express version = %q", byName["express"].Version)
	}
}

// Malicious manifest handling: oversized input is rejected up-front,
// malformed JSON returns an error rather than panicking.
func TestParseNodeSecurity(t *testing.T) {
	if _, err := ParseNode(make([]byte, MaxInputBytes+1), nil, "package-lock.json"); err == nil {
		t.Errorf("oversized manifest should be rejected")
	}
	if _, err := ParseNode([]byte("{not json"), []byte("{}"), "package-lock.json"); err == nil {
		t.Errorf("malformed manifest should produce an error")
	}
}

func TestSplitPnpmPath(t *testing.T) {
	cases := []struct {
		in, name, ver string
	}{
		{"/left-pad@1.3.0", "left-pad", "1.3.0"},
		{"/@scope/pkg@2.0.0", "@scope/pkg", "2.0.0"},
		{"/express@4.18.2/node_modules/left-pad@1.3.0", "left-pad", "1.3.0"},
	}
	for _, c := range cases {
		n, v := splitPnpmPath(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("splitPnpmPath(%q) = (%q,%q), want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
}

func keysOf(g *GraphOut) []string {
	out := make([]string, 0, len(g.Packages))
	for _, p := range g.Packages {
		out = append(out, p.Name+"@"+p.Version)
	}
	return out
}
