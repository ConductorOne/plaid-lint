// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/unit"
	"github.com/conductorone/plaid-lint/internal/unitcache"
)

// The tests below prove the three properties the unit cache lives or
// dies by, end to end through the CLI:
//
//  1. a hit reproduces a cold run byte for byte (SARIF, facts, stderr,
//     exit status);
//  2. perturbing any declared input recomputes instead of replaying;
//  3. the ambient environment changes nothing about what is served.
//
// (2) and (3) share a technique: the cache is seeded with a POISONED
// entry whose bytes no analysis would ever produce. A run that emits
// those bytes was served from the cache; a run that emits anything
// else was not. Without it, "the outputs are correct" is equally
// consistent with a working cache and with a cache that never hits.

// unitCacheFixture is a two-package module (root imports leaf) with a
// seeded errcheck violation in each. Two packages is the minimum that
// exercises the dependency-facts input class.
var unitCacheFixtureFiles = map[string]string{
	"go.mod": "module example.com/unitcache\n\ngo 1.26\n",
	"leaf/leaf.go": `package leaf

import (
	"fmt"
	"os"
)

// Logf is a printf wrapper; govet records a fact about it that the
// dependent package consumes.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}

// Touch has a deliberately unchecked error return (errcheck).
func Touch() {
	os.Remove("leaf-scratch")
}
`,
	"root/root.go": `package root

import "example.com/unitcache/leaf"

// Report misuses leaf.Logf; only leaf's exported fact makes it
// visible.
func Report() {
	leaf.Logf("%d")
}
`,
}

// unitCacheGolangci writes the analyzer selection these tests run
// under: errcheck for a local finding, govet for the fact-carried one.
func unitCacheGolangci(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "golangci.unitcache.yml")
	body := `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - govet
`
	if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return p
}

// unitCacheSetup builds the fixture and runs the leaf action once
// (uncached) so the dependent action has real declared facts to
// consume. It returns the module dir, the root action's config and
// output paths, and a fresh cache directory.
func unitCacheSetup(t *testing.T) (dir, cfgPath, sarifPath, factsPath, cacheDir string) {
	t.Helper()
	dir, pkgs := buildUnitFixture(t, unitCacheFixtureFiles)
	leaf, root := pkgs["example.com/unitcache/leaf"], pkgs["example.com/unitcache/root"]
	if leaf == nil || root == nil {
		t.Fatalf("fixture packages missing: %v", pkgs)
	}
	golangci := unitCacheGolangci(t, dir)

	leafCfg, _, leafFacts := writeUnitCfg(t, leaf, golangci, nil)
	if code, _, stderr := runApp(t, dir, "unit", "--cfg", leafCfg); code != exitSuccess {
		t.Fatalf("leaf action exit=%d stderr=%q", code, stderr)
	}
	cfgPath, sarifPath, factsPath = writeUnitCfg(t, root, golangci,
		map[string]string{"example.com/unitcache/leaf": leafFacts})
	return dir, cfgPath, sarifPath, factsPath, filepath.Join(t.TempDir(), "unit-cache")
}

// runUnitCached runs one unit action with the cache enabled.
func runUnitCached(t *testing.T, dir, cfgPath, cacheDir string) (int, string) {
	t.Helper()
	code, _, stderr := runApp(t, dir, "unit", "--cfg", cfgPath, "--cache-dir", cacheDir)
	return code, stderr
}

// readBytes reads a file or fails the test.
func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

// removeOutputs deletes the action's declared outputs, so what the
// next run leaves behind is unambiguously the next run's doing.
func removeOutputs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", p, err)
		}
	}
}

// unitCacheKey computes the key the CLI would compute for this action.
// It runs from the same working directory the CLI runs from, because
// a config declaring absolute paths keys on it (see unitcache's
// ComputeKey).
func unitCacheKey(t *testing.T, dir, cfgPath string) unitcache.Key {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	ucfg, err := unit.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	toolID, err := unitToolID()
	if err != nil {
		t.Fatalf("unitToolID: %v", err)
	}
	key, err := unitcache.ComputeKey(cfgPath, ucfg, toolID)
	if err != nil {
		t.Fatalf("ComputeKey: %v", err)
	}
	return key
}

