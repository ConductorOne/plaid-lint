// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package l0

import (
	"fmt"
	"io/fs"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
)

// mockBackend records every Get/Put/Has call and serves bytes out of
// an in-memory map. Mirrors the cache-package mockBackend so the L0
// dispatch invariant can be pinned without spinning up the local FS
// backend.
type mockBackend struct {
	getCalls []backendCall
	putCalls []backendCall
	store    map[string][]byte
}

type backendCall struct {
	Namespace string
	ID        clcache.ActionID
}

func newMockBackend() *mockBackend {
	return &mockBackend{store: map[string][]byte{}}
}

func (m *mockBackend) Get(namespace string, id clcache.ActionID) ([]byte, error) {
	m.getCalls = append(m.getCalls, backendCall{Namespace: namespace, ID: id})
	if body, ok := m.store[namespace+"|"+id.Hex()]; ok {
		out := make([]byte, len(body))
		copy(out, body)
		return out, nil
	}
	return nil, fmt.Errorf("mock miss for %s/%s: %w", namespace, id.Hex(), fs.ErrNotExist)
}

func (m *mockBackend) Put(namespace string, id clcache.ActionID, body []byte) error {
	m.putCalls = append(m.putCalls, backendCall{Namespace: namespace, ID: id})
	cp := make([]byte, len(body))
	copy(cp, body)
	m.store[namespace+"|"+id.Hex()] = cp
	return nil
}

func (m *mockBackend) Has(namespace string, id clcache.ActionID) bool {
	_, ok := m.store[namespace+"|"+id.Hex()]
	return ok
}

// TestL0_DispatchesThroughBackend is the load-bearing test that L0's
// Get/Put route through the backend seam. If L0 ever regrows a direct
// os.ReadFile / os.WriteFile (e.g. via a resurrected duplicate of
// writeFileAtomic), this test catches it: the mock would observe zero
// calls.
func TestL0_DispatchesThroughBackend(t *testing.T) {
	mock := newMockBackend()
	c := NewWithBackendForTest(mock)

	id := ComputeKey(sampleKey())
	e := sampleEntry()

	// Put → exactly one backend Put under the L0 namespace.
	if err := c.Put(id, e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, want := len(mock.putCalls), 1; got != want {
		t.Fatalf("Put: backend.Put calls = %d, want %d", got, want)
	}
	if got := mock.putCalls[0].Namespace; got != nsL0 {
		t.Errorf("Put namespace = %q, want %q", got, nsL0)
	}
	if got := mock.putCalls[0].ID; got != id {
		t.Errorf("Put id = %x, want %x", got, id)
	}

	// Get on the same id → exactly one backend Get, same namespace+id,
	// round-trips the entry.
	got, err := c.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PackageID != e.PackageID {
		t.Errorf("round-tripped PackageID = %q, want %q", got.PackageID, e.PackageID)
	}
	if len(mock.getCalls) != 1 {
		t.Fatalf("Get: backend.Get calls = %d, want 1", len(mock.getCalls))
	}
	if mock.getCalls[0].Namespace != nsL0 {
		t.Errorf("Get namespace = %q, want %q", mock.getCalls[0].Namespace, nsL0)
	}

	// Get on a missing id → fs.ErrNotExist from the mock, surfaces
	// through Cache.Get with miss-metric bumped.
	missing := ComputeKey(KeyParts{PackageID: "no-such-pkg"})
	if _, err := c.Get(missing); err == nil {
		t.Errorf("Get on missing id: want error, got nil")
	}
	m := c.MetricsPtr().Snapshot()
	if m.Hits != 1 || m.Stores != 1 || m.Misses != 1 {
		t.Errorf("metrics: %+v (want Hits=1 Stores=1 Misses=1)", m)
	}
}
