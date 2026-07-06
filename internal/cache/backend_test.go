package cache

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestBackend_RoundTrip pins the localBackend contract: Put + Get +
// Has on (namespace, id), idempotent overwrite, fs.ErrNotExist on
// miss. The seam stays meaningful only if these invariants hold for
// every backend implementation.
func TestBackend_RoundTrip(t *testing.T) {
	b := newLocalBackend(t.TempDir())
	id := NewActionID([]byte("round-trip"))
	const ns = "analyzer/ineffassign"

	// Miss → wraps fs.ErrNotExist.
	if got, err := b.Get(ns, id); err == nil {
		t.Fatalf("Get on miss: want error wrapping fs.ErrNotExist, got body %x", got)
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get on miss: want errors.Is(err, fs.ErrNotExist), got %v", err)
	}
	if b.Has(ns, id) {
		t.Fatalf("Has on miss: want false")
	}

	// Put → Has, Get returns identical bytes.
	body1 := []byte("payload one")
	if err := b.Put(ns, id, body1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !b.Has(ns, id) {
		t.Fatalf("Has after Put: want true")
	}
	got, err := b.Get(ns, id)
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if !bytes.Equal(got, body1) {
		t.Fatalf("Get bytes mismatch: got %q want %q", got, body1)
	}

	// Idempotent: Put with the same id must succeed. (localBackend's
	// link(2) O_EXCL primitive treats EEXIST as success; the previously
	// published bytes remain readable. Future backends are free to
	// overwrite, but the read-after-second-Put MUST succeed and MUST
	// return bytes that decode against id's content-addressed
	// contract.)
	if err := b.Put(ns, id, body1); err != nil {
		t.Fatalf("Put idempotent: %v", err)
	}
	got, err = b.Get(ns, id)
	if err != nil {
		t.Fatalf("Get after second Put: %v", err)
	}
	if !bytes.Equal(got, body1) {
		t.Fatalf("Get bytes after second Put mismatch: got %q want %q", got, body1)
	}

	// Namespace isolation: same id, different namespace, must miss.
	if b.Has("typecheck", id) {
		t.Fatalf("Has on isolated namespace: want false (key bled across namespaces)")
	}
	if _, err := b.Get("typecheck", id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get on isolated namespace: want fs.ErrNotExist, got %v", err)
	}
}

// clearBackendEnv unsets every env var that influences backend
// selection so a test starts from a known-zero state regardless of how
// the parent shell or `go test -count=N` happens to be configured.
func clearBackendEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PLAID_CACHE_BACKEND",
		"PLAID_L0_CACHE_BACKEND",
		"PLAID_L1_CACHE_BACKEND",
		"PLAID_L2_CACHE_BACKEND",
	} {
		t.Setenv(k, "")
	}
}

// TestBackendForTier_AllLocalByDefault pins the current default: no env
// vars set anywhere → every tier returns the local backend, matching
// the prior behavior byte-for-byte.
func TestBackendForTier_AllLocalByDefault(t *testing.T) {
	clearBackendEnv(t)
	for _, tier := range []string{TierL0, TierL1, TierL2} {
		if got := backendForTier(tier); got != "local" {
			t.Errorf("backendForTier(%q): got %q, want %q", tier, got, "local")
		}
	}
}

// TestBackendForTier_GlobalGocacheprog_L1StaysLocal pins the policy:
// PLAID_CACHE_BACKEND=gocacheprog routes L0 and L2 through gocacheprog
// but L1 stays local without an explicit per-tier override.
// cascade-l0-gocacheprog-rebench measured L1 entries (575k per cold seed)
// uploaded but never queried on the warm-S3 path — they don't earn the
// IPC cost. Users who explicitly want L1-via-gocacheprog must set
// PLAID_L1_CACHE_BACKEND=gocacheprog (covered by
// TestBackendForTier_L1OverrideOptsIn below).
func TestBackendForTier_GlobalGocacheprog_L1StaysLocal(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(TierL0); got != "gocacheprog" {
		t.Errorf("backendForTier(L0) under global=gocacheprog: got %q, want %q", got, "gocacheprog")
	}
	if got := backendForTier(TierL1); got != "local" {
		t.Errorf("backendForTier(L1) under global=gocacheprog: got %q, want %q (L1 excluded from default fallback)", got, "local")
	}
	if got := backendForTier(TierL2); got != "gocacheprog" {
		t.Errorf("backendForTier(L2) under global=gocacheprog: got %q, want %q", got, "gocacheprog")
	}
}

// TestBackendForTier_L1OverrideOptsIn pins the explicit-opt-in path:
// users who want L1 traffic on gocacheprog set PLAID_L1_CACHE_BACKEND
// directly. The per-tier override takes precedence over the L1 carve-out.
func TestBackendForTier_L1OverrideOptsIn(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("PLAID_L1_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(TierL1); got != "gocacheprog" {
		t.Errorf("backendForTier(L1) with explicit L1=gocacheprog: got %q, want %q", got, "gocacheprog")
	}
}

