// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import (
	"sync"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// TestStreamingFilter_PerPackage verifies that the streaming API
// applies every filter stage incrementally — three packages of
// diagnostics each go through one AddPackage call, and the kept
// stream matches what Apply would produce on the union batch.
func TestStreamingFilter_PerPackage(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Paths: []string{"pkg/pb/"},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	pkgA := []output.Diagnostic{
		{Linter: "x", Message: "drop me", Pos: output.Position{Filename: "/repo/pkg/pb/a.pb.go", Line: 10}},
		{Linter: "x", Message: "keep me", Pos: output.Position{Filename: "/repo/pkg/svc/a.go", Line: 20}},
	}
	pkgB := []output.Diagnostic{
		{Linter: "x", Message: "drop also", Pos: output.Position{Filename: "/repo/pkg/pb/b.pb.go", Line: 5}},
	}
	pkgC := []output.Diagnostic{
		{Linter: "x", Message: "keep also", Pos: output.Position{Filename: "/repo/pkg/svc/c.go", Line: 1}},
	}

	stream := f.NewStream()
	if stream == nil {
		t.Fatal("NewStream returned nil for non-nil Filter")
	}
	defer stream.Finish()

	gotA := stream.AddPackage("a", pkgA)
	gotB := stream.AddPackage("b", pkgB)
	gotC := stream.AddPackage("c", pkgC)

	if len(gotA) != 1 || gotA[0].Pos.Filename != "/repo/pkg/svc/a.go" {
		t.Errorf("pkgA: %+v", gotA)
	}
	if len(gotB) != 0 {
		t.Errorf("pkgB: %+v (want empty)", gotB)
	}
	if len(gotC) != 1 || gotC[0].Pos.Filename != "/repo/pkg/svc/c.go" {
		t.Errorf("pkgC: %+v", gotC)
	}

	// Streaming + batch should match in totals (this is the
	// equivalence guarantee for the cold-vs-warm contract — same
	// drop decisions per diagnostic).
	all := append(append(append([]output.Diagnostic{}, pkgA...), pkgB...), pkgC...)
	batch := f.Apply(all)
	if len(batch) != len(gotA)+len(gotB)+len(gotC) {
		t.Errorf("Apply produced %d, streaming kept %d total",
			len(batch), len(gotA)+len(gotB)+len(gotC))
	}
}

// TestStreamingFilter_UniqByLineAcrossPackages verifies that
// uniq-by-line dedup operates across the entire stream — not just
// within one AddPackage call. Two packages each emit a diagnostic on
// the same (file, line); only one survives.
func TestStreamingFilter_UniqByLineAcrossPackages(t *testing.T) {
	// uniqByLine is set to true by NewFilter with a non-nil cfg.
	f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	stream := f.NewStream()
	defer stream.Finish()

	a := []output.Diagnostic{
		{Linter: "gosec", Message: "issue 1", Pos: output.Position{Filename: "/repo/x.go", Line: 7}},
	}
	b := []output.Diagnostic{
		{Linter: "gosec", Message: "issue 2", Pos: output.Position{Filename: "/repo/x.go", Line: 7}},
	}

	gotA := stream.AddPackage("a", a)
	gotB := stream.AddPackage("b", b)
	if len(gotA) != 1 || len(gotB) != 0 {
		t.Errorf("uniq-by-line did not dedup across packages: A=%d B=%d", len(gotA), len(gotB))
	}
}

// TestStreamingFilter_ConcurrentAddPackage exercises AddPackage from
// multiple goroutines to verify the uniqSeen index is concurrency-safe.
func TestStreamingFilter_ConcurrentAddPackage(t *testing.T) {
	f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	stream := f.NewStream()
	defer stream.Finish()

	const N = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			diags := []output.Diagnostic{
				{Linter: "x", Message: "msg", Pos: output.Position{Filename: "/repo/p.go", Line: i + 1}},
			}
			kept := stream.AddPackage("p", diags)
			mu.Lock()
			total += len(kept)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Each goroutine uses a unique line, so all N should survive.
	if total != N {
		t.Errorf("concurrent AddPackage: kept %d / %d", total, N)
	}
}

// TestStreamingFilter_FinishClearsCaches verifies that Finish drops
// the per-file caches the streaming filter accumulated. Probes the
// generated-file cache via the un-exported field through an accessor
// in the same package.
func TestStreamingFilter_FinishClearsCaches(t *testing.T) {
	f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	// Force a generated-cache entry (path doesn't need to exist;
	// detectGenerated handles parse failures by returning false).
	f.isGenerated("/repo/nonexistent-generated.go")
	if len(f.generatedCache) == 0 {
		t.Fatal("expected generatedCache to have an entry after isGenerated()")
	}
	stream := f.NewStream()
	stream.Finish()
	if len(f.generatedCache) != 0 {
		t.Errorf("Finish did not clear generatedCache: %d entries remain", len(f.generatedCache))
	}
}

// TestStreamingFilter_NilFilter is the pass-through contract: a nil
// Filter has no NewStream entry-point in normal use, but the engine
// calls (*Filter).NewStream(); a nil receiver should return nil and a
// nil Stream's AddPackage should return its input unchanged.
func TestStreamingFilter_NilFilter(t *testing.T) {
	var f *Filter
	stream := f.NewStream()
	if stream != nil {
		t.Errorf("(nil).NewStream() = %v, want nil", stream)
	}
	got := stream.AddPackage("p", []output.Diagnostic{{Linter: "x"}})
	if len(got) != 1 {
		t.Errorf("nil-Stream AddPackage dropped diagnostics: %d", len(got))
	}
	// Finish on nil Stream must not panic.
	stream.Finish()
}
