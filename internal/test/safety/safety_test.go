// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package safety

// safety_test.go — W11 task-1.40 safety harness.
//
// Each subtest:
//
//  1. Stages the fixture under testdata/<name>/ into a writable
//     tempdir (cold-state baseline).
//  2. Runs Snapshot.Analyze cold-1 to populate L1+L2 caches.
//  3. Snapshots the L1 directory contents.
//  4. Applies the per-fixture edit (defined inline in editFor).
//  5. Runs Snapshot.Analyze cold-2 with the same on-disk L1+L2 caches.
//     The package source digest changes for re-analyzed packages, so
//     their L1 entries are written under new content-addressed paths.
//  6. Diffs the L1 directory: new files = entries written on cold-2.
//  7. Decodes each new entry's PackageID; the deduplicated set is
//     the observed cascade.
//  8. Compares observed vs expected_cascade.json's expected_packages
//     and fails t.Errorf with the diff on mismatch.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/conductorone/plaid-lint/internal/test/harness"
)

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

// expectedCascade is the on-disk schema of testdata/<name>/expected_cascade.json.
type expectedCascade struct {
	Description      string   `json:"description"`
	ExpectedPackages []string `json:"expected_packages"`
}

// editFn applies the per-fixture edit. The function returns the
// absolute paths of files that were created/modified, which are
// passed to ws.Invalidate on the re-run.
type editFn func(t *testing.T, modDir string) []string

