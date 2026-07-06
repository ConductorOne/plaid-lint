// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestUnusedAnalyzer_UnusedFunc pins the diagnostic stem
// `func <name> is unused` against an unexported function that is
// never referenced. Mirrors the subproc wrapper's canonicalized
// `func F is unused` emission so c1 exclusion rules continue to
// apply.
func TestUnusedAnalyzer_UnusedFunc(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func unusedFn() {} // want ` + "`func unusedFn is unused`" + `

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

// TestUnusedAnalyzer_UsedFunc asserts that a function referenced
// elsewhere in the same package is not flagged. Also confirms the
// exported-is-used branch (capital F).
func TestUnusedAnalyzer_UsedFunc(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func helper() int { return 1 }

func F() int { return helper() }
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unusedAnalyzer(nil)
	analysistest.Run(t, dir, a, "a")
}

// TestUnparamAnalyzer_UnusedParam pins unparam's message format
// against a function parameter that is never read in the body
// despite the function being reachable. The message stem
// `<func> - <param> is unused` matches upstream unparam's
// emission and what the subproc wrapper canonicalized.
func TestUnparamAnalyzer_UnusedParam(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

var DoWork func()
var Sink interface{}

func f(a, b int) int { // want ` + "`b is unused`" + `
	DoWork()
	return a + 1
}

func G() {
	Sink = f(1, 2)
	Sink = f(3, 4)
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unparamAnalyzer(false)
	analysistest.Run(t, dir, a, "a")
}

// TestUnparamAnalyzer_UsedParam asserts that a function whose
// parameter IS read does not fire.
func TestUnparamAnalyzer_UsedParam(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

func f(x int) int {
	return x + 1
}

func g() int {
	return f(42)
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	a := unparamAnalyzer(false)
	analysistest.Run(t, dir, a, "a")
}
