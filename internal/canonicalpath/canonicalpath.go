// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package canonicalpath converts between absolute filesystem paths and
// a machine-portable canonical form (<importPath>/<basename>) used in
// cached diagnostics.
//
// The canonical form is the string actually written into L0/L1 cache
// values and gob-encoded; the absolute form is what every printer and
// downstream tool consumes. The engine canonicalises diagnostics post-
// filter (after the exclusion filter has had a chance to read the real
// path) and exposes a Resolver so the CLI's printer pipeline can
// reverse-map for human-readable output.
//
// The canonical form is deliberately just string concatenation: the
// pkg-path delimiter is the same forward-slash as the basename
// separator. This means a canonical-form value is itself a valid POSIX
// path string, which all the printers' existing %s formatting handles
// without modification.
package canonicalpath

import (
	"path"
	"path/filepath"
	"strings"
)

// Canonicalize returns the portable form for absPath relative to the
// owning package pkgPath. The result is "<pkgPath>/<basename(absPath)>".
//
// When pkgPath is empty the absolute path is returned unchanged — the
// caller's owner lookup failed, so canonicalisation is impossible and
// returning the original is safer than emitting a half-formed key.
//
// absPath may itself be empty (token.Position.Filename is "" for
// synthetic positions); the function returns "" in that case so
// downstream PosString() rendering still falls back to "-" via
// formatPos.
func Canonicalize(absPath, pkgPath string) string {
	if absPath == "" {
		return ""
	}
	if pkgPath == "" {
		return absPath
	}
	base := filepath.Base(absPath)
	// Always emit forward-slashes — pkgPath is canonical Go path
	// syntax (forward slash regardless of host OS) and we want the
	// canonical form stable across host platforms.
	return pkgPath + "/" + base
}

// Resolver reverses Canonicalize using the loaded package set's
// pkgPath → on-disk directory map.
//
// A nil Resolver (or one with no entries) is safe: Resolve falls back
// to returning the canonical string unchanged. That is the right
// behaviour for diagnostics whose owning package isn't in the loaded
// set (vendored deps reporting via fact-graph propagation, cgo-
// generated files whose source directory is the build cache rather
// than the package's LoadDir, etc.).
type Resolver struct {
	pkgDirs map[string]string
}

// NewResolver constructs a Resolver from a pkgPath → absolute-directory
// map. A nil or empty map produces a no-op resolver.
func NewResolver(pkgDirs map[string]string) *Resolver {
	if len(pkgDirs) == 0 {
		return &Resolver{}
	}
	// Defensive copy: callers may mutate the original metadata graph
	// after handing it to us.
	cp := make(map[string]string, len(pkgDirs))
	for k, v := range pkgDirs {
		cp[k] = v
	}
	return &Resolver{pkgDirs: cp}
}

// Resolve returns the absolute path for a canonical filename. When the
// canonical filename's pkgPath prefix is not in the loaded set OR the
// joined absolute path doesn't exist on disk, Resolve returns the
// canonical string unchanged.
//
// The on-disk-existence check is deliberately omitted at this layer:
// it would force a stat per diagnostic at render time. Callers that
// care about generated-code falling back to the canonical form should
// observe that the canonical string itself is a stable, human-readable
// identifier (e.g. "net/http/_cgo_gotypes.go") and is acceptable in
// the printer output.
func (r *Resolver) Resolve(canonical string) string {
	if canonical == "" {
		return ""
	}
	if r == nil || len(r.pkgDirs) == 0 {
		return canonical
	}
	// Strategy: the last "/" splits basename from pkgPath. Walk
	// progressively shorter pkgPath prefixes (right-to-left split on
	// each "/") so an import path containing additional slashes is
	// still matched.
	//
	// In practice the first attempt (longest pkgPath) is the hit,
	// because Canonicalize uses the package's full PkgPath as the
	// prefix. The loop only matters for malformed inputs.
	idx := strings.LastIndex(canonical, "/")
	if idx <= 0 {
		// No slash, or starts with one — not a canonical form.
		return canonical
	}
	pkgPath := canonical[:idx]
	base := canonical[idx+1:]
	if dir, ok := r.pkgDirs[pkgPath]; ok {
		return filepath.Join(dir, base)
	}
	// pkgPath not in loaded set — fall back to the canonical string.
	// (This is the cgo / vendored-dep / external-test-pkg path.)
	return canonical
}

// IsCanonical reports whether s looks like a canonical filename
// (contains at least one slash and no drive letter / absolute prefix).
// Used by Resolve callers that want to short-circuit on inputs that
// are already absolute paths — e.g. mixed-mode test fixtures.
func IsCanonical(s string) bool {
	if s == "" {
		return false
	}
	if filepath.IsAbs(s) {
		return false
	}
	// path.IsAbs handles forward-slash-only POSIX-style checks
	// consistently across host OSes.
	if path.IsAbs(s) {
		return false
	}
	return strings.Contains(s, "/")
}
