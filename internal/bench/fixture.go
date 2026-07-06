// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FixtureShape describes a synthetic benchmark module's package
// topology. The generator (see [GenerateFixture]) materialises a
// go module on disk matching the shape. Stdlib imports are
// intentionally omitted: the gopls fork's stdlib closure varies
// across Go versions, which adds noise to benchmark comparisons.
//
// Three named presets cover the documented W10 shapes:
//
//   - [SmallShape]: 5-10 packages, depth 2, leaf+root mix.
//   - [MediumShape]: 30-50 packages, depth 3-4, fan-out at one
//     layer (used as a stand-in for "real workspace" while still
//     finishing in single-digit seconds).
//   - [CascadeShape]: a mid-graph package with ~10 dependents, so
//     touching it produces a measurable cascade-tier signal.
type FixtureShape struct {
	// Name is the module name (used in go.mod and in generated
	// import paths). Stable across regenerations so the harness's
	// L1/L2 cache keys are reproducible.
	Name string

	// Module is the go.mod's module path (e.g. "example.com/bench/small").
	Module string

	// NumLeaves is the count of leaf packages (no imports).
	NumLeaves int

	// NumMidLayer is the count of mid-layer packages, each of
	// which imports a non-empty subset of the leaves.
	NumMidLayer int

	// NumRoots is the count of root packages, each of which
	// imports a subset of the mid-layer packages.
	NumRoots int

	// CascadeMidPkg is the package the cascade scenario edits.
	// Empty means the harness picks an arbitrary mid-layer
	// package. When non-empty, the package must exist after
	// generation; the cascade run then touches its source.
	CascadeMidPkg string

	// LeafFanout caps how many leaves each mid-layer package
	// imports. 0 selects "all leaves". Lower values produce a
	// sparser DAG, useful for testing the cascade closure size.
	LeafFanout int

	// MidFanout caps how many mid-layer packages each root
	// imports. 0 selects "all mid". Same semantics as LeafFanout.
	MidFanout int
}

// SmallShape is the small fixture: 3 leaves + 2 mid + 1 root = 6
// packages, depth 3. Designed for sub-second benchmarks; the
// load-bearing claim is that the harness runs end-to-end without
// touching the cascade machinery.
var SmallShape = FixtureShape{
	Name:        "bench_small",
	Module:      "example.com/bench/small",
	NumLeaves:   3,
	NumMidLayer: 2,
	NumRoots:    1,
}

// MediumShape is the medium fixture: 10 leaves + 6 mid + 3 roots =
// 19 packages, depth 3, with mid-layer fan-out so roots see
// overlapping closure. Designed for several-second benchmarks; the
// load-bearing claim is the scheduler's gate actually fires.
var MediumShape = FixtureShape{
	Name:        "bench_medium",
	Module:      "example.com/bench/medium",
	NumLeaves:   10,
	NumMidLayer: 6,
	NumRoots:    3,
	LeafFanout:  4,
}

// CascadeShape is the cascade fixture: 3 leaves + 1 cascade-mid +
// 6 root packages, all importing the cascade-mid. Touching
// cascade-mid produces the W10 cascade signal: 1 edited package,
// 6 packages whose action graph must re-run.
var CascadeShape = FixtureShape{
	Name:          "bench_cascade",
	Module:        "example.com/bench/cascade",
	NumLeaves:     3,
	NumMidLayer:   1,
	NumRoots:      6,
	CascadeMidPkg: "mid0",
}

