// Version-comparison tests for the semver and PEP 440 code paths.
package graphd

import (
	"testing"

	"github.com/ensembyte/dtm/internal/core"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-rc.1", "1.0.0", -1}, // pre-release < release
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0", "1.0.0+build.5", 0}, // build metadata ignored for ordering
		{"v1.2.3", "1.2.3", 0},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b, core.EcoNode)
		if got != c.want {
			t.Errorf("semver %s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestComparePEP440(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0a1", "1.0.0", -1},
		{"1.0.0b1", "1.0.0rc1", -1},
		{"1.0.0", "1.0.0.post1", -1},
		{"1.0.0", "1.0.0+local", -1},
		{"2.0.0", "1.99.99", 1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b, core.EcoPython)
		if got != c.want {
			t.Errorf("pep440 %s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
