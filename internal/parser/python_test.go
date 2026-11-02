// Tests for the python requirements parser, including PEP 440 spec forms,
// hashes and comment/blank-line handling.
package parser

import (
	"strings"
	"testing"
)

func TestParsePythonBasic(t *testing.T) {
	req := `
# top comment
flask==3.0.0 --hash=sha256:aaaa
requests>=2.31
numpy~=1.26
scipy[accelerate]==1.11.0  # inline comment
`
	g, err := ParsePython([]byte(req))
	if err != nil {
		t.Fatalf("ParsePython: %v", err)
	}
	byName := map[string]PackageOut{}
	for _, p := range g.Packages {
		byName[p.Name] = p
	}
	if byName["flask"].Version != "3.0.0" {
		t.Errorf("flask version = %q", byName["flask"].Version)
	}
	if !strings.Contains(byName["flask"].Integrity, "sha256:aaaa") {
		t.Errorf("flask integrity = %q", byName["flask"].Integrity)
	}
	if byName["requests"].Version != "2.31" {
		t.Errorf("requests version = %q", byName["requests"].Version)
	}
	if byName["numpy"].Version != "1.26" {
		t.Errorf("numpy version = %q", byName["numpy"].Version)
	}
	if byName["scipy"].Version != "1.11.0" {
		t.Errorf("scipy version = %q (extras should be stripped)", byName["scipy"].Version)
	}
	if !byName["flask"].Direct {
		t.Errorf("requirements lines should mark packages direct")
	}
}

func TestParsePythonDedup(t *testing.T) {
	req := "flask==3.0.0\nflask==3.0.0\ndjango==5.0\n"
	g, err := ParsePython([]byte(req))
	if err != nil {
		t.Fatalf("ParsePython: %v", err)
	}
	if len(g.Packages) != 2 {
		t.Errorf("expected deduped to 2 packages, got %d", len(g.Packages))
	}
}

func TestParsePythonEmptyAndMalicious(t *testing.T) {
	if g, err := ParsePython([]byte("")); err != nil || len(g.Packages) != 0 {
		t.Errorf("empty requirements should parse to zero packages, err=%v", err)
	}
	// Oversized input is rejected up-front, no panic.
	if _, err := ParsePython(make([]byte, MaxInputBytes+1)); err == nil {
		t.Errorf("oversized requirements should be rejected")
	}
	// Malicious-ish but still-parseable content: no panic is the bar.
	weird := strings.Repeat("x=", 100000)
	if _, err := ParsePython([]byte(weird)); err != nil {
		// Unparseable lines are skipped, not fatal.
		t.Logf("weird input errored: %v", err)
	}
}

func TestParseRequirementSpec(t *testing.T) {
	cases := []struct{ spec, wantName, wantVer string }{
		{"flask==3.0.0", "flask", "3.0.0"},
		{"numpy~=1.26", "numpy", "1.26"},
		{"requests >= 2.31", "requests", "2.31"},
		{"scipy[accelerate]==1.11.0", "scipy", "1.11.0"},
		{"pkg", "pkg", ""},
		{"pkg==1.0,==2.0", "pkg", "1.0"}, // comma-separated: first wins
	}
	for _, c := range cases {
		n, v, ok := parseRequirementSpec(c.spec)
		if !ok {
			t.Errorf("parseRequirementSpec(%q): not ok", c.spec)
			continue
		}
		if n != c.wantName || v != c.wantVer {
			t.Errorf("parseRequirementSpec(%q) = (%q,%q), want (%q,%q)", c.spec, n, v, c.wantName, c.wantVer)
		}
	}
}
