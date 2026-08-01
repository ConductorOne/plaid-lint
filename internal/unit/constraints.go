// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// filterByConstraints splits the declared sources into the files that
// build under the configured {GOOS, GOARCH, tags} and those excluded
// by build constraints, mirroring the rules_go compile builder's
// filterAndSplitFiles: a GoArchive declares EVERY source and the
// compile action selects the matching subset at action time, so the
// unit driver must apply the same selection or it type-checks
// mutually exclusive files together (e.g. an _arm64.go/_noasm.go pair
// redeclaring the same function).
//
// Constraint evaluation is the toolchain's own: build.Context.MatchFile
// honors both filename-implied constraints (_GOOS.go / _GOARCH.go /
// _GOOS_GOARCH.go suffixes, including the "only when the suffix names
// a known OS/arch" rule and the leading-underscore/dot rejection) and
// //go:build / // +build directives. MatchFile reads only the named
// file — no GOROOT/GOPATH/dir walking — so it stays inside the
// hermeticity contract.
func filterByConstraints(pkg *PackageConfig) (matched, excluded []string, err error) {
	bctx := build.Context{
		GOOS:      pkg.GOOS,
		GOARCH:    pkg.GOARCH,
		BuildTags: pkg.Tags,
		// CgoEnabled mirrors the compile action's CGO_ENABLED (the
		// aspect derives it from rules_go's pure mode). MatchFile only
		// evaluates constraints — the builder's additional "exclude
		// files importing C when cgo is off" rule is mirrored below.
		// Cgo-ENABLED archives remain unsupported for analysis (the
		// compiler consumes cgo-generated sources, not the originals;
		// deferred-work ledger D1): with cgo on, import-"C" files are
		// kept and fail type-check, which reports the package as
		// does-not-compile rather than analyzing a wrong file subset.
		Compiler:    "gc",
		CgoEnabled:  pkg.Cgo,
		ReleaseTags: releaseTags(pkg.GoVersion),
	}
	for _, name := range pkg.GoFiles {
		dir, base := filepath.Split(name)
		ok, err := bctx.MatchFile(dir, base)
		if err != nil {
			return nil, nil, fmt.Errorf("unit: source %s: %w", name, err)
		}
		if ok && !pkg.Cgo {
			ok, err = notCgoFile(name)
			if err != nil {
				return nil, nil, fmt.Errorf("unit: source %s: %w", name, err)
			}
		}
		if ok {
			matched = append(matched, name)
		} else {
			excluded = append(excluded, name)
		}
	}
	return matched, excluded, nil
}

// notCgoFile reports whether the file does NOT import "C" — with cgo
// disabled the compile builder drops cgo files after constraint
// matching (rules_go filter.go: matched && (CgoEnabled || !isCgo)),
// and the driver must make the same selection. ImportsOnly parsing
// reads just the named file, preserving hermeticity.
func notCgoFile(name string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
	if err != nil {
		// Syntactically broken files stay in the matched set so the
		// type-checker reports them; constraint filtering only decides
		// selection, never validity.
		return true, nil //nolint:nilerr // deliberate: defer to the type-checker
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"C"` {
			return false, nil
		}
	}
	return true, nil
}

// releaseTags derives the go1.1..go1.N release tags implied by the
// configured language version ("1.26" → go1.1..go1.26), so
// `//go:build go1.X` constraints evaluate the way the configured
// toolchain would. Prerelease SDK versions ("1.26rc1", "1.26beta2")
// carry their release tag like the final version does, so the minor
// is read as the leading digit run ("26rc1" → 26) — exactly how the
// toolchain treats prereleases. When no version is declared, this
// binary's own toolchain tags apply (compiled-in; no environment
// access).
func releaseTags(goVersion string) []string {
	minor, ok := strings.CutPrefix(goVersion, "1.")
	if !ok {
		return build.Default.ReleaseTags
	}
	end := 0
	for end < len(minor) && minor[end] >= '0' && minor[end] <= '9' {
		end++
	}
	minor = minor[:end]
	n, err := strconv.Atoi(minor)
	if err != nil || n < 1 {
		return build.Default.ReleaseTags
	}
	tags := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		tags = append(tags, "go1."+strconv.Itoa(i))
	}
	return tags
}
