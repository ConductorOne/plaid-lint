// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/parser"
	"go/token"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
	"honnef.co/go/tools/unused"
)

// TestUnusedKey_StructIdentity asserts the struct-valued
// key produces the same identity comparison the prior Sprintf-built
// string produced (filename + line + name uniquely identifies an
// unused.Object's used-set lookup row).
func TestUnusedKey_StructIdentity(t *testing.T) {
	a := unused.Object{Name: "f", Position: token.Position{Filename: "a.go", Line: 3}}
	b := unused.Object{Name: "f", Position: token.Position{Filename: "a.go", Line: 3}}
	c := unused.Object{Name: "g", Position: token.Position{Filename: "a.go", Line: 3}}
	d := unused.Object{Name: "f", Position: token.Position{Filename: "a.go", Line: 4}}
	e := unused.Object{Name: "f", Position: token.Position{Filename: "b.go", Line: 3}}

	if unusedKey(a) != unusedKey(b) {
		t.Errorf("identical (name, file, line) compared unequal")
	}
	if unusedKey(a) == unusedKey(c) {
		t.Errorf("different name compared equal")
	}
	if unusedKey(a) == unusedKey(d) {
		t.Errorf("different line compared equal")
	}
	if unusedKey(a) == unusedKey(e) {
		t.Errorf("different file compared equal")
	}
}

// TestBuildFileIndex_AndLookup asserts the per-pass filename →
// *token.File index returns the same *token.File pointer the prior
// per-finding Iterate would have surfaced. The lookup must agree on
// every file present in the FileSet.
func TestBuildFileIndex_AndLookup(t *testing.T) {
	src1 := "package a\n\nfunc F() {}\n"
	src2 := "package b\n\nfunc G() {}\n"

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "a.go", src1, 0); err != nil {
		t.Fatalf("parse a.go: %v", err)
	}
	if _, err := parser.ParseFile(fset, "b.go", src2, 0); err != nil {
		t.Fatalf("parse b.go: %v", err)
	}

	idx := buildFileIndex(fset)
	if got := len(idx); got != 2 {
		t.Fatalf("fileIdx size = %d, want 2", got)
	}

	// Map every name in the FileSet via Iterate, verify each lands in idx.
	fset.Iterate(func(f *token.File) bool {
		idxFile, ok := idx[f.Name()]
		if !ok {
			t.Errorf("idx missing entry for %s", f.Name())
			return true
		}
		if idxFile != f {
			t.Errorf("idx[%s] = %p, want %p", f.Name(), idxFile, f)
		}
		return true
	})

	if _, ok := idx["nonexistent.go"]; ok {
		t.Errorf("idx had spurious entry for nonexistent.go")
	}
}

// TestUnusedPosFromIndex_NotInPass asserts the index-backed pos
// resolver returns NoPos when the unused.Object's filename is not in
// the pass's FileSet. Matches the prior Iterate-based behavior on
// cached graph data from cross-pass loads.
func TestUnusedPosFromIndex_NotInPass(t *testing.T) {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "a.go", "package a\n", 0); err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := buildFileIndex(fset)

	obj := unused.Object{
		Name: "x",
		Position: token.Position{
			Filename: "absent.go",
			Line:     1,
			Column:   1,
		},
	}
	if got := unusedPosFromIndex(idx, obj); got != token.NoPos {
		t.Errorf("pos for absent file = %v, want NoPos", got)
	}
}

// TestUnusedPosFromIndex_LineOutOfRange asserts we fall back to NoPos
// when the recorded line is beyond the file's last line. Mirrors the
// prior LineCount guard.
func TestUnusedPosFromIndex_LineOutOfRange(t *testing.T) {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "a.go", "package a\n", 0); err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := buildFileIndex(fset)

	obj := unused.Object{
		Name: "x",
		Position: token.Position{
			Filename: "a.go",
			Line:     999,
			Column:   1,
		},
	}
	if got := unusedPosFromIndex(idx, obj); got != token.NoPos {
		t.Errorf("pos for out-of-range line = %v, want NoPos", got)
	}
}

// TestUnusedAnalyzer_MultipleUnusedInOneFile is the focused
// behavior pin: a file with many unused candidates exercises the
// per-pass file index across every result entry. The diagnostic
// stream must match what the prior implementation produced.
func TestUnusedAnalyzer_MultipleUnusedInOneFile(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func unusedFnOne() {} // want ` + "`func unusedFnOne is unused`" + `

func unusedFnTwo() {} // want ` + "`func unusedFnTwo is unused`" + `

func unusedFnThree() {} // want ` + "`func unusedFnThree is unused`" + `

func unusedFnFour() {} // want ` + "`func unusedFnFour is unused`" + `

func unusedFnFive() {} // want ` + "`func unusedFnFive is unused`" + `

func F() {}

var _ = F
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unusedAnalyzer(nil)
	analysistest.Run(t, dir, a, "a")
}
