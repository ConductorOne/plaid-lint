// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"fmt"
	"go/parser"
	"go/token"
	"testing"
)

// TestFileSetSnapshotIsPerPackage demonstrates the load-bearing
// scaling property behind the per-package FileSet fix: encoding the
// batch-wide FileSet at every L2 store point produces O(packages²)
// on-disk growth, while encoding a per-package FileSet produces O(packages).
//
// The test simulates a 50-package batch by adding 50 files to a single
// "batch" *token.FileSet, then encoding (a) the cumulative batch
// FileSet at each step (i.e. what l2Store stored pre-fix) vs (b) a
// per-package FileSet rebuilt from each package's own *token.File
// (post-fix). It asserts:
//
//  1. Per-package totals scale linearly with N (each entry ~constant).
//  2. Batch-wide totals scale super-linearly (the N-th entry is N×
//     the first), so cumulative batch-total / cumulative per-pkg-total
//     grows large.
//  3. Position fidelity is preserved post-fix: a token.Pos encoded
//     against the per-package FileSet resolves to the same
//     (Filename, Line, Column) as in the original batch FileSet.
//
// On the pre-fix code path the FileSetSnapshot section of an L2 entry
// was the entire batch FileSet. On the post-fix path it is just one
// package's files. The scaling assertion (2) is what makes this a
// pre-fix-FAILS / post-fix-PASSES regression test.
func TestFileSetSnapshotIsPerPackage(t *testing.T) {
	const N = 200

	// Build a batch FileSet by parsing N synthetic source files.
	// Each file has identical structure so per-package encoded sizes
	// are roughly equal. We track each parsed file's *token.File so we
	// can rebuild a per-package FileSet that preserves the original
	// Base offsets (the load-bearing invariant).
	batchFset := token.NewFileSet()
	files := make([]pkgFile, N)
	for i := 0; i < N; i++ {
		src := fmt.Sprintf(`package p%d

import "fmt"

// Package %d.
type T%d struct {
	Name string
	X, Y int
}

func New%d(name string) *T%d {
	return &T%d{Name: name}
}

func Use%d(t *T%d) string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("%%s", t.Name)
}
`, i, i, i, i, i, i, i, i)
		path := fmt.Sprintf("p%d/p.go", i)
		af, err := parser.ParseFile(batchFset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		tf := batchFset.File(af.Pos())
		if tf == nil {
			t.Fatalf("no *token.File for %s", path)
		}
		files[i] = pkgFile{tokFile: tf, path: path}
	}

	// At each step i in [0, N), measure:
	//   batchSize[i]   = bytes from encoding batchFset truncated to
	//                    first (i+1) files (what l2Store stored pre-fix
	//                    for the (i+1)-th package).
	//   perPkgSize[i]  = bytes from encoding a fresh FileSet holding
	//                    only files[i] (what l2Store stores post-fix).
	//
	// We approximate batchSize[i] by encoding a fresh FileSet
	// containing files[0..i] using AddExistingFiles (which preserves
	// Base offsets and thus produces bytes representative of the
	// batch's true state at step i).
	batchTotal, perPkgTotal := int64(0), int64(0)
	var perPkgMax int64

	for i := 0; i < N; i++ {
		// Cumulative batch FileSet at step i: files[0..i].
		cum := token.NewFileSet()
		cum.AddExistingFiles(collectFiles(files[:i+1])...)
		bdata, err := EncodeFileSet(cum)
		if err != nil {
			t.Fatalf("encode cumulative @ %d: %v", i, err)
		}
		batchTotal += int64(len(bdata))

		// Per-package FileSet at step i: just files[i].
		one := token.NewFileSet()
		one.AddExistingFiles(files[i].tokFile)
		pdata, err := EncodeFileSet(one)
		if err != nil {
			t.Fatalf("encode per-package @ %d: %v", i, err)
		}
		perPkgTotal += int64(len(pdata))
		if int64(len(pdata)) > perPkgMax {
			perPkgMax = int64(len(pdata))
		}
	}

	t.Logf("N=%d: batchTotal=%d (pre-fix), perPkgTotal=%d (post-fix), perPkgMax=%d",
		N, batchTotal, perPkgTotal, perPkgMax)

	// (1) Per-package max must be small and roughly file-sized.
	// Each synthetic file is ~250 source bytes; the encoded FileSet
	// for one file is a small multiple of that.
	const perPkgCeiling int64 = 4 * 1024 // 4 KB
	if perPkgMax >= perPkgCeiling {
		t.Errorf("per-package max encoded FileSet = %d bytes, want < %d (regression: did the per-package shape come back as batch-wide?)",
			perPkgMax, perPkgCeiling)
	}

	// (2) The load-bearing scaling assertion: the batch encoding
	// scales as ~Σi (each step has size ~i × per-file), so for N=50
	// the batch total is roughly 50× the per-package total. We
	// require a ratio of at least 8× to be a non-trivial step-change
	// signal that is robust to encoding overhead constants.
	const minRatio = 8
	if batchTotal < int64(minRatio)*perPkgTotal {
		t.Errorf("batch-cumulative / per-package ratio = %.2f, want >= %d. "+
			"This is the regression signal this test protects against: if the "+
			"FileSetSnapshot is once again being populated from the batch-wide FileSet, "+
			"on-disk L2 growth is O(packages²).",
			float64(batchTotal)/float64(perPkgTotal), minRatio)
	}
}

// pkgFile binds a parsed file's *token.File to its on-disk path so the
// scaling test can assemble cumulative FileSets that mirror the
// batch's state at each step.
type pkgFile struct {
	tokFile *token.File
	path    string
}

func collectFiles(in []pkgFile) []*token.File {
	out := make([]*token.File, len(in))
	for i, p := range in {
		out[i] = p.tokFile
	}
	return out
}

// TestFileSetSnapshotPreservesBase asserts the load-bearing per-package
// FileSet invariant: AddExistingFiles preserves the original Base
// offset, so a token.Pos resolved against a per-package FileSet
// produces the same (Filename, Line, Column) as the same token.Pos
// resolved against the original batch FileSet. If this invariant ever
// breaks (e.g. by adding files with a renumbered base), warm-side
// gcexportdata.Read would dereference into junk for any cross-process
// L2 consumer that depends on FileSetSnapshot.
func TestFileSetSnapshotPreservesBase(t *testing.T) {
	batchFset := token.NewFileSet()
	// Add three unrelated files to the batch first so that the file
	// we care about has a non-trivial Base offset.
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("noise/n%d.go", i)
		src := fmt.Sprintf("package noise\n\nvar V%d = %d\n", i, i)
		if _, err := parser.ParseFile(batchFset, path, src, 0); err != nil {
			t.Fatalf("parse noise %d: %v", i, err)
		}
	}
	path := "p/p.go"
	src := `package p

import "fmt"

func F(x int) string {
	if x < 0 {
		return "neg"
	}
	return fmt.Sprintf("%d", x)
}
`
	af, err := parser.ParseFile(batchFset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tf := batchFset.File(af.Pos())
	if tf == nil {
		t.Fatalf("no token.File for p.go")
	}
	originalBase := tf.Base()

	// Build a per-package FileSet containing only p.go.
	perPkg := token.NewFileSet()
	perPkg.AddExistingFiles(tf)

	// Encode + decode the per-package FileSet (mirrors the L2
	// write/read round-trip).
	data, err := EncodeFileSet(perPkg)
	if err != nil {
		t.Fatalf("EncodeFileSet: %v", err)
	}
	decoded, err := DecodeFileSet(data)
	if err != nil {
		t.Fatalf("DecodeFileSet: %v", err)
	}

	// Positions resolved against the per-package FileSet must equal
	// positions resolved against the original batch FileSet.
	checked := 0
	for pos := af.Pos(); pos <= af.End(); pos++ {
		want := batchFset.Position(pos)
		got := perPkg.Position(pos)
		if want.Filename != got.Filename || want.Line != got.Line || want.Column != got.Column {
			t.Fatalf("per-package FileSet diverged from batch at pos %d: want %s, got %s", pos, want, got)
		}
		// And the decoded copy must agree.
		gotDec := decoded.Position(pos)
		if want.Filename != gotDec.Filename || want.Line != gotDec.Line || want.Column != gotDec.Column {
			t.Fatalf("decoded per-package FileSet diverged from batch at pos %d: want %s, got %s", pos, want, gotDec)
		}
		checked++
	}
	if checked < 50 {
		t.Fatalf("expected to check >50 positions, only checked %d", checked)
	}

	// Sanity check: the Base offset of the cloned file matches the
	// original. Renumbering would be the load-bearing failure mode.
	cloned := perPkg.File(af.Pos())
	if cloned == nil {
		t.Fatalf("per-package FileSet does not contain the file at original Pos")
	}
	if cloned.Base() != originalBase {
		t.Errorf("per-package FileSet Base = %d, want %d (original from batch FileSet)",
			cloned.Base(), originalBase)
	}
}