// TestBackendForTier_PerTierOverride pins the new capability: a per-
// tier override with the global unset selects gocacheprog for L0 and
// leaves L1/L2 on the default local backend. This is the
// "shared-L0-only" recommended config for CI workers.
func TestBackendForTier_PerTierOverride(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_L0_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(TierL0); got != "gocacheprog" {
		t.Errorf("backendForTier(L0): got %q, want %q", got, "gocacheprog")
	}
	if got := backendForTier(TierL1); got != "local" {
		t.Errorf("backendForTier(L1): got %q, want %q", got, "local")
	}
	if got := backendForTier(TierL2); got != "local" {
		t.Errorf("backendForTier(L2): got %q, want %q", got, "local")
	}
}

// TestBackendForTier_OverrideBeatsGlobal pins the precedence rule:
// PLAID_<TIER>_CACHE_BACKEND beats PLAID_CACHE_BACKEND when both
// are set. So "global local, L0 to gocacheprog" inverts the default
// for L0 alone.
func TestBackendForTier_OverrideBeatsGlobal(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "local")
	t.Setenv("PLAID_L0_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(TierL0); got != "gocacheprog" {
		t.Errorf("backendForTier(L0) with override: got %q, want %q", got, "gocacheprog")
	}
	if got := backendForTier(TierL1); got != "local" {
		t.Errorf("backendForTier(L1) with global=local: got %q, want %q", got, "local")
	}
	if got := backendForTier(TierL2); got != "local" {
		t.Errorf("backendForTier(L2) with global=local: got %q, want %q", got, "local")
	}
}

// TestBackendForTier_EmptyTierFallsBackToGlobal pins the contract for
// callers that don't pass a tier (e.g. legacy OpenBackend): they see
// the global env var only, never any per-tier override.
func TestBackendForTier_EmptyTierFallsBackToGlobal(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_L0_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(""); got != "local" {
		t.Errorf("backendForTier(\"\") with only L0 override: got %q, want %q",
			got, "local")
	}
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	if got := backendForTier(""); got != "gocacheprog" {
		t.Errorf("backendForTier(\"\") with global set: got %q, want %q",
			got, "gocacheprog")
	}
}

// TestSelectBackendForTier_RoutesL0ToGocacheprog_L1Local is the
// integration-shaped pin: with PLAID_L0_CACHE_BACKEND=gocacheprog
// and PLAID_L1_CACHE_BACKEND unset, selectBackendForTier hands L0 a
// gocacheprog backend and L1 a local backend (file lands on disk under
// the cache root). This is the load-bearing assertion that motivates
// the whole change.
func TestSelectBackendForTier_RoutesL0ToGocacheprog_L1Local(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_L0_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	root := t.TempDir()

	l0b, err := selectBackendForTier(filepath.Join(root, "l0"), TierL0)
	if err != nil {
		t.Fatalf("selectBackendForTier(L0): %v", err)
	}
	defer func() {
		if c, ok := l0b.(*gocacheprogBackend); ok {
			_ = c.Close()
		}
	}()
	if _, ok := l0b.(*gocacheprogBackend); !ok {
		t.Fatalf("L0 backend type: got %T, want *gocacheprogBackend", l0b)
	}

	l1Root := filepath.Join(root, "l1")
	if err := os.MkdirAll(l1Root, 0o755); err != nil {
		t.Fatalf("mkdir L1 root: %v", err)
	}
	l1b, err := selectBackendForTier(l1Root, TierL1)
	if err != nil {
		t.Fatalf("selectBackendForTier(L1): %v", err)
	}
	if _, ok := l1b.(*localBackend); !ok {
		t.Fatalf("L1 backend type: got %T, want *localBackend", l1b)
	}

	// Sanity: an L1 Put lands on the local disk under the L1 root, not
	// under any global cache dir. The hex shard layout pin is in
	// localBackend's own round-trip test; here we only need to assert
	// that the file exists under l1Root, proving L1 traffic stayed
	// off the gocacheprog backend.
	id := NewActionID([]byte("per-tier-l1-local"), []byte(t.Name()))
	const ns = "analyzer/ineffassign"
	if err := l1b.Put(ns, id, []byte("local L1 payload")); err != nil {
		t.Fatalf("L1 Put: %v", err)
	}
	hex := id.Hex()
	expectedPath := filepath.Join(l1Root, ns, hex[:2], hex)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("L1 Put did not land on local disk at %s: %v", expectedPath, err)
	}
}

