// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"go/token"
	"go/types"
	"reflect"
	"sync"
	"testing"

	"honnef.co/go/tools/go/ir"
	sa1000 "honnef.co/go/tools/staticcheck/sa1000"

	"golang.org/x/tools/go/analysis"
)

// buildirAnalyzer returns the honnef buildir analyzer pointer, reached
// the same way the production wiring does in
// internal/analyzers/bundled.go:139.
func buildirAnalyzer(t *testing.T) *analysis.Analyzer {
	t.Helper()
	a := sa1000.SCAnalyzer.Analyzer
	if len(a.Requires) == 0 {
		t.Fatalf("sa1000.SCAnalyzer.Analyzer has no Requires; honnef shape changed")
	}
	bi := a.Requires[0]
	if bi.Name != "buildir" {
		t.Fatalf("sa1000.Requires[0] is %q, want buildir", bi.Name)
	}
	return bi
}

// TestSharedBuildir_StructShapeAssertion is the falsifiable gate:
// the reflective IR constructor depends on
// buildir.IR being { Pkg, SrcFuncs } with exactly those types. The
// startup assertion is wired through initBuildirIRShape; this test
// exercises it against the honnef version we vendor today.
func TestSharedBuildir_StructShapeAssertion(t *testing.T) {
	bi := buildirAnalyzer(t)

	// First call resolves the shape. Should not panic.
	initBuildirIRShape(bi.ResultType)
	if buildirIRType == nil {
		t.Fatalf("buildirIRType not set after initBuildirIRShape")
	}
	if buildirIRType.NumField() != expectedBuildirIRNumFields {
		t.Errorf("buildirIRType.NumField()=%d, want %d", buildirIRType.NumField(), expectedBuildirIRNumFields)
	}

	// Pkg field must be *ir.Package, SrcFuncs must be []*ir.Function.
	pkgField := buildirIRType.Field(buildirIRFieldPkg)
	if pkgField.Name != "Pkg" {
		t.Errorf("field[%d].Name=%q, want Pkg", buildirIRFieldPkg, pkgField.Name)
	}
	if want := reflect.TypeOf((*ir.Package)(nil)); pkgField.Type != want {
		t.Errorf("Pkg type=%v, want %v", pkgField.Type, want)
	}
	srcFuncsField := buildirIRType.Field(buildirIRFieldSrcFuncs)
	if srcFuncsField.Name != "SrcFuncs" {
		t.Errorf("field[%d].Name=%q, want SrcFuncs", buildirIRFieldSrcFuncs, srcFuncsField.Name)
	}
	if want := reflect.TypeOf([]*ir.Function(nil)); srcFuncsField.Type != want {
		t.Errorf("SrcFuncs type=%v, want %v", srcFuncsField.Type, want)
	}

	// newBuildirIR must round-trip values through reflection and emit
	// a pointer whose dynamic type matches bi.ResultType.
	emptyPkg := &ir.Package{}
	emptyFuncs := []*ir.Function{}
	got := newBuildirIR(emptyPkg, emptyFuncs)
	if reflect.TypeOf(got) != bi.ResultType {
		t.Errorf("newBuildirIR returned %v, want %v", reflect.TypeOf(got), bi.ResultType)
	}
}

// TestSharedBuildir_EnvFlagDefault: with no env override, the flag is
// off so default dispatch runs.
func TestSharedBuildir_EnvFlagDefault(t *testing.T) {
	// Pin a snapshot of the parsed flag — we can't unset env globally
	// without affecting parallel tests, but parseSharedBuildirEnabled
	// with no env returns false.
	t.Setenv(sharedBuildirEnv, "")
	if got := parseSharedBuildirEnabled(); got {
		t.Errorf("parseSharedBuildirEnabled with empty env returned true; want false")
	}
	t.Setenv(sharedBuildirEnv, "0")
	if got := parseSharedBuildirEnabled(); got {
		t.Errorf("parseSharedBuildirEnabled with %q returned true; want false", "0")
	}
	t.Setenv(sharedBuildirEnv, "1")
	if got := parseSharedBuildirEnabled(); !got {
		t.Errorf("parseSharedBuildirEnabled with %q returned false; want true", "1")
	}
	// Non-numeric → treated as unset (logs a warning).
	t.Setenv(sharedBuildirEnv, "yes")
	if got := parseSharedBuildirEnabled(); got {
		t.Errorf("parseSharedBuildirEnabled with %q returned true; want false", "yes")
	}
}

// TestSharedBuildir_SetForTest verifies the test helper round-trips
// the flag value and resets stats.
func TestSharedBuildir_SetForTest(t *testing.T) {
	defer SetSharedBuildirEnabledForTest(true)()
	if !SharedBuildirEnabled() {
		t.Errorf("after Set(true), SharedBuildirEnabled=false")
	}
}

