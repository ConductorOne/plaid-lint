// Package app carries seeded findings: a printf-wrapper misuse only
// detectable through lib's exported fact, and an unchecked error.
package app

import (
	"os"

	"example.com/plaidexample/lib"
)

// Use exercises the seeded findings.
func Use() {
	lib.Logf("%d")          // printf: missing argument (via lib's fact)
	os.Mkdir("/tmp/x", 0o777) // errcheck: unchecked error
}