// poisonedSarif / poisonedFacts are bytes no analysis produces.
var (
	poisonedSarif = []byte(`{"poisoned":"unit cache entry"}`)
	poisonedFacts = []byte("PLF\x01poisoned")
)

// poisonCache replaces the action's entry with recognizable garbage
// and returns the key it was filed under.
func poisonCache(t *testing.T, dir, cfgPath, cacheDir string) unitcache.Key {
	t.Helper()
	store, err := unitcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	key := unitCacheKey(t, dir, cfgPath)
	entry := &unitcache.Entry{
		Sarif:    poisonedSarif,
		Facts:    poisonedFacts,
		HasFacts: true,
		Warnings: []string{"warning: poisoned entry"},
	}
	if err := store.Put(key, entry); err != nil {
		t.Fatalf("put poisoned entry: %v", err)
	}
	return key
}

// countCacheEntries counts published entries under a cache root.
func countCacheEntries(t *testing.T, cacheDir string) int {
	t.Helper()
	var n int
	err := filepath.Walk(cacheDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache: %v", err)
	}
	return n
}

// TestUnitCache_HitReproducesColdRunByteForByte is property (1): the
// second run of an unchanged action produces the same SARIF bytes, the
// same facts bytes, the same stderr and the same exit status as the
// first — the same standard the Bazel e2e holds worker and one-shot
// execution to.
func TestUnitCache_HitReproducesColdRunByteForByte(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)

	code, coldStderr := runUnitCached(t, dir, cfgPath, cacheDir)
	if code != exitSuccess {
		t.Fatalf("cold run exit=%d stderr=%q", code, coldStderr)
	}
	coldSarif, coldFacts := readBytes(t, sarifPath), readBytes(t, factsPath)
	if n := countCacheEntries(t, cacheDir); n != 1 {
		t.Fatalf("cold run published %d cache entries, want 1", n)
	}

	removeOutputs(t, sarifPath, factsPath)
	code, warmStderr := runUnitCached(t, dir, cfgPath, cacheDir)
	if code != exitSuccess {
		t.Fatalf("warm run exit=%d stderr=%q", code, warmStderr)
	}
	if got := readBytes(t, sarifPath); !bytes.Equal(got, coldSarif) {
		t.Errorf("cached SARIF differs from the cold run:\ncold: %s\nwarm: %s", coldSarif, got)
	}
	if got := readBytes(t, factsPath); !bytes.Equal(got, coldFacts) {
		t.Errorf("cached facts differ from the cold run (%d vs %d bytes)", len(coldFacts), len(got))
	}
	if warmStderr != coldStderr {
		t.Errorf("cached run stderr = %q; cold run said %q", warmStderr, coldStderr)
	}
	if n := countCacheEntries(t, cacheDir); n != 1 {
		t.Errorf("warm run published a second entry (%d total); the key is not stable", n)
	}
}

// TestUnitCache_HitIsServedFromTheCache proves reuse actually happens:
// with a poisoned entry filed under the action's key, the run emits
// the poison. This is the test that keeps the other two honest — a
// cache that silently never hit would pass a byte-identity assertion
// while delivering nothing.
func TestUnitCache_HitIsServedFromTheCache(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)

	if code, stderr := runUnitCached(t, dir, cfgPath, cacheDir); code != exitSuccess {
		t.Fatalf("cold run exit=%d stderr=%q", code, stderr)
	}
	poisonCache(t, dir, cfgPath, cacheDir)
	removeOutputs(t, sarifPath, factsPath)

	code, stderr := runUnitCached(t, dir, cfgPath, cacheDir)
	if code != exitSuccess {
		t.Fatalf("warm run exit=%d stderr=%q", code, stderr)
	}
	if got := readBytes(t, sarifPath); !bytes.Equal(got, poisonedSarif) {
		t.Errorf("action recomputed instead of using its cache entry: SARIF = %s", got)
	}
	if got := readBytes(t, factsPath); !bytes.Equal(got, poisonedFacts) {
		t.Errorf("facts output was not served from the cache entry: %q", got)
	}
	if !strings.Contains(stderr, "poisoned entry") {
		t.Errorf("stderr %q does not carry the cached run's warnings", stderr)
	}
}

