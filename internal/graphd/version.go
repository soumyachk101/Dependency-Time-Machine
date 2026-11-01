// Package graphd, file version.go: ecosystem-aware version comparison.
//
// compareVersions returns -1/0/+1 for a < b == a > b, using the version
// scheme of the ecosystem the package belongs to:
//   - node/rust: semver (with pre-release ordering)
//   - python:    PEP 440 (subset: release + pre-release + post + local)
//
// Unknown formats fall back to lexicographic-last-resort comparison that
// still yields a total order — determinism matters more than perfect
// semantic precision for a version we can't parse.
package graphd

import (
	"strconv"
	"strings"

	"github.com/ensembyte/dtm/internal/core"
)

// compareVersions orders two version strings for the given ecosystem.
func compareVersions(a, b string, eco core.Ecosystem) int {
	switch eco {
	case core.EcoPython:
		return comparePEP440(a, b)
	default: // node, rust both use semver
		return compareSemver(a, b)
	}
}

// numPart is one dot-separated numeric component.
type numPart []int

// splitVersion splits "1.2.3-rc.1" into ([1,2,3], "rc.1").
func splitVersion(v string) (numPart, string) {
	v = strings.TrimPrefix(v, "v")
	pre := ""
	if i := strings.Index(v, "-"); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	if i := strings.Index(v, "+"); i >= 0 { // build metadata ignored for ordering
		v = v[:i]
	}
	parts := make([]int, 0, 4)
	for _, s := range strings.Split(v, ".") {
		if s == "" {
			break
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			// Non-numeric component: stop the numeric walk; the remainder is
			// carried into the pre-release tail for total ordering.
			pre = strings.Join(append([]string{s}, strings.Split(pre, ".")...), ".")
			break
		}
		parts = append(parts, n)
	}
	return parts, pre
}

// compareSemver implements semver ordering: major.minor.patch numeric,
// then pre-release < release, lexicographic pre-release tags, build
// metadata ignored.
func compareSemver(a, b string) int {
	ap, apre := splitVersion(a)
	bp, bpre := splitVersion(b)
	// Pad to equal length with zeros.
	n := len(ap)
	if len(bp) > n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(ap) {
			x = ap[i]
		}
		if i < len(bp) {
			y = bp[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if apre == bpre {
		return 0
	}
	if apre == "" {
		return 1 // release > pre-release
	}
	if bpre == "" {
		return -1
	}
	if c := strings.Compare(apre, bpre); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return 0
}

// comparePEP440 covers the common release strings: "1.2.3",
// "1.2.3rc1", "1.2.3.post1", "1.2.3+local". Ordering: release < pre <
// release+pre? No — PEP440: X.Y.ZaN < X.Y.ZbN < X.Y.ZrcN < X.Y.Z <
// X.Y.Z.postN < X.Y.Z+local.
func comparePEP440(a, b string) int {
	type rec struct {
		rel []int
		pre string // "a", "b", "rc" or ""
		preN int
		post int
		local string
	}
	parse := func(v string) rec {
		v = strings.TrimSpace(v)
		r := rec{}
		if i := strings.Index(v, "+"); i >= 0 {
			r.local = v[i+1:]
			v = v[:i]
		}
		if i := strings.Index(v, ".post"); i >= 0 {
			n, _ := strconv.Atoi(v[i+len(".post"):])
			r.post = n
			v = v[:i]
		}
		for _, tag := range []string{"rc", "a", "b"} {
			if i := strings.Index(v, tag); i >= 0 {
				r.pre = tag
				n, _ := strconv.Atoi(v[i+len(tag):])
				r.preN = n
				v = v[:i]
				break
			}
		}
		for _, s := range strings.Split(v, ".") {
			if s == "" {
				break
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				break
			}
			r.rel = append(r.rel, n)
		}
		return r
	}
	ra, rb := parse(a), parse(b)
	// Release tuples.
	n := len(ra.rel)
	if len(rb.rel) > n {
		n = len(rb.rel)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(ra.rel) {
			x = ra.rel[i]
		}
		if i < len(rb.rel) {
			y = rb.rel[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	// Pre-release rank: none > rc > b > a (lower tag = earlier).
	preRank := func(r rec) (int, int) {
		if r.pre == "" {
			return 4, 0
		}
		switch r.pre {
		case "a":
			return 1, r.preN
		case "b":
			return 2, r.preN
		default:
			return 3, r.preN
		}
	}
	pra, praN := preRank(ra)
	prb, prbN := preRank(rb)
	if pra != prb {
		if pra < prb {
			return -1
		}
		return 1
	}
	if praN != prbN {
		if praN < prbN {
			return -1
		}
		return 1
	}
	if ra.post != rb.post {
		if ra.post < rb.post {
			return -1
		}
		return 1
	}
	if ra.local != rb.local {
		if ra.local == "" {
			// PEP440: no local < local
			return -1
		}
		if rb.local == "" {
			return 1
		}
		if c := strings.Compare(ra.local, rb.local); c != 0 {
			if c < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}
