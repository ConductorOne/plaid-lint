// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"sync/atomic"
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// newFakeNode builds a minimal analysisNode with a deterministic
// ph.key. Mirrors newFakeAction but at the node level (no action /
// analyzer wiring).
func newFakeNode(pkgID string, keySeed byte) *analysisNode {
	var key file.Hash
	for i := range key {
		key[i] = keySeed
	}
	return &analysisNode{
		ph: &packageHandle{
			mp: &metadata.Package{
				ID:      metadata.PackageID(pkgID),
				PkgPath: metadata.PackagePath(pkgID),
			},
			key: key,
		},
	}
}

// TestInputDigest_Deterministic — two consecutive calls on the same
// node MUST agree byte-for-byte. This is the substitution invariant
// cacheKeyInputBased relies on.
func TestInputDigest_Deterministic(t *testing.T) {
	an := newFakeNode("example.com/p", 1)
	d1 := an.inputDigest()
	d2 := an.inputDigest()
	if d1 != d2 {
		t.Errorf("inputDigest non-deterministic: %x vs %x", d1, d2)
	}
}

// TestInputDigest_TracksPhKey — changing ph.key changes the digest.
// Pins the reachability-content invalidation contract.
func TestInputDigest_TracksPhKey(t *testing.T) {
	a := newFakeNode("example.com/p", 1).inputDigest()
	b := newFakeNode("example.com/p", 2).inputDigest()
	if a == b {
		t.Errorf("ph.key change did not propagate to inputDigest")
	}
}

// TestInputDigest_TracksPkgPath — changing PkgPath changes the digest.
// Two packages with the same content hash but different paths must
// not alias.
func TestInputDigest_TracksPkgPath(t *testing.T) {
	a := newFakeNode("example.com/p", 1).inputDigest()
	b := newFakeNode("example.com/q", 1).inputDigest()
	if a == b {
		t.Errorf("PkgPath change did not propagate to inputDigest")
	}
}

// TestInputDigest_VerifyMode_DetectsDivergence is the runtime safety
// net for the determinism claim. It installs an inputDigestHook that
// returns a different value on each invocation, then runs cacheKey
// under flag=1+verify and asserts the verify-mode panic fires with
// the expected per-vdep error tag. The hook fires inside the vdep's
// inputDigest call from cacheKeyInputBased's per-vdep loop and from
// verify mode's own per-vdep re-derive.
func TestInputDigest_VerifyMode_DetectsDivergence(t *testing.T) {
	t.Setenv("PLAID_INPUT_DIGEST", "1")
	t.Setenv("PLAID_INPUT_DIGEST_VERIFY", "1")

	// Counter-driven hook: emits a different hash on each invocation,
	// simulating an analyzer whose inputDigest body reads process
	// state (clock, NumCPU, env var, etc.).
	var calls atomic.Uint64
	prev := inputDigestHook
	inputDigestHook = func(_ *analysisNode, _ file.Hash) file.Hash {
		var out file.Hash
		out[0] = byte(calls.Add(1))
		return out
	}
	t.Cleanup(func() { inputDigestHook = prev })

	consumer := newFakeNode("example.com/p", 1)
	vdep := newFakeNode("example.com/dep", 7)
	consumer.succs = map[PackageID]*analysisNode{
		PackageID("example.com/dep"): vdep,
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected verify-mode panic, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if want := "PLAID_INPUT_DIGEST_VERIFY"; len(msg) < len(want) || msg[:len(want)] != want {
			t.Errorf("panic message prefix: got %q, want prefix %q", msg, want)
		}
		// Verify mode's per-vdep loop fires first (per the order in
		// cacheKey), so the tag should pin the offending node.
		if want := "inputDigest:vdep"; !contains(msg, want) {
			t.Errorf("panic message: got %q, want to contain %q", msg, want)
		}
	}()
	_ = consumer.cacheKey()
}

// contains is a tiny strings.Contains stand-in (avoid the import for
// the single use site).
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestInputDigest_NoActionFieldRead is the structural decoupling pin.
// Under flag=1 the cacheKey path must NOT read vdep.actions. The
// summaryHash instrument (vdepActionReadObserve) increments per
// action; we assert the counter stays at zero after cacheKey runs
// against a vdep whose actions map is populated. Without the
// decoupling — under flag=0 — the counter would increment.
func TestInputDigest_NoActionFieldRead(t *testing.T) {
	t.Setenv("PLAID_INPUT_DIGEST", "1")

	consumer := newFakeNode("example.com/p", 1)
	vdep := newFakeNode("example.com/dep", 7)
	// Populate vdep.actions so the legacy path WOULD read it. We
	// don't run analyzers here; this is purely a structural test.
	vdep.actions = actionMap{
		"fakeanalyzer": &actionSummary{
			FactsHash: file.Hash{1, 2, 3, 4},
		},
	}
	consumer.succs = map[PackageID]*analysisNode{
		PackageID("example.com/dep"): vdep,
	}

	vdepActionReadReset()
	_ = consumer.cacheKey()
	if got := vdepActionReadCount(); got != 0 {
		t.Errorf("cacheKey under flag=1 read vdep.actions %d times; want 0", got)
	}

	// Sanity check: under flag=0, the same setup should increment
	// the counter (summaryHash iterates vdep.actions). Re-derives
	// require a fresh summaryHashOnce, so use a new vdep node.
	t.Setenv("PLAID_INPUT_DIGEST", "0")
	consumer2 := newFakeNode("example.com/p", 1)
	vdep2 := newFakeNode("example.com/dep", 7)
	vdep2.actions = actionMap{
		"fakeanalyzer": &actionSummary{
			FactsHash: file.Hash{1, 2, 3, 4},
		},
	}
	consumer2.succs = map[PackageID]*analysisNode{
		PackageID("example.com/dep"): vdep2,
	}
	vdepActionReadReset()
	_ = consumer2.cacheKey()
	if got := vdepActionReadCount(); got == 0 {
		t.Errorf("cacheKey under flag=0 did not read vdep.actions; instrument broken")
	}
}
