// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"go/token"

	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

// canonicalizeFileSet returns a clone of fset whose *token.File entries
// carry canonical "<pkgPath>/<basename>" names in place of absolute
// paths. Files whose URI isn't in uriPkg (cgo build cache, vendored
// dep, external-test) are cloned with their original absolute name.
//
// Each cloned file uses the same Base and Size as the original so a
// token.Pos that resolved to (filename, line, col) in fset resolves to
// (canonicalFilename, line, col) in the returned FileSet. Lines and
// AlternativeLineInfo are not preserved here — gcexportdata.Write only
// queries f.Name and (line, col) derived from Base+Size, and Lines is
// reconstructed by the reader via gcexportdata.Read. (If a future
// caller needs Lines on the canonical FileSet, copy oldFile.Lines()
// onto newFile.SetLines.)
//
// Used by l2StoreWithFiles to scrub absolute paths from the
// gcexportdata blob written to L2.
func canonicalizeFileSet(fset *token.FileSet, uriPkg map[protocol.DocumentURI]string) *token.FileSet {
	if fset == nil {
		return nil
	}
	out := token.NewFileSet()
	// Visit files in Base order so AddFile invariants hold.
	type fileInfo struct {
		name  string
		base  int
		size  int
		lines []int
	}
	var infos []fileInfo
	fset.Iterate(func(f *token.File) bool {
		name := f.Name()
		if pkgPath := uriPkg[protocol.URIFromPath(name)]; pkgPath != "" {
			if canon := canonicalpath.Canonicalize(name, pkgPath); canon != name {
				name = canon
			}
		}
		infos = append(infos, fileInfo{
			name:  name,
			base:  f.Base(),
			size:  f.Size(),
			lines: f.Lines(),
		})
		return true
	})
	for _, fi := range infos {
		newF := out.AddFile(fi.name, fi.base, fi.size)
		if len(fi.lines) > 0 {
			// SetLines validates strictly-increasing offsets within
			// (0, size). The source file's table satisfies that
			// invariant by construction; SetLines returning false
			// here would mean the input fset's File was malformed,
			// in which case dropping Lines is the safe fallback.
			newF.SetLines(fi.lines)
		}
	}
	return out
}
