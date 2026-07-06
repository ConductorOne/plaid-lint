package output

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden controls whether failing golden assertions overwrite the
// .golden file with the actual bytes. Pass -update to regenerate the
// whole fixture set:
//
//	go test ./internal/output/ -run TestGolden -update
var updateGolden = flag.Bool("update", false, "rewrite .golden files from current output")

// renderFor produces the bytes a given format would write for fixture.
// Centralized so the golden test and the upstream-comparison test share
// it.
func renderFor(t *testing.T, format Format, diags []Diagnostic) []byte {
	t.Helper()
	var buf bytes.Buffer
	p, err := NewPrinter(format, &buf)
	if err != nil {
		t.Fatalf("NewPrinter(%q): %v", format, err)
	}
	if err := p.Print(diags); err != nil {
		t.Fatalf("Print(%q): %v", format, err)
	}
	return buf.Bytes()
}

func goldenPath(format Format, fixture string) string {
	return filepath.Join("testdata", fmt.Sprintf("%s.%s.golden", fixture, format))
}

func TestGolden(t *testing.T) {
	for _, name := range fixtureNames() {
		diags := fixtures[name]
		for _, format := range AllFormats() {
			t.Run(fmt.Sprintf("%s/%s", name, format), func(t *testing.T) {
				got := renderFor(t, format, diags)
				path := goldenPath(format, name)
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatalf("MkdirAll: %v", err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatalf("write golden: %v", err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden %s: %v (re-run with -update to create)", path, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("golden mismatch for %s\n--- want (%d bytes) ---\n%s\n--- got (%d bytes) ---\n%s",
						path, len(want), want, len(got), got)
				}
			})
		}
	}
}
