// Package parser, python.go: requirements.txt and a pip-style lockfile
// (requirements lock or pip-compile output with hashes). Direct set comes
// from the requirements file; pinned +hash lines become integrity.
package parser

import (
	"sort"
	"strings"
)

// DetectPython reports the presence of python manifest/lock files.
func DetectPython(files map[string][]byte) (manifest, lock string, ok bool) {
	for _, name := range []string{"requirements.txt", "requirements.lock", "Pipfile", "pyproject.toml", "poetry.lock"} {
		if _, exists := files[name]; exists {
			if manifest == "" {
				manifest = name
			}
			switch name {
			case "requirements.lock", "poetry.lock":
				lock = name
			}
		}
	}
	// A bare requirements.txt counts as manifest+lock combined.
	if manifest == "requirements.txt" && lock == "" {
		lock = "requirements.txt"
	}
	if manifest == "" {
		return "", "", false
	}
	return manifest, lock, true
}

// ParsePython builds a GraphOut from a requirements file. Each line
// "name==version (or ~= / >= / ==)" with optional " --hash=sha256:..."
// becomes a package. Requirements only pin, so the graph is effectively
// flat: edges only exist where a line declares one (rare; left empty for
// MVP accuracy — transitive reach is not resolvable from requirements
// alone, and we refuse to guess).
func ParsePython(req []byte) (*GraphOut, error) {
	if err := guard("requirements", req); err != nil {
		return nil, err
	}
	out := &GraphOut{Ecosystem: "python"}
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(string(req), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		hashes := extractHashes(line)
		spec := strings.TrimSpace(strings.Split(line, "#")[0])
		name, ver, ok := parseRequirementSpec(spec)
		if !ok {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		p := PackageOut{
			Name:       name,
			Version:    ver,
			Direct:     true, // requirements lines are the declared set
			Source:     "pypi",
			DependsOn:  nil,
		}
		// Record the first hash (deterministic; pip-compile emits multiple,
		// sha256 first by convention — keep them all joined for the
		// integrity record so a supply-chain change is detectable).
		p.Integrity = strings.Join(hashes, ",")
		out.Packages = append(out.Packages, p)
	}
	sort.Slice(out.Packages, func(i, j int) bool {
		return out.Packages[i].Name < out.Packages[j].Name
	})
	return out, nil
}

// parseRequirementSpec handles "name==1.2.3", "name~=1.2", "name>=1.2",
// "name[extra]==1.2" and bare "name".
func parseRequirementSpec(spec string) (name, version string, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", false
	}
	// Strip extras: name[extra1,extra2]
	i := strings.IndexByte(spec, '[')
	if i > 0 {
		if j := strings.IndexByte(spec, ']'); j > i {
			spec = spec[:i] + spec[j+1:]
		}
	}
	// Split on the first operator.
	for _, op := range []string{"==", "~=", ">=", "<=", ">", "<", "==="} {
		if idx := strings.Index(spec, op); idx > 0 {
			name = strings.TrimSpace(spec[:idx])
			rest := strings.TrimSpace(spec[idx+len(op):])
			// PEP 440: drop any trailing comma-separated alternatives.
			if c := strings.IndexByte(rest, ','); c >= 0 {
				rest = rest[:c]
			}
			rest = strings.TrimSpace(rest)
			return name, rest, true
		}
	}
	return spec, "", true // unpinned name
}

// extractHashes pulls "sha256:..." tokens out of a line (pip-compile
// format: "--hash=sha256:abc").
func extractHashes(line string) []string {
	var out []string
	rest := line
	for {
		idx := strings.Index(rest, "--hash=")
		if idx < 0 {
			break
		}
		rest = rest[idx+len("--hash="):]
		end := len(rest)
		for _, stop := range []string{" ", "\t", "\n", "#"} {
			if j := strings.IndexByte(rest, rune(stop[0])); j >= 0 && j < end {
				end = j
			}
		}
		h := strings.TrimSpace(rest[:end])
		if h != "" {
			out = append(out, h)
		}
		rest = rest[end:]
	}
	return out
}
