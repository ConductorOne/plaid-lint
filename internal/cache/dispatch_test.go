package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

// mockBackend records every Get/Put/Has call and serves bytes out of
// an in-memory map. It exists purely to verify that *Cache routes its
// six hot-path methods through the backend seam. A future
// GOCACHEPROG backend would slot into the same
// interface; if *Cache ever side-steps the seam (e.g. with a direct
// os.ReadFile), this test fails.
type mockBackend struct {
	getCalls []backendCall
	putCalls []backendCall
	hasCalls []backendCall
	store    map[string][]byte
}

type backendCall struct {
	Namespace string
	ID        ActionID
}

func newMockBackend() *mockBackend {
	return &mockBackend{store: map[string][]byte{}}
}

func (m *mockBackend) Get(namespace string, id ActionID) ([]byte, error) {
	m.getCalls = append(m.getCalls, backendCall{Namespace: namespace, ID: id})
	if body, ok := m.store[namespace+"|"+id.Hex()]; ok {
		// Return a copy so callers cannot mutate the stored bytes.
		out := make([]byte, len(body))
		copy(out, body)
		return out, nil
	}
	return nil, fmt.Errorf("mock miss for %s/%s: %w", namespace, id.Hex(), fs.ErrNotExist)
}

func (m *mockBackend) Put(namespace string, id ActionID, body []byte) error {
	m.putCalls = append(m.putCalls, backendCall{Namespace: namespace, ID: id})
	cp := make([]byte, len(body))
	copy(cp, body)
	m.store[namespace+"|"+id.Hex()] = cp
	return nil
}

func (m *mockBackend) Has(namespace string, id ActionID) bool {
	m.hasCalls = append(m.hasCalls, backendCall{Namespace: namespace, ID: id})
	_, ok := m.store[namespace+"|"+id.Hex()]
	return ok
}

