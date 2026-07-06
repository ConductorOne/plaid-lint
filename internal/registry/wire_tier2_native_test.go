// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestGodoxAnalyzer_BasicMatch asserts the library-wrapped godox
// surface emits a diagnostic on a TODO comment with the same stem
// shape the subproc wrapper produced (`TODO(...)` style), so the
// `Message` field carries the keyword + tail.
func TestGodoxAnalyzer_BasicMatch(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

// TODO: fix this later // want ` + "`TODO`" + `
func F() {}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := godoxAnalyzer([]string{"TODO", "FIXME"})
	analysistest.Run(t, dir, a, "a")
}

// TestGodoxAnalyzer_NoFalsePositive asserts comments that don't
// contain a configured keyword do not trigger.
func TestGodoxAnalyzer_NoFalsePositive(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

// regular comment, no keywords
func F() {}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := godoxAnalyzer([]string{"TODO", "FIXME"})
	analysistest.Run(t, dir, a, "a")
}

// TestGocycloAnalyzer_HighComplexity pins the diagnostic message
// format `cyclomatic complexity N of func \`<name>\` is high (> M)`
// matching the subproc wrapper so c1 exclusion rules over the stem
// continue to apply.
func TestGocycloAnalyzer_HighComplexity(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F(x int) int { // want ` + "`cyclomatic complexity .* of func .F. is high \\(> 1\\)`" + `
	if x == 1 {
		return 1
	}
	if x == 2 {
		return 2
	}
	return 0
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	// MinComplexity=2 ensures the small fixture trips.
	a := gocycloAnalyzer(2)
	analysistest.Run(t, dir, a, "a")
}

// TestGocycloAnalyzer_Simple asserts a trivial function below the
// threshold does not fire.
func TestGocycloAnalyzer_Simple(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() int { return 1 }
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := gocycloAnalyzer(30)
	analysistest.Run(t, dir, a, "a")
}

// TestNestifAnalyzer_DeeplyNested asserts the library produces the
// `if ...` has complex nested blocks (complexity: N)` stem the
// subproc wrapper emitted.
func TestNestifAnalyzer_DeeplyNested(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F(a, b, c, d int) int {
	if a > 0 { // want ` + "`has complex nested blocks`" + `
		if b > 0 {
			if c > 0 {
				if d > 0 {
					return 1
				}
			}
		}
	}
	return 0
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := nestifAnalyzer(1)
	analysistest.Run(t, dir, a, "a")
}

// TestNestifAnalyzer_Flat asserts a single-level if does not fire
// even at MinComplexity=1.
func TestNestifAnalyzer_Flat(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F(a int) int {
	if a > 0 {
		return 1
	}
	return 0
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := nestifAnalyzer(5)
	analysistest.Run(t, dir, a, "a")
}

// TestUnconvertAnalyzer_RedundantConversion pins the diagnostic
// stem `unnecessary conversion` against a known-redundant int(int)
// site. unconvert is types-aware; analysistest threads type info
// through pass.TypesInfo so the library finds the conversion.
func TestUnconvertAnalyzer_RedundantConversion(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() int {
	x := 1
	return int(x) // want ` + "`unnecessary conversion`" + `
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unconvertAnalyzer()
	analysistest.Run(t, dir, a, "a")
}

// TestUnconvertAnalyzer_NeededConversion asserts a real conversion
// (int -> int64) does not trip.
func TestUnconvertAnalyzer_NeededConversion(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func F() int64 {
	x := 1
	return int64(x)
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unconvertAnalyzer()
	analysistest.Run(t, dir, a, "a")
}
