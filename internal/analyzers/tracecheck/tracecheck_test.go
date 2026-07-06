// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tracecheck_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/conductorone/plaid-lint/internal/analyzers/tracecheck"
)

// DataDir returns the path to the testdata fixture tree, computed
// relative to this test file so the test works from any cwd.
func DataDir() string {
	_, testFilename, _, _ := runtime.Caller(1)
	return filepath.Join(filepath.Dir(testFilename), "testdata")
}

// TestTraceCheck pins the diagnostic surface against the testdata
// fixture vendored from upstream (github.com/ductone/ci-tools). The
// `// want "..."` comments in testdata/src/trace/trace.go declare the
// expected diagnostics; analysistest.Run fails the test on any drift.
func TestTraceCheck(t *testing.T) {
	analysistest.Run(t, DataDir(), tracecheck.Analyzer, "trace")
}
