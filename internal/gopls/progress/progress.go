// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package progress provides a no-op shim for the upstream gopls
// progress reporting API. The plaid-lint CLI does not need
// LSP-style progress notifications; it just satisfies the call
// sites in the forked cache code.
package progress

import "context"

// Tracker is a no-op stand-in for gopls's LSP-coupled progress tracker.
type Tracker struct{}

// SupportsWorkDoneProgress reports whether progress is supported.
// Always false in plaid-lint.
func (*Tracker) SupportsWorkDoneProgress() bool { return false }

// Start begins a work-done report. The CLI returns a no-op WorkDone.
func (*Tracker) Start(ctx context.Context, title, msg string, token any, cancel func()) *WorkDone {
	return &WorkDone{}
}

// WorkDone is a no-op work-progress handle.
type WorkDone struct{}

// End marks the work as complete.
func (*WorkDone) End(ctx context.Context, msg string) {}

// Report reports incremental progress.
func (*WorkDone) Report(ctx context.Context, msg string, pct float64) {}
