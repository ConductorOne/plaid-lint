// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package l0

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestL0_CrossMachine is the backend-level portability gate for L0
// after Stage 1.5. Mirrors TestL1_CrossMachine and complements
// the higher-level TestCanonicalize_CrossMachine: a Cache opened at
// root A writes an Entry; we copy A's L0 tree onto a fresh root B;
// a Cache opened at root B must serve the entry on Get.
//
// The test exercises only the local backend path: it asserts that the
// (namespace, id) → bytes seam preserves the on-disk layout in a way
// that survives a tree clone — i.e. that no machine-local input is
// folded into the path or filename. The end-to-end engine flow is
// covered by TestCanonicalize_CrossMachine.
func TestL0_CrossMachine(t *testing.T) {
	parentA := t.TempDir()
	cA, err := Open(parentA)
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	id := ComputeKey(sampleKey())
	e := sampleEntry()
	if err := cA.Put(id, e); err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// Clone the L0 namespace dir onto a fresh parent root B.
	parentB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parentB, nsL0), 0o755); err != nil {
		t.Fatalf("mkdir B: %v", err)
	}
	if err := copyTree(filepath.Join(parentA, nsL0), filepath.Join(parentB, nsL0)); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	cB, err := Open(parentB)
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	got, err := cB.Get(id)
	if err != nil {
		t.Fatalf("Get B: %v (L0 must serve the cloned entry)", err)
	}
	if got.PackageID != e.PackageID {
		t.Errorf("PackageID after clone: want %q, got %q", e.PackageID, got.PackageID)
	}
	if len(got.Diagnostics) != len(e.Diagnostics) {
		t.Errorf("diag count after clone: want %d, got %d", len(e.Diagnostics), len(got.Diagnostics))
	}
	if cB.MetricsPtr().Snapshot().Hits != 1 {
		t.Errorf("Hits after clone Get: want 1, got %d", cB.MetricsPtr().Snapshot().Hits)
	}

	// Falsifiability check: the parent-A absolute path must not appear
	// inside any L0 entry file. If a machine-local path leaked into the
	// gob payload (Diagnostic.Pos.Filename etc.), the engine's
	// canonicalisation regressed.
	parentABytes := []byte(parentA)
	walkErr := filepath.Walk(filepath.Join(parentA, nsL0), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Base(p) == "version" {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, parentABytes) {
			t.Errorf("L0 entry %s contains absolute parent-A path %q (leaks dev-box paths cross-machine)", p, parentA)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyOne(p, target)
	})
}

func copyOne(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
