// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unitcache

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func testKey(b byte) Key {
	var k Key
	for i := range k {
		k[i] = b
	}
	return k
}

// TestStore_RoundTrip: what goes in comes out — output bytes AND the
// warnings, since a run that dropped a "linter X is not supported"
// warning is not the same run.
func TestStore_RoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := testKey(0x11)
	want := &Entry{
		Sarif:    []byte(`{"runs":[]}`),
		Facts:    []byte("PLF\x01payload"),
		HasFacts: true,
		Warnings: []string{"warning: linter gomodguard is not supported in unit mode", "warning: second"},
	}
	if err := s.Put(k, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned a miss for a key just written")
	}
	if !bytes.Equal(got.Sarif, want.Sarif) || !bytes.Equal(got.Facts, want.Facts) || got.HasFacts != want.HasFacts {
		t.Errorf("entry bytes differ: got %+v want %+v", got, want)
	}
	if !slices.Equal(got.Warnings, want.Warnings) {
		t.Errorf("warnings = %q want %q", got.Warnings, want.Warnings)
	}
}

// TestStore_MissIsNotAnError: an absent key is a miss, so a cold cache
// is an ordinary run rather than a failed action.
func TestStore_MissIsNotAnError(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Get(testKey(0x22))
	if err != nil {
		t.Fatalf("Get on a cold cache: %v", err)
	}
	if got != nil {
		t.Errorf("Get returned %+v for a key never written", got)
	}
}

// TestStore_NoFactsEntry: mode=module actions declare no facts output,
// and an entry for one must not invent an empty facts file.
func TestStore_NoFactsEntry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := testKey(0x33)
	if err := s.Put(k, &Entry{Sarif: []byte("{}")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(k)
	if err != nil || got == nil {
		t.Fatalf("Get: %v (entry %+v)", err, got)
	}
	if got.HasFacts || len(got.Facts) != 0 {
		t.Errorf("entry invented facts: %+v", got)
	}

	outDir := t.TempDir()
	out := Outputs{Sarif: filepath.Join(outDir, "out.sarif"), Facts: filepath.Join(outDir, "out.plaidfacts")}
	if err := got.Write(out); err == nil {
		t.Error("a facts-less entry satisfied an action that declares a facts output")
	}
	if _, err := os.Stat(out.Facts); err == nil {
		t.Error("the rejected entry left a facts output behind")
	}
}

// TestStore_CorruptEntryIsNotServed: a damaged entry must fail loudly
// rather than hand back plausible-looking bytes. Every byte position
// is exercised through the three framing layers (magic, embedded key,
// trailing checksum) by flipping one byte at a time.
func TestStore_CorruptEntryIsNotServed(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := testKey(0x44)
	entry := &Entry{Sarif: []byte(`{"runs":[{"results":[]}]}`), Facts: []byte("PLF\x01f"), HasFacts: true, Warnings: []string{"w"}}
	if err := s.Put(k, entry); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := s.path(k)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}

	for i := range good {
		damaged := slices.Clone(good)
		damaged[i] ^= 0xff
		if err := os.WriteFile(path, damaged, 0o666); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(k)
		if err == nil {
			t.Fatalf("byte %d: damaged entry was served as %+v", i, got)
		}
		if got != nil {
			t.Fatalf("byte %d: Get returned both an error and an entry", i)
		}
	}
}

// TestStore_MisfiledEntryIsNotServed: an entry carrying a different
// key — a hand-copied file, a botched sync — is rejected. The key
// lives inside the entry precisely so its filename is not the only
// thing vouching for it.
func TestStore_MisfiledEntryIsNotServed(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	real, other := testKey(0x55), testKey(0x66)
	if err := s.Put(other, &Entry{Sarif: []byte("{}")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	body, err := os.ReadFile(s.path(other))
	if err != nil {
		t.Fatal(err)
	}
	dest := s.path(real)
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, body, 0o666); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(real); err == nil {
		t.Errorf("misfiled entry was served as %+v", got)
	}
}

// TestStore_ReadWriteOutputs: the production round trip — capture what
// an action wrote, then materialize it again elsewhere byte for byte.
func TestStore_ReadWriteOutputs(t *testing.T) {
	src := t.TempDir()
	out := Outputs{Sarif: filepath.Join(src, "a.sarif"), Facts: filepath.Join(src, "a.plaidfacts")}
	sarif, facts := []byte(`{"runs":[]}`), []byte("PLF\x01facts")
	if err := os.WriteFile(out.Sarif, sarif, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out.Facts, facts, 0o666); err != nil {
		t.Fatal(err)
	}

	entry, err := ReadOutputs(out, []string{"warning: w"})
	if err != nil {
		t.Fatalf("ReadOutputs: %v", err)
	}

	// A nested, not-yet-existing output directory is the Bazel shape.
	dstDir := filepath.Join(t.TempDir(), "nested", "deeper")
	dst := Outputs{Sarif: filepath.Join(dstDir, "a.sarif"), Facts: filepath.Join(dstDir, "a.plaidfacts")}
	if err := entry.Write(dst); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, pair := range [][2]string{{out.Sarif, dst.Sarif}, {out.Facts, dst.Facts}} {
		a, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs from %s after a cache round trip", pair[1], pair[0])
		}
	}
}

// TestStore_ConcurrentPutGet: several processes analyzing different
// packages share one cache root, and within a build several may land
// on the same key. Publishing is a rename, so a concurrent reader sees
// either no entry or a whole one — never a half-written file.
func TestStore_ConcurrentPutGet(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := testKey(0x77)
	entry := &Entry{Sarif: bytes.Repeat([]byte("x"), 1<<16), Warnings: []string{"w"}}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := s.Put(k, entry); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			got, err := s.Get(k)
			if err != nil {
				errs <- err
				return
			}
			if got != nil && !bytes.Equal(got.Sarif, entry.Sarif) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent access: %v", err)
	}
}