// TestUnitCache_PerturbedInputRecomputes is property (2): change any
// declared input and the poisoned entry must NOT be served. A cache
// that replayed findings across a config change would be worse than no
// cache — it would report a repository clean under rules it never ran.
func TestUnitCache_PerturbedInputRecomputes(t *testing.T) {
	cases := []struct {
		name    string
		perturb func(t *testing.T, dir, cfgPath string)
	}{
		{"package source", func(t *testing.T, dir, _ string) {
			appendLine(t, filepath.Join(dir, "root", "root.go"), "\n// touched\n")
		}},
		{"dependency facts", func(t *testing.T, dir, cfgPath string) {
			ucfg, err := unit.LoadConfig(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range ucfg.Deps.Facts {
				appendLine(t, p, "extra")
			}
		}},
		{"golangci config", func(t *testing.T, dir, _ string) {
			appendLine(t, filepath.Join(dir, "golangci.unitcache.yml"), "    - ineffassign\n")
		}},
		{"unit.json", func(t *testing.T, _, cfgPath string) {
			body := readBytes(t, cfgPath)
			patched := bytes.Replace(body, []byte(`"config":`), []byte(`"mode": "facts_only",
    "config":`), 1)
			if bytes.Equal(body, patched) {
				t.Fatalf("unit.json did not contain the expected analysis block:\n%s", body)
			}
			if err := os.WriteFile(cfgPath, patched, 0o666); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)
			if code, stderr := runUnitCached(t, dir, cfgPath, cacheDir); code != exitSuccess {
				t.Fatalf("cold run exit=%d stderr=%q", code, stderr)
			}
			poisonCache(t, dir, cfgPath, cacheDir)
			removeOutputs(t, sarifPath, factsPath)

			tc.perturb(t, dir, cfgPath)

			code, stderr := runUnitCached(t, dir, cfgPath, cacheDir)
			if code != exitSuccess {
				t.Fatalf("run after perturbation exit=%d stderr=%q", code, stderr)
			}
			if got := readBytes(t, sarifPath); bytes.Equal(got, poisonedSarif) {
				t.Errorf("the entry keyed on the OLD %s was served after it changed", tc.name)
			}
			if got := readBytes(t, factsPath); bytes.Equal(got, poisonedFacts) {
				t.Errorf("facts from the entry keyed on the OLD %s were served after it changed", tc.name)
			}
		})
	}
}

// appendLine appends text to an existing file.
func appendLine(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
}

// TestUnitCache_AmbientEnvironmentDoesNotChangeWhatIsServed is
// property (3). The variables below steer every other cache in this
// repo (and differ freely between a developer's shell, a CI worker and
// a remote executor); none may reach this one. The poisoned entry is
// what makes the assertion sharp: the run must still be served from
// the same entry under a wholly different environment.
func TestUnitCache_AmbientEnvironmentDoesNotChangeWhatIsServed(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)
	if code, stderr := runUnitCached(t, dir, cfgPath, cacheDir); code != exitSuccess {
		t.Fatalf("cold run exit=%d stderr=%q", code, stderr)
	}
	poisonCache(t, dir, cfgPath, cacheDir)
	removeOutputs(t, sarifPath, factsPath)

	for _, kv := range [][2]string{
		{"GOCACHEPROG", "/nonexistent/cacheprog"},
		{"XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg")},
		{"HOME", filepath.Join(t.TempDir(), "home")},
		{"GOCACHE", filepath.Join(t.TempDir(), "gocache")},
		{"PLAID_CACHE_BACKEND", "gocacheprog"},
		{"PLAID_L1_CACHE_BACKEND", "gocacheprog"},
		{"PLAID_CACHE_DIR", filepath.Join(t.TempDir(), "plaid")},
		{"PLAID_METRICS_TRACE", "1"},
	} {
		t.Setenv(kv[0], kv[1])
	}

	code, stderr := runUnitCached(t, dir, cfgPath, cacheDir)
	if code != exitSuccess {
		t.Fatalf("run under a changed environment exit=%d stderr=%q", code, stderr)
	}
	if got := readBytes(t, sarifPath); !bytes.Equal(got, poisonedSarif) {
		t.Errorf("the environment changed which entry the cache served: SARIF = %s", got)
	}
	if got := readBytes(t, factsPath); !bytes.Equal(got, poisonedFacts) {
		t.Errorf("the environment changed the facts the cache served: %q", got)
	}
}

