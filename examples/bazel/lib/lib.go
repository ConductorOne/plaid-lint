// Package lib is the dependency: it exports a printf wrapper (a
// cross-package analysis fact) and a symbol used only from its
// in-package test (the unused supersede case).
package lib

import "fmt"

// Logf is a printf wrapper; the printf analyzer exports a wrapper
// fact about it that dependents consume.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}

// testOnly is referenced only from lib_test.go: a per-target unused
// pass over the library alone would falsely flag it.
func testOnly() int { return 42 }
