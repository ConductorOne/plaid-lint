// Copyright 2026 Anthropic. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts_test

import (
	"encoding/gob"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sync"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/facts"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/typesyncmu"
	"golang.org/x/tools/go/analysis/analysistest"
)

type raceFact struct{ S string }

func (f *raceFact) String() string { return f.S }
func (f *raceFact) AFact()         {}

func init() { gob.Register(new(raceFact)) }

// TestFactsEncode_NoRaceOnConcurrentScope models the race: cascade fanout
// runs many facts.Set.Encode calls while peer goroutines populate *types.Scope
// via gcimporter.importReader.declare → Scope.Insert. The race trips
// "concurrent map read and map write" in Scope.Lookup (called from
// objectpath.For under Set.Encode).
//
// We model the writer as a typesyncmu.Lock-guarded Scope.Insert, since
// gcimporter is the only writer in the running plaid process and our fix
// gates its declare/doDecl through the same typesyncmu lock the encoder uses.
//
// Run under -race; without the typesyncmu coupling the test would trip the
// race detector reliably.
func TestFactsEncode_NoRaceOnConcurrentScope(t *testing.T) {
	files := map[string]string{
		"a/a.go": `package a
type T1 int
type T2 int
type T3 int
type T4 int
type T5 int
type T6 int
type T7 int
type T8 int
`,
		"b/b.go": `package b
import "a"
var V1 a.T1
var V2 a.T2
var V3 a.T3
var V4 a.T4
`,
	}
	dir, cleanup, err := analysistest.WriteFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	pkgA, err := load(t, dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	pkgB, err := load(t, dir, "b")
	if err != nil {
		t.Fatal(err)
	}

	// Build a Set per package and export a fact per package-level object.
	// Encode walks Scope via objectpath.For, which is what trips the race.
	mkSet := func(p *types.Package) *facts.Set {
		set, err := facts.NewDecoderFunc(p, func(string) *types.Package { return nil }).
			Decode(func(string) ([]byte, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range p.Scope().Names() {
			obj := p.Scope().Lookup(name)
			set.ExportObjectFact(obj, &raceFact{S: name})
		}
		return set
	}
	setA := mkSet(pkgA)
	setB := mkSet(pkgB)

	// Concurrent writers (gcimporter-style declare) plus concurrent encoders.
	// All inserts go through typesyncmu.Lock (matching the fix in gcimporter).
	// All encodes acquire typesyncmu.RLock inside facts.Set.Encode.
	// Without the typesyncmu coupling these would race on pkgA.Scope().
	//
	// Simulate the load phase being active so that RLock actually
	// acquires the underlying mu. Outside the load phase, RLock is a no-op
	// (analyze-phase elision); the writer/reader race we want to exercise
	// here is the load-phase race that the lock guards against.
	typesyncmu.EnterLoadPhase()
	defer typesyncmu.ExitLoadPhase()

	const (
		writers     = 4
		encoders    = 8
		iterations  = 200
		insertNames = 6 // names not pre-populated in pkg a's scope
	)

	// Pre-collect names that don't yet exist in pkgA so we can insert/remove
	// without colliding with already-declared identifiers.
	staging := make([]types.Object, insertNames)
	for i := range staging {
		// Use a TypeName so it has a path under objectpath.
		nm := pkgA.Scope().Lookup("T1").(*types.TypeName)
		_ = nm
		staging[i] = types.NewTypeName(0, pkgA, "raceInjected_"+string(rune('A'+i)), nil)
	}

	var wg sync.WaitGroup

	// Writers: lock, insert into pkgA's scope, unlock. Insert is idempotent
	// (Scope.Insert is a no-op if the name is taken). We do not remove —
	// gcimporter doesn't remove either; the population is monotonic per run.
	// To keep the test reentrant across iterations, we hash the iteration into
	// the name. (Names accumulate; harmless for a unit test that runs once.)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				obj := types.NewTypeName(0, pkgA, fakeName(w, i), types.Typ[types.Int])
				typesyncmu.Lock()
				pkgA.Scope().Insert(obj)
				typesyncmu.Unlock()
			}
		}(w)
	}

	// Encoders: call Set.Encode, which RLocks typesyncmu while walking Scope.
	for e := 0; e < encoders; e++ {
		wg.Add(1)
		go func(e int) {
			defer wg.Done()
			set := setA
			if e%2 == 0 {
				set = setB
			}
			for i := 0; i < iterations; i++ {
				_ = set.Encode()
			}
		}(e)
	}

	wg.Wait()
}

