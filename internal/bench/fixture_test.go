// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateFixture_ShapesBuild confirms every documented fixture
// shape produces a buildable module. We run `go build ./...` against
// the generated module and expect a zero exit code. If this test
// fails, the fixture generator has produced invalid Go; benchmarks
// against the generated module would surface the same error as a
// "package does not compile" skip in the analyze pipeline, which
// would invalidate the benchmark.
func TestGenerateFixture_ShapesBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
	cases := []FixtureShape{SmallShape, MediumShape, CascadeShape}
	for _, shape := range cases {
		shape := shape
		t.Run(shape.Name, func(t *testing.T) {
			dir := t.TempDir()
			modRoot, cascadeFile, err := GenerateFixture(dir, shape)
			if err != nil {
				t.Fatalf("GenerateFixture: %v", err)
			}
			if modRoot != dir {
				t.Errorf("GenerateFixture modRoot = %q, want %q", modRoot, dir)
			}
			if shape.CascadeMidPkg != "" && cascadeFile == "" {
				t.Errorf("CascadeMidPkg=%q but cascadeFile is empty", shape.CascadeMidPkg)
			}
			if shape.CascadeMidPkg == "" && cascadeFile != "" {
				t.Errorf("no CascadeMidPkg but cascadeFile=%q", cascadeFile)
			}
			cmd := exec.Command("go", "build", "./...")
			cmd.Dir = modRoot
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go build in %s: %v\n%s", modRoot, err, out)
			}
		})
	}
}

// TestGenerateFixture_CascadeMidExists confirms the cascade fixture's
// cascade-mid file is at the expected path and is editable.
func TestGenerateFixture_CascadeMidExists(t *testing.T) {
	dir := t.TempDir()
	_, cascadeFile, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	want := filepath.Join(dir, "mid0", "mid0.go")
	if cascadeFile != want {
		t.Errorf("cascadeFile = %q, want %q", cascadeFile, want)
	}
	// Sanity: a 'mid0' import appears in at least one root.
	for i := 0; i < CascadeShape.NumRoots; i++ {
		p := filepath.Join(dir, "root"+itoa(i), "root"+itoa(i)+".go")
		body := mustReadFile(t, p)
		if !strings.Contains(body, "mid0") {
			t.Errorf("root%d does not import mid0:\n%s", i, body)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	cmd := exec.Command("cat", p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(out)
}
