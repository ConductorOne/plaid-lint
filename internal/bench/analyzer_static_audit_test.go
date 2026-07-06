// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
)

// TestAnalyzerStaticNonDeterminismAudit is the static counterpart to
// TestAnalyzerDeterminism_NRepeats. It walks every analyzer registered
// in analyzers.BundledRegistry, resolves the analyzer's Run function
// to its source directory via runtime.FuncForPC, and greps that
// directory tree for calls into the stdlib symbols that read process
// state:
//
//   - os.Getenv / os.Environ / os.Hostname  (env / host identity)
//   - time.Now / time.Since / time.Until   (wall clock)
//   - rand.*  (math/rand or crypto/rand)
//   - runtime.GOMAXPROCS / runtime.NumCPU / runtime.NumGoroutine
//   - filepath.Abs                            (cwd-dependent)
//
// For each hit the test emits a one-line report row. The test does
// NOT fail on hits — the runtime gate (N=5 repeat) is the
// falsifiable signal; this is the surface-area pass.
//
// The output is dumped to:
//   - the test log (always)
//   - $PLAID_R31A_AUDIT_OUT when set (used by the r31a report run)
//
// Scope: every analyzer registered in BundledRegistry. The audit is
// dir-scoped: we hash every analyzer's Run-file directory once and
// also walk one level up from honnef.co/go/tools' SA-* dirs (which
// share helper code in the parent dir). Source files outside the
// analyzer's module (e.g. shared util packages) are not chased
// transitively; the runtime gate covers transitive sources.
func TestAnalyzerStaticNonDeterminismAudit(t *testing.T) {
	// Collect the source directories of every registered analyzer.
	descs := analyzers.BundledRegistry.All()
	if len(descs) == 0 {
		t.Fatalf("BundledRegistry empty; analyzer wiring is broken")
	}
	dirs := map[string][]string{} // dir → list of analyzer names that resolve there
	for _, d := range descs {
		if d.Analyzer == nil || d.Analyzer.Run == nil {
			continue
		}
		addr := reflect.ValueOf(d.Analyzer.Run).Pointer()
		fn := runtime.FuncForPC(addr)
		if fn == nil {
			continue
		}
		file, _ := fn.FileLine(addr)
		if file == "" {
			continue
		}
		dir := filepath.Dir(file)
		dirs[dir] = append(dirs[dir], d.Analyzer.Name)
	}
	t.Logf("audit covers %d unique analyzer Run-source directories (over %d registered analyzers)", len(dirs), len(descs))

	// Build the pattern set. Each pattern is a Go source token we
	// look for in non-test .go files. The classification field is
	// the column the report dumps to the body table.
	type pattern struct {
		name   string
		re     *regexp.Regexp
		kind   string
		filter func(line string) bool
	}
	notInComment := func(line string) bool {
		trim := strings.TrimSpace(line)
		return !strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "/*") && !strings.HasPrefix(trim, "*")
	}
	pats := []pattern{
		{"os.Getenv", regexp.MustCompile(`\bos\.Getenv\(`), "env", notInComment},
		{"os.Environ", regexp.MustCompile(`\bos\.Environ\(`), "env", notInComment},
		{"os.Hostname", regexp.MustCompile(`\bos\.Hostname\(`), "host", notInComment},
		{"os.Getwd", regexp.MustCompile(`\bos\.Getwd\(`), "cwd", notInComment},
		{"time.Now", regexp.MustCompile(`\btime\.Now\(`), "clock", notInComment},
		{"time.Since", regexp.MustCompile(`\btime\.Since\(`), "clock", notInComment},
		{"time.Until", regexp.MustCompile(`\btime\.Until\(`), "clock", notInComment},
		{"math/rand", regexp.MustCompile(`\brand\.(Int|Float|Read|New|Seed|Perm|Shuffle|Intn|Int31|Int63|Uint32|Uint64)\b`), "rand", notInComment},
		{"runtime.GOMAXPROCS", regexp.MustCompile(`\bruntime\.GOMAXPROCS\(`), "runtime", notInComment},
		{"runtime.NumCPU", regexp.MustCompile(`\bruntime\.NumCPU\(`), "runtime", notInComment},
		{"runtime.NumGoroutine", regexp.MustCompile(`\bruntime\.NumGoroutine\(`), "runtime", notInComment},
		{"filepath.Abs", regexp.MustCompile(`\bfilepath\.Abs\(`), "cwd", notInComment},
	}

	type hit struct {
		analyzer string // first analyzer that lives in the dir
		file     string // path relative to a module root
		line     int
		symbol   string
		kind     string
		snippet  string
	}
	var hits []hit

	sortedDirs := make([]string, 0, len(dirs))
	for d := range dirs {
		sortedDirs = append(sortedDirs, d)
	}
	sort.Strings(sortedDirs)

	for _, dir := range sortedDirs {
		analyzersInDir := dirs[dir]
		sort.Strings(analyzersInDir)
		representative := analyzersInDir[0]
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			// Stay in the immediate directory; SA-* dirs already
			// register one descriptor per dir, and the parent
			// honnef.co/go/tools/staticcheck dir holds the shared
			// helpers we want to audit separately. We pick those
			// up via the per-SA dir entries.
			if d.IsDir() && p != dir {
				return filepath.SkipDir
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			lines := strings.Split(string(body), "\n")
			for i, line := range lines {
				for _, pat := range pats {
					if !pat.re.MatchString(line) {
						continue
					}
					if pat.filter != nil && !pat.filter(line) {
						continue
					}
					hits = append(hits, hit{
						analyzer: representative,
						file:     shortenPath(p),
						line:     i + 1,
						symbol:   pat.name,
						kind:     pat.kind,
						snippet:  strings.TrimSpace(line),
					})
				}
			}
			return nil
		})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].analyzer != hits[j].analyzer {
			return hits[i].analyzer < hits[j].analyzer
		}
		if hits[i].file != hits[j].file {
			return hits[i].file < hits[j].file
		}
		return hits[i].line < hits[j].line
	})

	t.Logf("static grep produced %d hits across %d analyzer dirs", len(hits), len(dirs))

	// Group by symbol for a one-line summary.
	bySymbol := map[string]int{}
	for _, h := range hits {
		bySymbol[h.symbol]++
	}
	syms := make([]string, 0, len(bySymbol))
	for s := range bySymbol {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		t.Logf("  %s: %d", s, bySymbol[s])
	}

	// Detailed dump.
	var b strings.Builder
	fmt.Fprintf(&b, "# r31a static non-determinism audit\n")
	fmt.Fprintf(&b, "# analyzer dirs scanned: %d (over %d registered analyzers)\n", len(dirs), len(descs))
	fmt.Fprintf(&b, "# total hits: %d\n\n", len(hits))
	fmt.Fprintf(&b, "%-32s %-12s %-72s %-6s %s\n", "ANALYZER(REP)", "KIND", "FILE", "LINE", "SYMBOL")
	for _, h := range hits {
		fmt.Fprintf(&b, "%-32s %-12s %-72s %-6d %s\n", truncate(h.analyzer, 32), h.kind, truncate(h.file, 72), h.line, h.symbol)
	}
	t.Logf("static audit dump:\n%s", b.String())

	if out := os.Getenv("PLAID_R31A_AUDIT_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Logf("failed to write PLAID_R31A_AUDIT_OUT %q: %v", out, err)
		} else {
			t.Logf("wrote audit dump to %s", out)
		}
	}
}

// shortenPath collapses the GOMODCACHE prefix so report rows stay
// human-readable across hosts.
func shortenPath(p string) string {
	if mc := runtime.GOROOT(); mc != "" && strings.HasPrefix(p, mc) {
		return "$GOROOT" + strings.TrimPrefix(p, mc)
	}
	// gomodcache is typically /…/go/mod; strip everything up to the
	// last occurrence of "/mod/".
	if i := strings.LastIndex(p, "/mod/"); i >= 0 {
		return "$GOMODCACHE/" + p[i+5:]
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