// fixtures is the W11 1.40 fixture registry. Each entry binds a
// fixture name (matching testdata/<name>/) to the editFn that
// mutates it between cold-1 and cold-2. The expected_cascade.json
// next to each fixture pins the ground-truth post-edit re-analyzed
// set.
//
// Why an inline registry: the 6 edits are small (<10 lines each)
// and putting them in Go keeps the expected-cascade definition and
// the edit side-by-side for review. A shell-script-or-patchfile
// scheme would split the contract across two languages.
var fixtures = []struct {
	name string
	edit editFn
}{
	{
		name: "leaf_body",
		edit: func(t *testing.T, modDir string) []string {
			// Modify leaf's unexported helper body. The exported
			// surface is unchanged; gcexportdata stays byte-stable.
			path := filepath.Join(modDir, "leaf", "leaf.go")
			body := `package leaf

func helper(s string) string {
	return s + "-leaf-EDITED"
}

func Public(s string) string {
	return helper(s)
}
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
	{
		name: "leaf_wrapper",
		edit: func(t *testing.T, modDir string) []string {
			// Add a NEW exported printf wrapper to leaf. The printf
			// analyzer publishes a fact about it; consumer's
			// DepFactsDigest changes, so consumer cascades.
			path := filepath.Join(modDir, "leaf", "leaf.go")
			body := `package leaf

func Errorf(format string, args ...any) error {
	return errImpl(format, args)
}

// Warningf is the new exported printf-style wrapper added by the
// safety subtest. The printf analyzer publishes an isWrapper fact
// about it, so leaf's exported fact set changes.
func Warningf(format string, args ...any) error {
	return errImpl(format, args)
}

func errImpl(format string, args []any) error {
	_ = format
	_ = args
	return nil
}
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
	{
		name: "midtype",
		edit: func(t *testing.T, modDir string) []string {
			// Add an exported field to mid.MidT. The mid package's
			// gcexportdata changes, so consumera + consumerb both
			// re-analyze.
			path := filepath.Join(modDir, "mid", "mid.go")
			body := `package mid

import "example.com/safety/midtype/leaf"

type MidT struct {
	Underlying *leaf.LeafT
	// AddedField is the new exported field. Adding it changes
	// gcexportdata, which propagates DepTypeDigest to consumers.
	AddedField string
}

func New(name string) *MidT {
	return &MidT{Underlying: leaf.New(name)}
}
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
	{
		name: "midbody",
		edit: func(t *testing.T, modDir string) []string {
			// Edit mid's unexported helper body. Exported surface is
			// stable; consumers' DepTypeDigest doesn't change.
			path := filepath.Join(modDir, "mid", "mid.go")
			body := `package mid

import "example.com/safety/midbody/leaf"

type MidT struct {
	Underlying *leaf.LeafT
}

func New(name string) *MidT {
	return &MidT{Underlying: leaf.New(helper(name))}
}

func helper(s string) string {
	return s + "-midbody-EDITED"
}
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
	{
		name: "gomod_bump",
		edit: func(t *testing.T, modDir string) []string {
			// Bump go.mod's `go` directive from 1.22 to 1.23.
			// localPackageKey reads goVersion (from mp.Module.GoVersion)
			// so every package's ph.key changes → every L1 action ID
			// changes → every package re-analyzes.
			path := filepath.Join(modDir, "go.mod")
			body := `module example.com/safety/gomodbump

go 1.23
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
	{
		name: "buildtag_flip",
		edit: func(t *testing.T, modDir string) []string {
			// Flip extra.go's build tag from "linux || darwin" to
			// "!linux && !darwin". On a linux or darwin host, the
			// file goes from "included" to "excluded"; targetpkg's
			// exported surface loses the Extra type.
			path := filepath.Join(modDir, "targetpkg", "extra.go")
			body := `//go:build !linux && !darwin

package targetpkg

type Extra struct {
	Tag string
}

func NewExtra(tag string) *Extra { return &Extra{Tag: tag} }
`
			harness.WriteFile(t, path, body)
			return []string{path}
		},
	},
}

// runSafetyCascade is the per-fixture driver. Steps mirror the
// file-header comment; the per-step logging is verbose so failures
// localise immediately.
func runSafetyCascade(t *testing.T, fixtureName string, edit editFn) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	// Stage the fixture into a writable copy.
	modDir := harness.LeakyTempDir(t, "plaid-w11-safety-"+fixtureName+"-mod-")
	harness.CopyTree(t, filepath.Join("testdata", fixtureName), modDir)

	goplsDir := harness.LeakyTempDir(t, "plaid-w11-safety-"+fixtureName+"-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-safety-"+fixtureName+"-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-safety-"+fixtureName+"-l2-")

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}

	// Step 1: cold-1 — populate L1+L2 with baseline state.
	cold1 := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("cold-1: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		cold1.L1Metrics.Hits, cold1.L1Metrics.Stores,
		cold1.L2Metrics.Hits, cold1.L2Metrics.Stores)

	// Counter assertion: cold-1 L1 stores > 0 proves baseline
	// analyzers ran. A zero count means the fixture is broken (e.g.
	// stdlib regression) and the cascade observation is
	// meaningless. Fail fast so reviewers see the right cause.
	if cold1.L1Metrics.Stores == 0 {
		t.Fatalf("cold-1: L1.Stores = 0; baseline analysis failed (fixture broken or regression)")
	}

	// Step 2: snapshot L1 dir contents after cold-1.
	beforeEdit := harness.SnapshotL1(t, l1Dir)
	t.Logf("L1 file count after cold-1: %d", len(beforeEdit))

	// Step 3: apply the edit.
	editedPaths := edit(t, modDir)
	t.Logf("edited paths: %v", editedPaths)

	// Step 4: cold-2 — re-analyze with the same L1+L2 stores. The
	// re-run hits cache for unaffected (analyzer, package) pairs and
	// re-stores under new action IDs for the cascade.
	cfg.InvalidatePaths = editedPaths
	cold2 := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("cold-2: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		cold2.L1Metrics.Hits, cold2.L1Metrics.Stores,
		cold2.L2Metrics.Hits, cold2.L2Metrics.Stores)

	// Step 5: snapshot post-edit L1 dir.
	afterEdit := harness.SnapshotL1(t, l1Dir)
	newFiles := harness.L1NewFiles(beforeEdit, afterEdit)
	t.Logf("L1 new files after edit: %d", len(newFiles))

	// Step 6: derive observed re-analyzed package set.
	observedPackages := harness.PackagesFromL1Files(t, newFiles)

	// Step 7: load expected cascade and compare.
	expected := loadExpectedCascade(t, fixtureName)
	expectedSet := stringSliceToSet(expected.ExpectedPackages)

	missing, extra := harness.CompareStringSets(expectedSet, observedPackages)
	observedList := harness.JoinSorted(observedPackages)
	expectedList := expected.ExpectedPackages
	sort.Strings(expectedList)

	if len(missing) != 0 || len(extra) != 0 {
		t.Errorf(`cascade mismatch for %q:
  description: %s
  expected: %v
  observed: %v
  missing from observed (false negative): %v
  extra in observed (false positive):     %v`,
			fixtureName,
			expected.Description,
			expectedList,
			observedList,
			missing,
			extra)
	} else {
		t.Logf("cascade matches: %v", observedList)
	}
}

// loadExpectedCascade reads testdata/<name>/expected_cascade.json
// and unmarshals into expectedCascade. t.Fatal on any error.
func loadExpectedCascade(t *testing.T, fixtureName string) expectedCascade {
	t.Helper()
	path := filepath.Join("testdata", fixtureName, "expected_cascade.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out expectedCascade
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	if len(out.ExpectedPackages) == 0 {
		t.Fatalf("%s: expected_packages is empty; safety fixtures must declare a cascade", path)
	}
	if out.Description == "" {
		t.Errorf("%s: description is empty; safety fixtures must explain the expected cascade for reviewers", path)
	}
	return out
}

func stringSliceToSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

// TestSafetyCascade is the table-driven fixture runner. Each fixture
// gets its own subtest so the test output names which cascade
// regressed.
func TestSafetyCascade(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			runSafetyCascade(t, f.name, f.edit)
		})
	}
}

// TestSafetyFixturesHaveExpectedCascade is the meta-test ensuring
// every fixture under testdata/ has a corresponding expected_cascade.json
// — catches the "forgot to commit the golden" case.
func TestSafetyFixturesHaveExpectedCascade(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	wantSet := make(map[string]bool)
	for _, f := range fixtures {
		wantSet[f.name] = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !wantSet[name] {
			t.Errorf("testdata/%s/ has no fixture entry in safety_test.go fixtures table", name)
		}
		expectedPath := filepath.Join("testdata", name, "expected_cascade.json")
		if _, err := os.Stat(expectedPath); err != nil {
			t.Errorf("%s missing or unreadable: %v", expectedPath, err)
		}
		goModPath := filepath.Join("testdata", name, "go.mod")
		if _, err := os.Stat(goModPath); err != nil {
			t.Errorf("%s missing or unreadable: %v", goModPath, err)
		}
	}
}

