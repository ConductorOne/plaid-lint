// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestWhitespaceAnalyzer_LeadingNewline pins the fork's
// "unnecessary leading newline" diagnostic on the same shape upstream
// `ultraware/whitespace` v0.2.0 emits. Behavior equivalence is the W6
// contract gate for this sub-path.
func TestWhitespaceAnalyzer_LeadingNewline(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() { // want ` + "`unnecessary leading newline`" + `

	x := 1
	_ = x
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := newWhitespaceAnalyzer(wsSettings{})
	analysistest.Run(t, dir, a, "a")
}

// TestWhitespaceAnalyzer_TrailingNewline pins the trailing-newline
// diagnostic message.
func TestWhitespaceAnalyzer_TrailingNewline(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() {
	x := 1
	_ = x

} // want ` + "`unnecessary trailing newline`" + `
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := newWhitespaceAnalyzer(wsSettings{})
	analysistest.Run(t, dir, a, "a")
}

// TestWhitespaceAnalyzer_MultipleFindingsInOneFile is the focused A.1
// test: pin behavior on a file that emits multiple whitespace findings.
// The per-file line cache must produce correct line numbers across
// every fire — the memoization is the regression target.
func TestWhitespaceAnalyzer_MultipleFindingsInOneFile(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() { // want ` + "`unnecessary leading newline`" + `

	x := 1
	_ = x
}

func G() {
	y := 2
	_ = y

} // want ` + "`unnecessary trailing newline`" + `

func H() { // want ` + "`unnecessary leading newline`" + `

	z := 3
	_ = z

} // want ` + "`unnecessary trailing newline`" + `
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := newWhitespaceAnalyzer(wsSettings{})
	analysistest.Run(t, dir, a, "a")
}

// TestWhitespaceAnalyzer_Clean asserts no false positive on a clean file.
func TestWhitespaceAnalyzer_Clean(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() {
	x := 1
	_ = x
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := newWhitespaceAnalyzer(wsSettings{})
	analysistest.Run(t, dir, a, "a")
}

// TestWhitespaceFileCache_LineNumbers asserts the wsFileCache.line
// helper returns line numbers equivalent to go/token's Position.Line.
// The memoized binary search must agree with the canonical lookup for
// every position in the file; otherwise the analyzer would emit
// diagnostics with wrong Pos values.
func TestWhitespaceFileCache_LineNumbers(t *testing.T) {
	src := `package a

func F() {
	x := 1
	_ = x
}

func G() {
	y := 2
	_ = y
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "a.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	tf := fset.File(file.Pos())
	fc := &wsFileCache{base: tf.Base(), lines: tf.Lines()}

	// Walk every node and verify each Pos's line matches Position.Line.
	var allPos []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		allPos = append(allPos, n.Pos(), n.End())
		return true
	})
	if len(allPos) == 0 {
		t.Fatal("no positions collected from AST")
	}
	for _, pos := range allPos {
		want := fset.Position(pos).Line
		got := fc.line(pos)
		if got != want {
			t.Errorf("line(%v) = %d, want %d", pos, got, want)
		}
	}
}
