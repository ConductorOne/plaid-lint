// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package parallel is the W11 task-1.41 parallel-safety stress test.
// 8 concurrent invocations of Snapshot.Analyze run against a shared
// L1+L2 cache directory; the test asserts every invocation completes
// without error AND every diagnostic stream is byte-identical.
//
// This pins the parallel-safety contract: parallel-safe by default, no
// `--allow-parallel-runners` flag. The test failing means a
// concurrent invocation either errored out, produced a different
// diagnostic stream, or corrupted a cache entry — any of which would
// require the user to externally serialise their `plaid-lint`
// invocations.
//
// The W4 cache primitive's first-writer-wins `link(2) O_EXCL`
// semantics are what make this work: when 8 goroutines race to
// produce the same L1 entry, exactly one writer succeeds and the
// other 7 see EEXIST and treat that as success (the L1 path is
// content-addressed, so EEXIST means "someone else stored the same
// bytes already"). The test indirectly validates that contract by
// observing byte-identical outputs.
//
// The test uses a fixed stress-test seed and a per-iteration
// barrier to keep concurrent invocations interleaving.
package parallel
