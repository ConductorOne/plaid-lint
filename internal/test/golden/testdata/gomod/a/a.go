package a

// Stdlib-importing fixture: the gomod golden subtest bumps go.mod's
// `go` directive and asserts the View's reported GoVersion stays
// non-zero, defending against the regression where the
// language-version stub returned 0 and stdlib generics rejected the
// type-check pass.
//
// The fixture uses fmt and strings (the same pair the W9 stdlib_fmt
// regression fixture uses) so generics-bearing transitive stdlib
// packages are exercised.

import (
	"fmt"
	"strings"
)

func Capitalize(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf("%s%s", strings.ToUpper(name[:1]), name[1:])
}
