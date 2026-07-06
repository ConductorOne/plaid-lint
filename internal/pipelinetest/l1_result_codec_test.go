// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// l1_result_codec_test.go — exercises the ConsumedAsResult mechanic.
// A synthetic prerequisite analyzer with a working
// ResultCodec lets the L1 fast path cache its Result alongside
// diagnostics + facts; a consumer analyzer reads ResultOf[prereq] and
// reports a diagnostic per Result entry. Warm hits keep the consumer
// firing because the L1 path restores the Result.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// serialisableResult is a tiny gob-friendly result type our synthetic
// prerequisite analyzer returns. The consumer reads it via
// pass.ResultOf and emits a diagnostic per entry.
type serialisableResult struct {
	Tokens []string
}

func makeSynthPrereq() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "synthprereq",
		Doc:  "synthetic prereq for L1 ResultCodec test",
		Run: func(pass *analysis.Pass) (any, error) {
			// Produce a deterministic Result keyed off the package name
			// so two runs against the same package agree.
			return &serialisableResult{Tokens: []string{pass.Pkg.Name(), "prereq"}}, nil
		},
		ResultType: reflect.TypeOf((*serialisableResult)(nil)),
	}
}

func makeSynthConsumer(prereq *analysis.Analyzer) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:     "synthconsumer",
		Doc:      "consumer of synthprereq's Result for L1 ResultCodec test",
		Requires: []*analysis.Analyzer{prereq},
		Run: func(pass *analysis.Pass) (any, error) {
			r, ok := pass.ResultOf[prereq].(*serialisableResult)
			if !ok || r == nil {
				return nil, errors.New("synthconsumer: missing or nil ResultOf[synthprereq]")
			}
			if len(r.Tokens) == 0 {
				return nil, errors.New("synthconsumer: empty Tokens")
			}
			// Emit one diagnostic so the equivalence test has signal.
			for _, file := range pass.Files {
				pass.Report(analysis.Diagnostic{
					Pos:     file.Package,
					Message: "synthconsumer saw tokens=" + r.Tokens[0],
				})
				break
			}
			return nil, nil
		},
	}
}

// synthResultCodec marshals/unmarshals *serialisableResult through
// encoding/json. The codec is independent of go-runtime addresses, so
// a hit decodes to a fresh value the consumer can read.
func synthResultCodec() *analyzers.ResultCodec {
	return &analyzers.ResultCodec{
		Encode: func(result any) ([]byte, error) {
			return json.Marshal(result)
		},
		Decode: func(blob []byte) (any, error) {
			var r serialisableResult
			if err := json.Unmarshal(blob, &r); err != nil {
				return nil, err
			}
			return &r, nil
		},
	}
}

// TestL1ResultCodecRoundTrip — a prereq analyzer with a working
// ResultCodec gets its Result cached alongside the L1 entry, and a
// downstream consumer that reads pass.ResultOf still works on warm
// runs without the prereq's Run body re-executing.
func TestL1ResultCodecRoundTrip(t *testing.T) {
	requireGo(t)

	prereq := makeSynthPrereq()
	consumer := makeSynthConsumer(prereq)

	// Install a registry containing only our synthetic pair.
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:    prereq,
		ConfigSalt:  func(any) [32]byte { return [32]byte{0x01} },
		ResultCodec: synthResultCodec(),
	})
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:   consumer,
		ConfigSalt: func(any) [32]byte { return [32]byte{0x02} },
	})

	// Install AllAnalyzers for the Snapshot.Analyze walk; only the
	// consumer is "enabled" — the prereq is required via Requires.
	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(consumer)}

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()

	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	const toolVer = "plaid-lint-w7-resultcodec"

	// runResultWithErrors extends runResult locally with the L1 error
	// counter delta — the falsifiable signal. A non-zero
	// warm-pass Errors means the canonical-URI decode regression has
	// silently come back even if the high-level hit count looks fine.
	type runResultWithErrors struct {
		runResult
		l1Errors int64
	}

	runOneWithRegistry := func(t *testing.T) runResultWithErrors {
		t.Helper()
		l1, err := clcache.Open(l1Dir)
		if err != nil {
			t.Fatalf("Open L1: %v", err)
		}
		l2, err := clcache.Open(l2Dir)
		if err != nil {
			t.Fatalf("Open L2: %v", err)
		}
		c := cache.New(nil)
		c.AttachL1WithRegistry(l1, toolVer, registry)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()

		beforeL1 := c.L1Metrics()
		diags := runAnalyzePipeline(t, ws)
		afterL1 := c.L1Metrics()
		return runResultWithErrors{
			runResult: runResult{
				diagnostics: diags,
				l1Hits:      afterL1.Hits - beforeL1.Hits,
				l1Stores:    afterL1.Stores - beforeL1.Stores,
			},
			l1Errors: afterL1.Errors - beforeL1.Errors,
		}
	}

	cold := runOneWithRegistry(t)
	if cold.l1Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0")
	}
	if cold.l1Hits != 0 {
		t.Fatalf("cold: L1 hits = %d, want 0", cold.l1Hits)
	}
	if cold.l1Errors != 0 {
		t.Fatalf("cold: L1 errors = %d, want 0", cold.l1Errors)
	}
	consumerDiags := cold.diagnostics["synthconsumer"]
	if len(consumerDiags) == 0 {
		t.Fatalf("cold: synthconsumer produced no diagnostics; ResultCodec round-trip is the load-bearing claim")
	}

	warm := runOneWithRegistry(t)
	if warm.l1Hits == 0 {
		t.Errorf("warm: L1 hits = 0, want > 0")
	}
	// Falsifiable gate: a canonical URI in the on-disk gob
	// diagnostic must round-trip through json.Unmarshal +
	// protocol.DocumentURI.UnmarshalText without tripping the file-
	// scheme validator. A pre-fix build bumps Errors by exactly
	// one per diagnostic-bearing L1 entry on this fixture.
	if warm.l1Errors != 0 {
		t.Errorf("warm: L1 errors = %d, want 0 (canonical URI must round-trip through json.Unmarshal)", warm.l1Errors)
	}
	// Critical: the prereq's Run body did NOT execute on the warm
	// run (its descriptor has a ResultCodec so it stays L1-eligible),
	// but the consumer's Run body sees a non-nil ResultOf[prereq]
	// because the L1 hit path restored the Result. If the codec is
	// broken the consumer fails with "missing or nil ResultOf" and
	// no diagnostics fire.
	if len(warm.diagnostics["synthconsumer"]) != len(consumerDiags) {
		t.Errorf("warm vs cold consumer diag count mismatch: %d vs %d",
			len(warm.diagnostics["synthconsumer"]), len(consumerDiags))
	}

	// Hit rate should be 100% over eligible — the prereq is L1
	// eligible because its descriptor opts in via ResultCodec.
	if warm.l1Hits < cold.l1Stores-warm.l1Stores {
		t.Errorf("warm hit rate sub-100 pct over eligible: hits=%d eligible=%d",
			warm.l1Hits, cold.l1Stores-warm.l1Stores)
	}
}

// keep imports honest in case some helpers go unused under -tags
// filtering.
var (
	_ context.Context = context.Background()
	_                 = filepath.Base
	_                 = sort.Strings
	_ metadata.PackageID
	_ protocol.URI
)