// GenerateFixture materialises shape under dir (which must already
// exist). The returned ModuleRoot is the absolute path of dir; the
// returned CascadeFile is the path of the file the cascade scenario
// should edit (empty if shape has no cascade-mid).
//
// The generated module is buildable by the standard go toolchain:
// each package contains one .go file, all dependencies are within
// the module, and the go.mod declares go 1.22.
func GenerateFixture(dir string, shape FixtureShape) (modRoot, cascadeFile string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	modRoot = dir

	// go.mod
	gomod := fmt.Sprintf("module %s\n\ngo 1.22\n", shape.Module)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		return "", "", err
	}

	// Leaves: no imports, single exported function and type.
	for i := 0; i < shape.NumLeaves; i++ {
		pkg := fmt.Sprintf("leaf%d", i)
		body := fmt.Sprintf(`package %s

// T is a marker type the mid-layer references.
type T struct {
	Name string
}

// New is the leaf's only constructor; touched transitively by
// every importer.
func New(name string) *T { return &T{Name: name} }

// Use is a no-op the importer calls so the import is non-trivial.
func Use(t *T) string {
	if t == nil {
		return ""
	}
	return t.Name
}
`, pkg)
		if err := writeFixtureFile(dir, pkg+"/"+pkg+".go", body); err != nil {
			return "", "", err
		}
	}

	// Mid-layer: imports a subset of leaves.
	leafFanout := shape.LeafFanout
	if leafFanout == 0 {
		leafFanout = shape.NumLeaves
	}
	if leafFanout > shape.NumLeaves {
		leafFanout = shape.NumLeaves
	}
	for i := 0; i < shape.NumMidLayer; i++ {
		pkg := fmt.Sprintf("mid%d", i)
		body := generateMidBody(shape, pkg, i, leafFanout)
		if err := writeFixtureFile(dir, pkg+"/"+pkg+".go", body); err != nil {
			return "", "", err
		}
		if pkg == shape.CascadeMidPkg {
			cascadeFile = filepath.Join(dir, pkg, pkg+".go")
		}
	}
	if shape.CascadeMidPkg != "" && cascadeFile == "" {
		return "", "", fmt.Errorf("CascadeMidPkg %q not in generated mid-layer", shape.CascadeMidPkg)
	}

	// Roots: imports a subset of mid-layer.
	midFanout := shape.MidFanout
	if midFanout == 0 {
		midFanout = shape.NumMidLayer
	}
	if midFanout > shape.NumMidLayer {
		midFanout = shape.NumMidLayer
	}
	for i := 0; i < shape.NumRoots; i++ {
		pkg := fmt.Sprintf("root%d", i)
		body := generateRootBody(shape, pkg, i, midFanout)
		if err := writeFixtureFile(dir, pkg+"/"+pkg+".go", body); err != nil {
			return "", "", err
		}
	}
	return modRoot, cascadeFile, nil
}

// generateMidBody produces the source for a mid-layer package.
// Each mid-layer package imports `leafFanout` leaves, picked by
// rotating the index so different mids cover overlapping leaf
// subsets (which is what produces non-trivial cross-flow at the
// L2/L1 boundaries).
func generateMidBody(shape FixtureShape, pkg string, idx, leafFanout int) string {
	var imports []string
	var uses []string
	for j := 0; j < leafFanout; j++ {
		k := (idx + j) % shape.NumLeaves
		imports = append(imports, fmt.Sprintf("\t%q", shape.Module+"/leaf"+itoa(k)))
		uses = append(uses, fmt.Sprintf("\tleaf%d.Use(leaf%d.New(%q))", k, k, pkg+"_use_"+itoa(j)))
	}
	importBlock := "import (\n" + strings.Join(imports, "\n") + "\n)"
	useBlock := strings.Join(uses, "\n")

	// We avoid using leaf packages with an alias because alias-import
	// is a known irritant for some analyzers (varcheck, deadcode);
	// instead each `Touch` body references the leaf's package name
	// directly.
	return fmt.Sprintf(`package %s

%s

// MidT mirrors the leaf type so the root layer sees a
// non-trivial mid-package surface.
type MidT struct {
	Name string
}

// New constructs a MidT; called by the root layer so the
// mid-package is non-trivially used.
func New(name string) *MidT { return &MidT{Name: name} }

// Touch runs every imported leaf's Use(New(...)) so the analyzer
// graph has cross-package work to do.
func Touch() string {
%s
	return %q
}
`, pkg, importBlock, useBlock, pkg)
}

// generateRootBody produces the source for a root package. Each
// root imports `midFanout` mid-layer packages, picked by rotating
// the index (same overlapping-subset trick as the mid layer).
func generateRootBody(shape FixtureShape, pkg string, idx, midFanout int) string {
	var imports []string
	var uses []string
	for j := 0; j < midFanout; j++ {
		k := (idx + j) % shape.NumMidLayer
		imports = append(imports, fmt.Sprintf("\t%q", shape.Module+"/mid"+itoa(k)))
		uses = append(uses, fmt.Sprintf("\t_ = mid%d.Touch()", k))
	}
	importBlock := "import (\n" + strings.Join(imports, "\n") + "\n)"
	useBlock := strings.Join(uses, "\n")

	return fmt.Sprintf(`package %s

%s

// Run is the root's only entrypoint; the harness's Analyze loop
// reaches every imported mid-layer through this function.
func Run() {
%s
}
`, pkg, importBlock, useBlock)
}

// writeFixtureFile is a thin os.WriteFile + mkdir helper.
func writeFixtureFile(root, rel, body string) error {
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

// itoa avoids the strconv import to keep the generator self-
// contained when this file is copied into the testdata tree's
// regenerator.
func itoa(i int) string { return fmt.Sprintf("%d", i) }