// TestSelectBackendForTier_GlobalGocacheprog_L0L2OnlyByDefault pins the
// integration-shaped policy: with PLAID_CACHE_BACKEND=gocacheprog and
// no per-tier override, L0 and L2 get gocacheprog backends but L1 gets a
// localBackend. The L1 carve-out is enforced at backendForTier and
// applies regardless of whether GOCACHEPROG is set.
func TestSelectBackendForTier_GlobalGocacheprog_L0L2OnlyByDefault(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	root := t.TempDir()
	var toClose []*gocacheprogBackend
	defer func() {
		for _, b := range toClose {
			_ = b.Close()
		}
	}()

	for _, tier := range []string{TierL0, TierL2} {
		b, err := selectBackendForTier(filepath.Join(root, tier), tier)
		if err != nil {
			t.Fatalf("selectBackendForTier(%q): %v", tier, err)
		}
		gb, ok := b.(*gocacheprogBackend)
		if !ok {
			t.Fatalf("backend for %q: got %T, want *gocacheprogBackend", tier, b)
		}
		toClose = append(toClose, gb)
	}

	l1Root := filepath.Join(root, "l1-carveout")
	if err := os.MkdirAll(l1Root, 0o755); err != nil {
		t.Fatalf("mkdir L1 root: %v", err)
	}
	l1b, err := selectBackendForTier(l1Root, TierL1)
	if err != nil {
		t.Fatalf("selectBackendForTier(L1): %v", err)
	}
	if _, ok := l1b.(*localBackend); !ok {
		t.Fatalf("L1 backend under global=gocacheprog: got %T, want *localBackend (L1 carve-out)", l1b)
	}
}

// TestSelectBackendForTier_L1OverrideForcesGocacheprog pins the explicit
// opt-in: PLAID_CACHE_BACKEND=gocacheprog + PLAID_L1_CACHE_BACKEND=gocacheprog
// puts L1 traffic on the gocacheprog backend, defeating the default carve-out.
func TestSelectBackendForTier_L1OverrideForcesGocacheprog(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("PLAID_L1_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	root := t.TempDir()
	b, err := selectBackendForTier(filepath.Join(root, "l1"), TierL1)
	if err != nil {
		t.Fatalf("selectBackendForTier(L1): %v", err)
	}
	gb, ok := b.(*gocacheprogBackend)
	if !ok {
		t.Fatalf("L1 backend with explicit override: got %T, want *gocacheprogBackend", b)
	}
	defer gb.Close()
}

// TestSelectBackendForTier_UnknownBackendErrors pins the strict-typo
// guard: an unknown backend name on either the global env var or a
// per-tier override fails loud rather than silently falling through to
// "local".
func TestSelectBackendForTier_UnknownBackendErrors(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_L0_CACHE_BACKEND", "definitely-not-a-backend")
	if _, err := selectBackendForTier(t.TempDir(), TierL0); err == nil {
		t.Fatalf("selectBackendForTier(L0) with garbage backend: want error, got nil")
	}
	// L1 with the same garbage on the global var (no per-tier override)
	// must also error.
	clearBackendEnv(t)
	t.Setenv("PLAID_CACHE_BACKEND", "still-garbage")
	if _, err := selectBackendForTier(t.TempDir(), TierL1); err == nil {
		t.Fatalf("selectBackendForTier(L1) with garbage global: want error, got nil")
	}
}

// TestOpenForTier_RoutesL1AndL2Independently exercises the full
// Cache.OpenForTier surface: L1 to local, L2 to gocacheprog (and back-
// wards: a Cache opened with the global env unset and a per-tier
// override sees the override). The on-disk init still happens against
// the cache root either way; the test just pins the backend type
// observed by hot-path traffic.
func TestOpenForTier_RoutesL1AndL2Independently(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_DISABLE_GC", "1")
	t.Setenv("PLAID_L2_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	l1Dir := t.TempDir()
	l2Dir := t.TempDir()

	l1, err := OpenForTier(l1Dir, TierL1)
	if err != nil {
		t.Fatalf("OpenForTier(L1): %v", err)
	}
	defer func() { _ = l1.Close() }()
	if _, ok := l1.backend.(*localBackend); !ok {
		t.Fatalf("L1 backend type: got %T, want *localBackend", l1.backend)
	}

	l2, err := OpenForTier(l2Dir, TierL2)
	if err != nil {
		t.Fatalf("OpenForTier(L2): %v", err)
	}
	defer func() { _ = l2.Close() }()
	if _, ok := l2.backend.(*gocacheprogBackend); !ok {
		t.Fatalf("L2 backend type: got %T, want *gocacheprogBackend", l2.backend)
	}
}

// TestOpenForTier_CloseAllBackends_PerNamespace pins the shutdown
// contract under a mixed config: an L1 local cache and an L2
// gocacheprog cache both Close cleanly (no goroutine leaks, no
// dangling helper process), and Close is idempotent on each.
func TestOpenForTier_CloseAllBackends_PerNamespace(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("PLAID_DISABLE_GC", "1")
	t.Setenv("PLAID_L2_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	l1, err := OpenForTier(t.TempDir(), TierL1)
	if err != nil {
		t.Fatalf("OpenForTier(L1): %v", err)
	}
	l2, err := OpenForTier(t.TempDir(), TierL2)
	if err != nil {
		t.Fatalf("OpenForTier(L2): %v", err)
	}

	if err := l1.Close(); err != nil {
		t.Fatalf("L1 Close: %v", err)
	}
	if err := l1.Close(); err != nil {
		t.Fatalf("L1 Close (second): %v", err)
	}
	if err := l2.Close(); err != nil {
		t.Fatalf("L2 Close: %v", err)
	}
	if err := l2.Close(); err != nil {
		t.Fatalf("L2 Close (second): %v", err)
	}
}
