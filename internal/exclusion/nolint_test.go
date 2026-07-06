// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/output"
)

// nolintFixture is one .go file written to a temp dir.
type nolintFixture struct {
	name string
	src  string
}

func writeNolintFixture(t *testing.T, f nolintFixture) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, f.name)
	if err := os.WriteFile(path, []byte(f.src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func diagAt(path string, line int, linter string) output.Diagnostic {
	return output.Diagnostic{
		Linter: linter,
		Pos: output.Position{
			Filename: path,
			Line:     line,
		},
	}
}

func TestNolintFilter_TrailingDirective_SuppressesSpecificLinter(t *testing.T) {
	const src = `package x

func f() int32 {
	x := 0
	return int32(x) //nolint:gosec // bounded
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if !n.suppresses(diagAt(path, 5, "gosec")) {
		t.Fatal("expected gosec on line 5 to be suppressed by inline //nolint:gosec")
	}
	if n.suppresses(diagAt(path, 5, "errcheck")) {
		t.Fatal("expected errcheck on line 5 NOT to be suppressed by //nolint:gosec")
	}
	if n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("expected gosec on line 4 NOT to be suppressed (different line)")
	}
}

func TestNolintFilter_FunctionLevelDirective_ExpandsThroughBody(t *testing.T) {
	const src = `package x

//nolint:nonamedreturns // span trace needs named return
func f() (retErr error) {
	defer func() { _ = retErr }()
	return nil
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	// Comment is on line 3, function declaration on line 4. The
	// expanded range should cover the body.
	if !n.suppresses(diagAt(path, 4, "nonamedreturns")) {
		t.Fatal("expected nonamedreturns at func decl line to be suppressed")
	}
	if !n.suppresses(diagAt(path, 5, "nonamedreturns")) {
		t.Fatal("expected nonamedreturns inside body to be suppressed")
	}
}

func TestNolintFilter_BareDirectiveSuppressesAllLinters(t *testing.T) {
	const src = `package x

func f() {
	_ = 1 //nolint
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	for _, lname := range []string{"gosec", "errcheck", "goconst"} {
		if !n.suppresses(diagAt(path, 4, lname)) {
			t.Fatalf("expected %s on line 4 to be suppressed by bare //nolint", lname)
		}
	}
}

func TestNolintFilter_AllSentinelSuppressesAllLinters(t *testing.T) {
	const src = `package x

func f() {
	_ = 1 //nolint:all
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if !n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("expected gosec on line 4 to be suppressed by //nolint:all")
	}
}

func TestNolintFilter_CommaList(t *testing.T) {
	const src = `package x

func f() {
	_ = 1 //nolint:gosec,errcheck // ok
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if !n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("gosec should be in list")
	}
	if !n.suppresses(diagAt(path, 4, "errcheck")) {
		t.Fatal("errcheck should be in list")
	}
	if n.suppresses(diagAt(path, 4, "nonamedreturns")) {
		t.Fatal("nonamedreturns not in list — should NOT be suppressed")
	}
}

func TestNolintFilter_StaticcheckFamilyAlias(t *testing.T) {
	const src = `package x

func f() {
	_ = 1 //nolint:staticcheck // suppress family
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	for _, name := range []string{"SA1019", "ST1000", "QF1001", "S1000"} {
		if !n.suppresses(diagAt(path, 4, name)) {
			t.Fatalf("expected %s to be suppressed via //nolint:staticcheck family alias", name)
		}
	}
	if n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("gosec is not part of staticcheck family — should not be suppressed")
	}
}

func TestNolintFilter_NoDirectiveNoSuppression(t *testing.T) {
	const src = `package x

func f() int { return 1 }
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if n.suppresses(diagAt(path, 3, "gosec")) {
		t.Fatal("no //nolint in file — nothing should be suppressed")
	}
}

func TestNolintFilter_DoesNotMatchNolintlintCommentItself(t *testing.T) {
	// `nolintlint` is the linter's own name as a *prefix* of the
	// comment text — `//nolintlint:...` is NOT a nolint directive.
	const src = `package x

//nolintlint:fake
func f() {}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("//nolintlint:... is not a nolint directive — should not suppress")
	}
}

func TestNolintFilter_MalformedColonNoList(t *testing.T) {
	// `//nolint:` with no linter list is malformed; plaid treats
	// this as "no suppression" (defensive: don't drop diagnostics on
	// broken syntax). nolintlint will warn separately.
	const src = `package x

func f() {
	_ = 1 //nolint:
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	if n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("malformed //nolint: should NOT suppress (defensive)")
	}
}

func TestNolintFilter_NilReceiver(t *testing.T) {
	var n *nolintFilter
	if n.suppresses(diagAt("/tmp/x.go", 1, "gosec")) {
		t.Fatal("nil receiver must not suppress")
	}
}

// TestNolintFilter_GovetFamilyAlias pins the fix: a
// `//nolint:govet` directive must suppress diagnostics emitted by any
// govet sub-analyzer (copylocks, printf, shift, ...). Without the
// alias, c1's `//nolint:govet // Because we need to` directives in
// pkg/controller/app/controller/entitlement_proxy_binding_test.go don't
// cover the copylocks sub-analyzer that fires on the next-line
// MessageState-bearing struct copy.
func TestNolintFilter_GovetFamilyAlias(t *testing.T) {
	const src = `package x

func f() {
	_ = 1 //nolint:govet // suppress family
}
`
	path := writeNolintFixture(t, nolintFixture{name: "f.go", src: src})
	n := newNolintFilter()

	for _, name := range []string{"copylocks", "printf", "shift", "structtag", "unreachable"} {
		if !n.suppresses(diagAt(path, 4, name)) {
			t.Fatalf("expected %s to be suppressed via //nolint:govet family alias", name)
		}
	}
	if n.suppresses(diagAt(path, 4, "gosec")) {
		t.Fatal("gosec is not part of govet family — should not be suppressed")
	}
	if n.suppresses(diagAt(path, 4, "errcheck")) {
		t.Fatal("errcheck is not part of govet family — should not be suppressed")
	}
}