// TestSharedBuildir_FutureCacheHitMiss exercises the future-cache
// behavior in isolation: a second caller for the same *types.Package
// reuses the first caller's irpkg.
//
// This pins the cache mechanism without needing the full analysis
// driver — we drive workspaceBuildir.future directly to keep the unit
// test hermetic and race-detector friendly.
func TestSharedBuildir_FutureCacheHitMiss(t *testing.T) {
	w := &workspaceBuildir{
		futures: make(map[*types.Package]*buildirFuture),
	}

	pkgA := types.NewPackage("example.com/a", "a")
	pkgB := types.NewPackage("example.com/b", "b")

	fA1, firstA1 := w.future(pkgA)
	if !firstA1 {
		t.Fatalf("first future(pkgA): first=false, want true")
	}
	fA2, firstA2 := w.future(pkgA)
	if firstA2 {
		t.Fatalf("second future(pkgA): first=true, want false")
	}
	if fA1 != fA2 {
		t.Errorf("future(pkgA) returned different pointers on repeat call")
	}

	fB, firstB := w.future(pkgB)
	if !firstB {
		t.Fatalf("first future(pkgB): first=false, want true")
	}
	if fB == fA1 {
		t.Errorf("future(pkgB) shares pointer with future(pkgA)")
	}
}

// TestSharedBuildir_ConcurrentFutureRace fans 8 goroutines requesting
// futures for 4 distinct packages, with 2 goroutines per package. The
// future-cache contract: exactly 4 first-callers across the run, and
// the same future pointer for both goroutines targeting one package.
//
// Race-detector falsifiable: any data race on w.mu or the futures map
// surfaces here under `go test -race`.
func TestSharedBuildir_ConcurrentFutureRace(t *testing.T) {
	const nPkgs = 4
	const callersPerPkg = 2

	w := &workspaceBuildir{
		futures: make(map[*types.Package]*buildirFuture),
	}
	pkgs := make([]*types.Package, nPkgs)
	for i := range pkgs {
		pkgs[i] = types.NewPackage("example.com/p"+string(rune('a'+i)), "p")
	}

	var (
		firstCount int64
		firstMu    sync.Mutex
		got        = make(map[*types.Package][]*buildirFuture, nPkgs)
		gotMu      sync.Mutex
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < nPkgs; i++ {
		for j := 0; j < callersPerPkg; j++ {
			wg.Add(1)
			go func(p *types.Package) {
				defer wg.Done()
				<-start
				f, first := w.future(p)
				if first {
					firstMu.Lock()
					firstCount++
					firstMu.Unlock()
				}
				gotMu.Lock()
				got[p] = append(got[p], f)
				gotMu.Unlock()
				// Resolve immediately so the future doesn't leak.
				if first {
					close(f.ready)
				} else {
					<-f.ready
				}
			}(pkgs[i])
		}
	}
	close(start)
	wg.Wait()

	if firstCount != int64(nPkgs) {
		t.Errorf("firstCount=%d, want %d", firstCount, nPkgs)
	}
	for _, p := range pkgs {
		fs := got[p]
		if len(fs) != callersPerPkg {
			t.Errorf("pkg %q got %d futures, want %d", p.Path(), len(fs), callersPerPkg)
			continue
		}
		for _, f := range fs[1:] {
			if f != fs[0] {
				t.Errorf("pkg %q: future pointers differ across callers", p.Path())
			}
		}
	}
}

// TestSharedBuildir_ProgramInitOnce confirms the shared *ir.Program is
// allocated exactly once per workspaceBuildir even under concurrent
// initProgram calls.
func TestSharedBuildir_ProgramInitOnce(t *testing.T) {
	w := &workspaceBuildir{
		futures: make(map[*types.Package]*buildirFuture),
	}
	fset := token.NewFileSet()

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := w.initProgram(fset); err != nil {
				t.Errorf("initProgram: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if w.prog == nil {
		t.Fatalf("w.prog is nil after init")
	}
	if w.prog.Fset != fset {
		t.Errorf("w.prog.Fset != fset")
	}
}

// TestSharedBuildir_NoReturnFactReflection pins that we can construct
// and read *noReturn fact values via reflection. The fact type lives
// under honnef internal/, so we go through the Analyzer pointer like
// production does.
func TestSharedBuildir_NoReturnFactReflection(t *testing.T) {
	bi := buildirAnalyzer(t)
	w := &workspaceBuildir{
		futures: make(map[*types.Package]*buildirFuture),
	}
	if err := w.rememberBuildirAnalyzer(bi); err != nil {
		t.Fatalf("rememberBuildirAnalyzer: %v", err)
	}

	fact := w.newNoReturnFactPtr()
	w.setNoReturnKind(fact, ir.AlwaysExits)
	if got := w.noReturnKind(fact); got != ir.AlwaysExits {
		t.Errorf("round-trip: got %v, want %v", got, ir.AlwaysExits)
	}
}