// TestCascadeEdit_NoPanic5of5 is a 5-trial harness around
// TestFactsEncode_NoRaceOnConcurrentScope. The c1 cascade-edit repro
// panics 4/5 trials at GOMAXPROCS=8; this test asserts the
// synthetic fixture passes 5/5 under -race. It is a regression gate for the
// typesyncmu coupling.
func TestCascadeEdit_NoPanic5of5(t *testing.T) {
	for trial := 1; trial <= 5; trial++ {
		t.Run("trial", func(t *testing.T) {
			// Re-runs the concurrent-scope harness. If any iteration trips the race
			// detector, the test binary terminates with a non-zero exit and -race
			// captures the offending stack — we don't need to inspect t.Failed()
			// because the runtime panic is fatal.
			TestFactsEncode_NoRaceOnConcurrentScope(t)
		})
	}
}

// TestReadExportData_NoRaceVsBuildirReader covers a further writer site. After
// the earlier fix closed the facts.Encode → objectpath.For panic site, c1
// cascade still tripped buildir → ir.Program.CreatePackage → Scope.Names →
// Scope.Lookup 4/5 trials. The remaining writer lived in
// clcache.ReadExportData → x/tools internal/gcimporter (a different fork
// from internal/gopls/internal/gcimporter, which the prior fix already
// gated): both iimport.go's importReader.declare and ureader.go's per-decl
// loop do obj.Pkg().Scope().Insert without any locking.
//
// The fix wraps the two clcache.ReadExportData call sites in
// internal/gopls/cache/check.go (tryL2Lookup and tryL2WriteThrough) and
// the two clcache.EncodeExportData call sites (the encoder also walks
// Scope.Names / Scope.Lookup via x/tools internal/gcimporter/iexport.go).
//
// This test asserts the locking contract on the decoder writer site
// directly: many concurrent ReadExportData calls (writers, under
// typesyncmu.Lock) interleaved with many buildir-style scope walks
// (readers, under typesyncmu.RLock) do not trip the race detector.
// Run with -race; without the typesyncmu coupling, the test fails.
func TestReadExportData_NoRaceVsBuildirReader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := `package fixture

type T1 int
type T2 int
type T3 int
type T4 int
type T5 int

const C1 = 1
const C2 = 2

func F1() {}
func F2() {}
`
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := &types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("fixture", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	data, err := clcache.EncodeExportData(fset, pkg)
	if err != nil {
		t.Fatalf("EncodeExportData: %v", err)
	}

	// Pre-seed the imports map with the target package: each writer's
	// ReadExportData call will resolve "fixture" to this instance and
	// then insert objects into its Scope — the racy site without the
	// lock. Mirrors how tryL2Lookup / tryL2WriteThrough share
	// b.l2Imports across the batch.
	imports := map[string]*types.Package{
		"fixture": types.NewPackage("fixture", "fixture"),
	}

	// Simulate the load phase being active so that RLock actually
	// acquires the underlying mu. See the rationale in
	// TestFactsEncode_NoRaceOnConcurrentScope.
	typesyncmu.EnterLoadPhase()
	defer typesyncmu.ExitLoadPhase()

	const (
		writers    = 4
		readers    = 8
		iterations = 50
	)

	var (
		mapMu sync.Mutex // mirrors b.l2ImportsMu around the shared imports map
		wg    sync.WaitGroup
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				localFset := token.NewFileSet()
				mapMu.Lock()
				typesyncmu.Lock()
				_, err := clcache.ReadExportData(localFset, imports, "fixture", data)
				typesyncmu.Unlock()
				mapMu.Unlock()
				if err != nil {
					t.Errorf("ReadExportData: %v", err)
					return
				}
			}
		}()
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations*4; i++ {
				rtok := typesyncmu.RLock()
				scope := imports["fixture"].Scope()
				for _, name := range scope.Names() {
					_ = scope.Lookup(name)
				}
				typesyncmu.RUnlock(rtok)
			}
		}()
	}

	wg.Wait()
}

// fakeName returns a unique identifier per (writer, iteration) so that
// Scope.Insert performs an actual map write each time.
func fakeName(writer, iter int) string {
	// Encoded as raceX_Y where X/Y are base-36-ish digits.
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := []byte{'r', 'a', 'c', 'e'}
	for n := writer; ; {
		out = append(out, alphabet[n%36])
		n /= 36
		if n == 0 {
			break
		}
	}
	out = append(out, '_')
	for n := iter; ; {
		out = append(out, alphabet[n%36])
		n /= 36
		if n == 0 {
			break
		}
	}
	return string(out)
}