// TestCache_DispatchesThroughBackend is the LOAD-BEARING test that the
// backend seam actually intercepts every L1/L2 hot-path read, write,
// and probe. A future Stage 2 swap (GOCACHEPROG backend) relies on
// this guarantee; if Cache regrows a direct os.ReadFile / link call,
// this test catches it.
func TestCache_DispatchesThroughBackend(t *testing.T) {
	mock := newMockBackend()
	// Build a Cache that does NOT touch disk: skip Open / init entirely
	// and inject the mock. validateAnalyzerName + envelope encoding
	// still run; only the storage seam is swapped out.
	c := &Cache{backend: mock}

	const analyzer = "ineffassign"
	e1 := sampleL1()
	e1.Analyzer = analyzer
	l1ID := NewActionID([]byte("l1-id"))

	// --- L1 write → exactly one Put with namespace "analyzer/<name>".
	if err := c.WriteL1(e1, l1ID); err != nil {
		t.Fatalf("WriteL1: %v", err)
	}
	if got, want := len(mock.putCalls), 1; got != want {
		t.Fatalf("WriteL1 Put calls: got %d, want %d", got, want)
	}
	if got, want := mock.putCalls[0].Namespace, "analyzer/"+analyzer; got != want {
		t.Fatalf("WriteL1 namespace: got %q want %q", got, want)
	}
	if mock.putCalls[0].ID != l1ID {
		t.Fatalf("WriteL1 id: got %s want %s", mock.putCalls[0].ID.Hex(), l1ID.Hex())
	}

	// --- L1 has → exactly one Has on the same namespace+id.
	if !c.HasL1(analyzer, l1ID) {
		t.Fatalf("HasL1: want true (mock holds the entry)")
	}
	if got, want := len(mock.hasCalls), 1; got != want {
		t.Fatalf("HasL1 Has calls: got %d, want %d", got, want)
	}
	if got, want := mock.hasCalls[0].Namespace, "analyzer/"+analyzer; got != want {
		t.Fatalf("HasL1 namespace: got %q want %q", got, want)
	}

	// --- L1 read → exactly one Get on the same namespace+id, decoded
	// back to the original entry.
	got1, err := c.ReadL1(analyzer, l1ID)
	if err != nil {
		t.Fatalf("ReadL1: %v", err)
	}
	if got, want := len(mock.getCalls), 1; got != want {
		t.Fatalf("ReadL1 Get calls: got %d, want %d", got, want)
	}
	if got, want := mock.getCalls[0].Namespace, "analyzer/"+analyzer; got != want {
		t.Fatalf("ReadL1 namespace: got %q want %q", got, want)
	}
	if got1.Analyzer != e1.Analyzer || got1.PackageID != e1.PackageID {
		t.Fatalf("ReadL1 decoded entry identity mismatch")
	}

	// --- L1 miss → fs.ErrNotExist propagates through the seam.
	if _, err := c.ReadL1(analyzer, NewActionID([]byte("absent"))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadL1 miss: want fs.ErrNotExist, got %v", err)
	}

	// --- L2 write/has/read on the "typecheck" namespace.
	e2 := sampleL2()
	l2ID := NewActionID([]byte("l2-id"))

	if err := c.WriteL2(e2, l2ID); err != nil {
		t.Fatalf("WriteL2: %v", err)
	}
	if got, want := len(mock.putCalls), 2; got != want {
		t.Fatalf("WriteL2 Put calls (cumulative): got %d, want %d", got, want)
	}
	if got, want := mock.putCalls[1].Namespace, "typecheck"; got != want {
		t.Fatalf("WriteL2 namespace: got %q want %q", got, want)
	}
	if mock.putCalls[1].ID != l2ID {
		t.Fatalf("WriteL2 id: got %s want %s", mock.putCalls[1].ID.Hex(), l2ID.Hex())
	}

	if !c.HasL2(l2ID) {
		t.Fatalf("HasL2: want true")
	}
	if got, want := len(mock.hasCalls), 2; got != want {
		t.Fatalf("HasL2 Has calls (cumulative): got %d, want %d", got, want)
	}
	if got, want := mock.hasCalls[1].Namespace, "typecheck"; got != want {
		t.Fatalf("HasL2 namespace: got %q want %q", got, want)
	}

	preGetL2 := len(mock.getCalls)
	got2, err := c.ReadL2(l2ID)
	if err != nil {
		t.Fatalf("ReadL2: %v", err)
	}
	if got, want := len(mock.getCalls), preGetL2+1; got != want {
		t.Fatalf("ReadL2 Get calls: got %d, want %d", got, want)
	}
	if got, want := mock.getCalls[preGetL2].Namespace, "typecheck"; got != want {
		t.Fatalf("ReadL2 namespace: got %q want %q", got, want)
	}
	if mock.getCalls[preGetL2].ID != l2ID {
		t.Fatalf("ReadL2 id: got %s want %s", mock.getCalls[preGetL2].ID.Hex(), l2ID.Hex())
	}
	if got2.PackageID != e2.PackageID || !bytes.Equal(got2.ExportData, e2.ExportData) {
		t.Fatalf("ReadL2 decoded entry mismatch")
	}

	// Belt and braces: an invalid analyzer name MUST short-circuit
	// before reaching the backend; validation is *Cache's job, not the
	// backend's. (Stage 2 backends will not re-validate names.)
	preGet := len(mock.getCalls)
	prePut := len(mock.putCalls)
	preHas := len(mock.hasCalls)
	if err := c.WriteL1(&L1Entry{Analyzer: "../escape"}, l1ID); err == nil {
		t.Fatalf("WriteL1 with bad analyzer: want validation error, got nil")
	}
	if _, err := c.ReadL1("../escape", l1ID); err == nil {
		t.Fatalf("ReadL1 with bad analyzer: want validation error, got nil")
	}
	if c.HasL1("../escape", l1ID) {
		t.Fatalf("HasL1 with bad analyzer: want false")
	}
	if len(mock.getCalls) != preGet || len(mock.putCalls) != prePut || len(mock.hasCalls) != preHas {
		t.Fatalf("bad-analyzer paths reached backend: get %d→%d, put %d→%d, has %d→%d",
			preGet, len(mock.getCalls), prePut, len(mock.putCalls), preHas, len(mock.hasCalls))
	}

	// Silence unused-import warnings if sample helpers ever stop using
	// encoding/json. (sampleL1's Diagnostics field carries
	// json.RawMessage; the build will catch a real unused import.)
	_ = json.RawMessage(nil)
}
