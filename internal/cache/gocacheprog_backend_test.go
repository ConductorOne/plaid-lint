package cache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// freshNS appends a per-run salt to the namespace string so a test
// that asserts "miss before Put" is not poisoned by a prior run that
// landed under the same id. The live helper at
// /data/squire/bin/gocacheprog-wrapper persists entries across
// processes, so without a salt the second `go test` would observe a
// pre-existing hit.
func freshNS(prefix string) string {
	return fmt.Sprintf("%s/%d", prefix, time.Now().UnixNano())
}

// liveBackend returns a gocacheprog-backed backend driving the
// real /data/squire/bin/gocacheprog-wrapper helper. Tests that need a
// live wire skip cleanly when the wrapper is not reachable in the
// current environment.
func liveBackend(t *testing.T) *gocacheprogBackend {
	t.Helper()
	const wrapper = "/data/squire/bin/gocacheprog-wrapper"
	if _, err := os.Stat(wrapper); err != nil {
		t.Skipf("gocacheprog-wrapper not reachable: %v", err)
	}
	b, err := newGocacheprogBackend(wrapper)
	if err != nil {
		t.Skipf("gocacheprog-wrapper handshake failed: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// TestGocacheprogBackend_RoundTrip exercises Put + Has + Get against
// the live wrapper. Skipped (not failed) when the wrapper is not in
// the env so this file remains useful in CI.
func TestGocacheprogBackend_RoundTrip(t *testing.T) {
	b := liveBackend(t)

	id := NewActionID([]byte("gocacheprog-backend-roundtrip"), []byte(t.Name()))
	ns := freshNS("analyzer/ineffassign")

	if got, err := b.Get(ns, id); err == nil {
		t.Fatalf("Get on miss: want error, got body len=%d", len(got))
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get on miss: want errors.Is(err, fs.ErrNotExist), got %v", err)
	}
	if b.Has(ns, id) {
		t.Fatalf("Has on miss: want false")
	}

	body := []byte("hello round-trip via gocacheprog")
	if err := b.Put(ns, id, body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !b.Has(ns, id) {
		t.Fatalf("Has after Put: want true")
	}
	got, err := b.Get(ns, id)
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("Get bytes mismatch: got %q want %q", got, body)
	}
}

// TestGocacheprogBackend_NamespaceIsolation pins the deriveActionID
// contract: same ActionID under different namespaces MUST land
// disjoint cache entries.
func TestGocacheprogBackend_NamespaceIsolation(t *testing.T) {
	b := liveBackend(t)

	id := NewActionID([]byte("namespace-iso"), []byte(t.Name()))
	salt := time.Now().UnixNano()
	nsA := fmt.Sprintf("analyzer/whitespace/%d", salt)
	nsB := fmt.Sprintf("analyzer/ineffassign/%d", salt)

	bodyA := []byte("payload-for-whitespace")
	if err := b.Put(nsA, id, bodyA); err != nil {
		t.Fatalf("Put nsA: %v", err)
	}
	// nsB must still miss.
	if b.Has(nsB, id) {
		t.Fatalf("Has(nsB): want false, namespace boundary failed")
	}
	if _, err := b.Get(nsB, id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get(nsB) on isolated namespace: want fs.ErrNotExist, got %v", err)
	}
	// nsA still readable.
	got, err := b.Get(nsA, id)
	if err != nil {
		t.Fatalf("Get(nsA): %v", err)
	}
	if !bytes.Equal(got, bodyA) {
		t.Fatalf("Get(nsA) body mismatch")
	}
}

// TestGocacheprogBackend_SingleflightProbeLive is the probe-first
// measurement against the live wrapper. It seeds one
// entry, then fires K concurrent same-key Gets and reports the
// duplicate-coalescing rate. The test logs the rate (so the run
// surfaces it as evidence) and fails only if zero coalescing occurred
// — that would mean singleflight is structurally broken on the live
// path. The cascade-workload rate is measured in a follow-up bench.
func TestGocacheprogBackend_SingleflightProbeLive(t *testing.T) {
	b := liveBackend(t)
	const K = 64

	id := NewActionID([]byte("singleflight-probe"), []byte(t.Name()))
	ns := freshNS("analyzer/sf-probe")
	body := make([]byte, 4096) // L2-ish size so the body read matters
	for i := range body {
		body[i] = byte(i)
	}
	if err := b.Put(ns, id, body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	before := b.client.SingleflightStats()

	var wg sync.WaitGroup
	for i := 0; i < K; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := b.Get(ns, id)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			if len(got) != len(body) {
				t.Errorf("Get body len: got %d want %d", len(got), len(body))
			}
		}()
	}
	wg.Wait()

	after := b.client.SingleflightStats()
	total := after.GetTotal - before.GetTotal
	coalesced := after.GetCoalesced - before.GetCoalesced
	if total != K {
		t.Errorf("GetTotal delta: got %d want %d", total, K)
	}
	rate := float64(coalesced) / float64(total)
	t.Logf("live probe: K=%d Gets, %d coalesced (%.1f%% rate)",
		total, coalesced, rate*100)
	if coalesced == 0 {
		t.Fatalf("zero coalescing on K=%d concurrent same-key Gets — singleflight broken on live path", K)
	}
}

// stubHelper writes a /bin/sh script that performs the GOCACHEPROG
// handshake, then reads its stdin until EOF and exits 0. The
// advertised KnownCommands intentionally exclude "close" so the
// client's Close path goes straight to "close stdin, wait for read
// loop, reap child" rather than waiting on a protocol close reply
// the stub does not implement. That matches the wire shape the bench
// observed against the real wrapper, where the read-loop drains on
// EOF.
func stubHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stub.sh")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"ID\":0,\"KnownCommands\":[\"get\",\"put\"]}'\n" +
		"while IFS= read -r _; do :; done\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// TestGocacheprogBackend_Close_ShutsDownPluginChild verifies that
// Close() terminates the helper subprocess promptly. Pre-fix, the
// helper was left running in futex_wait_queue after plaid-lint
// finished its work and the parent process refused to exit.
func TestGocacheprogBackend_Close_ShutsDownPluginChild(t *testing.T) {
	b, err := newGocacheprogBackend(stubHelper(t))
	if err != nil {
		t.Fatalf("newGocacheprogBackend: %v", err)
	}
	pid := b.client.PidForTest()
	if pid <= 0 {
		t.Fatalf("PidForTest: got %d, want >0", pid)
	}

	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Close did not return within 10s")
	}

	// Helper must have exited. signal(0) reports an error once the
	// kernel has reaped the pid (the cmd.Wait call inside Close).
	assertProcessExited(t, pid)
}

// assertProcessExited probes pid liveness via signal(0). After
// cmd.Wait reaps the child, the kernel reports ESRCH on the pid (or
// EPERM on a recycled pid we don't own); either is acceptable. A nil
// error means the child is still running and the test fails.
func assertProcessExited(t *testing.T, pid int) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if sigErr := proc.Signal(syscall.Signal(0)); sigErr == nil {
		t.Fatalf("helper pid %d still alive after Close", pid)
	}
}

