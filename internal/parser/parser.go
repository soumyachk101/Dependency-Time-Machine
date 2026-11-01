// Package parser detects manifest + lockfile pairs for supported
// ecosystems (node npm/pnpm, python, rust) and turns them into a
// core.Graph.
//
// Security note: lockfiles and manifests are untrusted input. All parsers
// enforce a size cap and treat malformed content as an error, never a
// panic (fuzz targets live in parser_test.go).
package parser

import (
	"fmt"
)

// Ecosystem is re-exported for convenience.
type Ecosystem = string

// Parsers keyed by lockfile filename.
type Parser func(name string, data []byte) (*GraphOut, error)

// GraphOut is the parser's output, convertible to core.Graph.
type GraphOut struct {
	Ecosystem string
	Packages  []PackageOut
	Edges     []EdgeOut
}

type PackageOut struct {
	Name       string
	Version    string
	Integrity  string
	Direct     bool
	Source     string
	RuntimeReq string
	DependsOn  []string // ecosystem-scoped "name" strings
}

type EdgeOut struct {
	From      string
	FromVer   string
	To        string
	ToVer     string
	Direct    bool
}

// MaxInputBytes caps any single manifest/lockfile read. Larger files are
// rejected up-front (archive-bomb / DoS guard, per spec §12).
const MaxInputBytes = 8 * 1024 * 1024

// ErrInputTooLarge / ErrUnsupported signals deterministic rejection.
var (
	ErrInputTooLarge = fmt.Errorf("parser: input exceeds %d bytes", MaxInputBytes)
	ErrUnsupported   = fmt.Errorf("parser: unsupported or unrecognised manifest")
)

// guard enforces the size cap.
func guard(name string, data []byte) error {
	if len(data) > MaxInputBytes {
		return ErrInputTooLarge
	}
	return nil
}
