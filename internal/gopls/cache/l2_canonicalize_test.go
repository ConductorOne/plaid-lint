// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"go/token"
	"sync"
	"testing"
)

func newCanonFsetBatch(fset *token.FileSet) *typeCheckBatch {
	return &typeCheckBatch{_handles: map[PackageID]*packageHandle{}, fset: fset}
}

// M2c: unchanged b.fset returns the same clone pointer (cache hit).
func TestCanonicalFset_CacheHit(t *testing.T) {
	fset := token.NewFileSet()
	fset.AddFile("/abs/a.go", -1, 100)
	b := newCanonFsetBatch(fset)
	first := b.canonicalBatchFileSet()
	if first == nil {
		t.Fatal("first call returned nil")
	}
	if second := b.canonicalBatchFileSet(); second != first {
		t.Errorf("want cache hit, got rebuild: first=%p second=%p", first, second)
	}
}

// M2c: AddFile (Base()-growth) and AddExistingFiles via the
// canonicalFsetVersion bump both invalidate the cache.
func TestCanonicalFset_InvalidatesOnSizeChange(t *testing.T) {
	// Base()-growth path: real AddFile.
	fset := token.NewFileSet()
	fset.AddFile("/abs/a.go", -1, 100)
	b := newCanonFsetBatch(fset)
	first := b.canonicalBatchFileSet()
	fset.AddFile("/abs/b.go", -1, 200)
	if second := b.canonicalBatchFileSet(); second == first {
		t.Errorf("want rebuild after AddFile, got cache hit")
	}

	// Version-bump path: checkPackage's parseCache.parseFiles adds files
	// via AddExistingFiles with pre-allocated bases that don't move
	// Base() when b.fset starts at reservedForParsing.
	fset2 := fileSetWithBase(reservedForParsing)
	b2 := newCanonFsetBatch(fset2)
	pre1 := b2.canonicalBatchFileSet()
	baseBefore := fset2.Base()
	tmp := token.NewFileSet()
	fset2.AddExistingFiles(tmp.AddFile("/abs/cached.go", 1, 100))
	if got := fset2.Base(); got != baseBefore {
		t.Fatalf("precondition: Base() moved %d->%d", baseBefore, got)
	}
	if stale := b2.canonicalBatchFileSet(); stale != pre1 {
		t.Errorf("want cache hit before version bump")
	}
	b2.canonicalFsetVersion.Add(1)
	if post := b2.canonicalBatchFileSet(); post == pre1 {
		t.Errorf("want rebuild after version bump")
	}
}

// M2c: concurrent callers must not race. The mutex serializes the memo
// read+write; AddFile is internally locked by token.FileSet.
func TestCanonicalFset_RaceFree(t *testing.T) {
	fset := token.NewFileSet()
	for i := 0; i < 20; i++ {
		fset.AddFile("/abs/seed.go", -1, 50)
	}
	b := newCanonFsetBatch(fset)
	const readers, iters = 16, 200
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if b.canonicalBatchFileSet() == nil {
					t.Error("nil clone")
					return
				}
			}
		}()
	}
	for j := 0; j < 50; j++ {
		fset.AddFile("/abs/grow.go", -1, 10)
	}
	wg.Wait()
}