// TestGocacheprogBackend_Close_Idempotent verifies Close() is safe to
// call multiple times and does not panic, matching the io.Closer
// idempotency convention used elsewhere in plaid-lint.
func TestGocacheprogBackend_Close_Idempotent(t *testing.T) {
	b, err := newGocacheprogBackend(stubHelper(t))
	if err != nil {
		t.Fatalf("newGocacheprogBackend: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second call must not panic and must return the same (cached)
	// error; the Client uses sync.Once internally.
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Third call for good measure.
	if err := b.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}

// TestCacheClose_TerminatesGocacheprogHelper verifies cache.Cache.Close
// propagates through to the backend when PLAID_CACHE_BACKEND selects
// the gocacheprog backend. This is the wired path that the
// cmd/plaid-lint defer relies on.
func TestCacheClose_TerminatesGocacheprogHelper(t *testing.T) {
	t.Setenv("PLAID_DISABLE_GC", "1")
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stubHelper(t))

	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gb, ok := c.backend.(*gocacheprogBackend)
	if !ok {
		t.Fatalf("backend type = %T, want *gocacheprogBackend", c.backend)
	}
	pid := gb.client.PidForTest()

	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Cache.Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Cache.Close did not return within 10s")
	}

	// Idempotent: Cache.Close has its own sync.Once, so a second
	// call also returns nil without re-invoking the backend.
	if err := c.Close(); err != nil {
		t.Fatalf("second Cache.Close: %v", err)
	}

	assertProcessExited(t, pid)
}

// TestCacheClose_LocalBackendIsNoOp pins the contract that closing a
// localBackend-backed cache is a successful no-op: the local path has
// no resources to release, but callers must be able to defer Close
// unconditionally without branching on the env var.
func TestCacheClose_LocalBackendIsNoOp(t *testing.T) {
	t.Setenv("PLAID_DISABLE_GC", "1")
	t.Setenv("PLAID_CACHE_BACKEND", "")
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close on local backend: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close on local backend: %v", err)
	}
}

// TestDeriveActionID_NamespaceSeparator pins the null-byte separator
// invariant without needing a live backend. Two (namespace, id) pairs
// that share a concat-collision string MUST hash distinctly.
func TestDeriveActionID_NamespaceSeparator(t *testing.T) {
	idA := ActionID{1, 2, 3, 4}
	idB := ActionID{2, 3, 4}
	a := deriveActionID("analyzer/a", idA)
	b := deriveActionID("analyzer/b", idA)
	if a == b {
		t.Fatalf("deriveActionID collided across namespaces")
	}
	// And same-ns same-id is stable.
	a2 := deriveActionID("analyzer/a", idA)
	if a != a2 {
		t.Fatalf("deriveActionID is non-deterministic")
	}
	// Cross-id same-ns must differ too.
	c := deriveActionID("analyzer/a", idB)
	if a == c {
		t.Fatalf("deriveActionID collapsed distinct ids")
	}
}

func TestValidGocacheprogBody(t *testing.T) {
	body := []byte("cached diagnostics")
	outputID := sha256.Sum256(body)
	if !validGocacheprogBody(outputID, body) {
		t.Fatalf("validGocacheprogBody rejected matching body")
	}
	body[0] ^= 0xff
	if validGocacheprogBody(outputID, body) {
		t.Fatalf("validGocacheprogBody accepted mismatched body")
	}
}