// TestUnitCache_OffByDefault: without --cache-dir nothing is read,
// nothing is written, and the action's outputs are exactly what a
// cached cold run produces. The flag buys reuse; it must not change
// what a run means.
func TestUnitCache_OffByDefault(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)

	code, _, uncachedStderr := runApp(t, dir, "unit", "--cfg", cfgPath)
	if code != exitSuccess {
		t.Fatalf("uncached run exit=%d stderr=%q", code, uncachedStderr)
	}
	uncachedSarif, uncachedFacts := readBytes(t, sarifPath), readBytes(t, factsPath)
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Errorf("a run without --cache-dir touched %s (stat err: %v)", cacheDir, err)
	}

	removeOutputs(t, sarifPath, factsPath)
	code, cachedStderr := runUnitCached(t, dir, cfgPath, cacheDir)
	if code != exitSuccess {
		t.Fatalf("cached cold run exit=%d stderr=%q", code, cachedStderr)
	}
	if got := readBytes(t, sarifPath); !bytes.Equal(got, uncachedSarif) {
		t.Errorf("enabling the cache changed the SARIF:\noff: %s\non:  %s", uncachedSarif, got)
	}
	if got := readBytes(t, factsPath); !bytes.Equal(got, uncachedFacts) {
		t.Errorf("enabling the cache changed the facts output (%d vs %d bytes)", len(uncachedFacts), len(got))
	}
	if cachedStderr != uncachedStderr {
		t.Errorf("enabling the cache changed stderr: %q vs %q", cachedStderr, uncachedStderr)
	}
}

// TestUnitCache_WorkerSharesTheCache: --cache-dir is a worker startup
// flag, so a persistent worker's requests hit the same entries a
// one-shot run would. The poisoned entry proves the worker read it.
func TestUnitCache_WorkerSharesTheCache(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, cacheDir := unitCacheSetup(t)
	if code, stderr := runUnitCached(t, dir, cfgPath, cacheDir); code != exitSuccess {
		t.Fatalf("cold run exit=%d stderr=%q", code, stderr)
	}
	poisonCache(t, dir, cfgPath, cacheDir)
	removeOutputs(t, sarifPath, factsPath)

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	var in, out bytes.Buffer
	fmt.Fprintf(&in, `{"arguments":["--cfg",%q],"requestId":1}`+"\n", cfgPath)
	sess := newUnitSession(cacheDir, io.Discard)
	if sess.cache == nil {
		t.Fatal("newUnitSession did not open the cache")
	}
	if code := unitWorkerLoop(&in, &out, sess); code != exitSuccess {
		t.Fatalf("worker loop exit=%d output=%q", code, out.String())
	}
	resps := decodeWorkResponses(t, &out)
	if len(resps) != 1 || resps[0].ExitCode != 0 {
		t.Fatalf("worker responses = %+v", resps)
	}
	if got := readBytes(t, sarifPath); !bytes.Equal(got, poisonedSarif) {
		t.Errorf("worker request did not use the cache entry: SARIF = %s", got)
	}
}

// TestUnitCache_UnreadableCacheDirDegradesToAnUncachedRun: cache setup
// is best-effort. A cache root that cannot be created is a
// housekeeping problem, and failing the action over one would trade a
// correct build for it.
func TestUnitCache_UnreadableCacheDirDegradesToAnUncachedRun(t *testing.T) {
	dir, cfgPath, sarifPath, factsPath, _ := unitCacheSetup(t)

	// A regular file cannot host a cache directory.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runApp(t, dir, "unit", "--cfg", cfgPath, "--cache-dir", blocked)
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitSuccess, stderr)
	}
	if !strings.Contains(stderr, "unit cache disabled") {
		t.Errorf("stderr=%q; want it to report the disabled cache", stderr)
	}
	for _, p := range []string{sarifPath, factsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("declared output missing after a degraded run: %v", err)
		}
	}
}
