// Package a is a deliberately-noisy fixture used by W9 to verify
// that the gopls fork's stdlib type-check path is complete enough to
// run SA-* analyzers on workspace packages that import the stdlib.
//
// The body of this file is dead simple but imports fmt and strings,
// the two transitive-stdlib pulls that empirically tripped the
// pre-fix language-version stub.
package a

import (
	"fmt"
	"strings"
)

// Capitalize returns name with its first byte uppercased. It uses fmt
// and strings so the package's transitive import set covers both
// stdlib leaves the W9 fixture cares about.
func Capitalize(name string) string {
	if name == "" {
		return ""
	}
	upper := strings.ToUpper(name[:1])
	return fmt.Sprintf("%s%s", upper, name[1:])
}

// Identity is a pure function (no side effects, no panics) that
// SA4017's fact_purity prereq recognises. The plaid-lint W9
// fixture uses it to confirm fact-bearing intermediate analyzers
// run on a stdlib-importing workspace package.
func Identity(s string) string { return s }
