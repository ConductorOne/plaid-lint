// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// l3.go wires the gopls action graph to the L3 IRManager coordination
// interface. The wiring inspects each action's analyzer
// and, when the analyzer's Run will consume IR, takes a pin on the
// action's package via the batch-scoped IRManager. The pin is
// released via a defer in action.exec.
//
// "Will consume IR" is decided by two cooperating sources:
//
//  1. AnalyzerDescriptor.NeedsIR — set at bundle time via
//     analyzers.AnalyzerRequiresIR for every registered analyzer.
//  2. analyzers.AnalyzerRequiresIR — a runtime walker-fallback for
//     analyzers WITHOUT a registered descriptor. Honnef intermediate
//     prerequisites (fact_purity, fact_typedness, honnef's internal
//     nilness) are pulled in transitively via SA-* Requires chains
//     but are not in BundledRegistry; their Run bodies still
//     consume `pass.ResultOf[buildir]`. Both must pin, otherwise
//     the W9 scheduler's free-after-fanin signal undercounts IR
//     consumers and a use-after-free is possible.
//
// The default Cache attaches no IRManager (nil); pinIRForAction
// returns a nil *l3.Pin in that case, and *l3.Pin.Release is safe to
// call on a nil receiver. The W7 baseline is byte-equivalent.

import (
	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/l3"
)

// pinIRForAction takes an L3 pin on act's package iff:
//
//  1. The batch has an attached IRManager, AND
//  2. The action's analyzer either (a) has a registered descriptor
//     with NeedsIR=true, or (b) has no descriptor but its
//     transitive Requires set includes an IR-producer (buildir or
//     buildssa) per [analyzers.AnalyzerRequiresIR].
//
// When neither condition is met, pinIRForAction returns nil. The
// returned *l3.Pin's Release method tolerates a nil receiver, so the
// caller can unconditionally `defer pin.Release()`.
//
// The pin's lifetime brackets the analyzer's Run body (the L3
// liveness window). When the analyzer is a prerequisite (e.g.
// buildir, the IR-producer itself), its Run will populate the IR
// stored in act.result; that result is read by horizontal-dependent
// consumers via pass.ResultOf and stays live as long as the action
// graph keeps act alive — which is until analysisNode.run returns.
// Pinning the prereq's Run window therefore covers the construction
// half; pinning each consumer's Run window covers the consumption
// half. The W9 scheduler treats the "last release on pkg" event as
// its free-after-fanin signal.
func pinIRForAction(act *action) *l3.Pin {
	if act == nil || act.an == nil || act.an.batch == nil {
		return nil
	}
	b := act.an.batch
	if b.irManager == nil {
		return nil
	}
	d := act.descriptor()
	switch {
	case d != nil && d.NeedsIR:
		// Registered descriptor opts in — fast path.
	case d == nil && analyzers.AnalyzerRequiresIR(act.a):
		// Unregistered transitive prerequisite (honnef's
		// fact_purity, fact_typedness, internal/nilness, ...).
		// The walker is the single source of truth.
	default:
		return nil
	}
	pkg := l3.PackageID(act.pkg.pkg.metadata.ID)
	// When the manager satisfies the optional analyzer-aware
	// extension, thread the analyzer name through so observers can
	// key on (pkg, analyzer) tuples. The default SequentialIRManager
	// does NOT implement this extension, so production behaviour
	// is unchanged.
	if aware, ok := b.irManager.(l3.AnalyzerAwareIRManager); ok {
		return aware.PinWithAnalyzer(pkg, act.a.Name)
	}
	return b.irManager.Pin(pkg)
}
