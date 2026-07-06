// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"path/filepath"

	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/output"
)

// uriPkgPathMap returns a map from compiled-Go-file URI → owning
// package's PkgPath. Mirrors uriPkgMap (which targets PackageID); kept
// separate so canonicalisation doesn't depend on the L0 partition map's
// types or its "first write wins on ITV collision" policy beyond what's
// needed for filename rewrites.
func uriPkgPathMap(pkgs map[metadata.PackageID]*metadata.Package) map[protocol.DocumentURI]string {
	out := make(map[protocol.DocumentURI]string, len(pkgs)*4)
	for _, mp := range pkgs {
		if mp == nil || mp.PkgPath == "" {
			continue
		}
		for _, uri := range mp.CompiledGoFiles {
			if _, ok := out[uri]; !ok {
				out[uri] = string(mp.PkgPath)
			}
		}
	}
	return out
}

// pkgDirsFor returns a PkgPath → on-disk source directory map. The
// directory is derived from CompiledGoFiles[0] (consistent with
// bench/exclude.go's packageSourceDir); empty when no compiled files
// are present.
//
// Multiple packages may share a PkgPath in the metadata graph (test
// variants, ITVs). First write wins — matches the canonicalisation
// pass which uses the same first-write policy in uriPkgPathMap, so
// reverse-mapping a canonical filename produced by this Run lands in
// the same directory.
func pkgDirsFor(pkgs map[metadata.PackageID]*metadata.Package) map[string]string {
	out := make(map[string]string, len(pkgs))
	for _, mp := range pkgs {
		if mp == nil || mp.PkgPath == "" {
			continue
		}
		key := string(mp.PkgPath)
		if _, ok := out[key]; ok {
			continue
		}
		if len(mp.CompiledGoFiles) > 0 {
			out[key] = filepath.Dir(mp.CompiledGoFiles[0].Path())
			continue
		}
		if len(mp.GoFiles) > 0 {
			out[key] = filepath.Dir(mp.GoFiles[0].Path())
		}
	}
	return out
}

// canonicalizeDiagnostics rewrites every Pos.Filename in diags to its
// canonical form using uriPkg. Diagnostics whose primary Pos.Filename
// isn't in uriPkg fall back to the absolute path (the cgo / synthetic-
// file path). The Related[].Position.Filename and the TextEdit ranges
// inside SuggestedFixes are rewritten via the same lookup.
//
// Inputs are mutated in place; the engine canonicalises freshConverted
// diagnostics right after the exclusion filter has run, so the post-
// filter Pos.Filename values still carry absolute paths when this
// function is called.
func canonicalizeDiagnostics(diags []output.Diagnostic, uriPkg map[protocol.DocumentURI]string) {
	for i := range diags {
		d := &diags[i]
		d.Pos.Filename = canonicalizeName(d.Pos.Filename, uriPkg)
		for j := range d.Related {
			d.Related[j].Position.Filename = canonicalizeName(d.Related[j].Position.Filename, uriPkg)
		}
		for j := range d.SuggestedFixes {
			for k := range d.SuggestedFixes[j].TextEdits {
				te := &d.SuggestedFixes[j].TextEdits[k]
				te.Start.Filename = canonicalizeName(te.Start.Filename, uriPkg)
				te.End.Filename = canonicalizeName(te.End.Filename, uriPkg)
			}
		}
	}
}

// canonicalizeName looks up the owning PkgPath for an absolute file
// path and rewrites it to canonical form. Absolute paths whose URI is
// not in uriPkg (cgo build-cache files, vendored deps reported via
// fact-graph propagation) are returned unchanged — the canonical form
// requires an owning package, and a missing entry is safer to leave
// absolute than to fabricate.
func canonicalizeName(name string, uriPkg map[protocol.DocumentURI]string) string {
	if name == "" {
		return ""
	}
	pkgPath := uriPkg[protocol.URIFromPath(name)]
	if pkgPath == "" {
		return name
	}
	return canonicalpath.Canonicalize(name, pkgPath)
}
