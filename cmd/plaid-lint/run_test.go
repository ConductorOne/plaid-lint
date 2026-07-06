// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
)

func TestContextWithRunTimeout(t *testing.T) {
	t.Run("zero is a no-op", func(t *testing.T) {
		parent, parentCancel := context.WithCancel(context.Background())
		defer parentCancel()
		ctx, cancel := contextWithRunTimeout(parent, 0)
		defer cancel()
		if ctx != parent {
			t.Errorf("zero timeout should return parent unchanged; got distinct ctx")
		}
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("zero timeout should leave parent deadline-less; got deadline")
		}
	})

	t.Run("positive value sets a deadline", func(t *testing.T) {
		ctx, cancel := contextWithRunTimeout(context.Background(), config.Duration(50*time.Millisecond))
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("expected deadline to be set")
		}
		if d := time.Until(deadline); d > 50*time.Millisecond || d < 0 {
			t.Errorf("deadline %v not within (0, 50ms]", d)
		}
	})

	t.Run("fires deadline after timeout", func(t *testing.T) {
		ctx, cancel := contextWithRunTimeout(context.Background(), config.Duration(20*time.Millisecond))
		defer cancel()
		select {
		case <-ctx.Done():
			if ctx.Err() != context.DeadlineExceeded {
				t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
			}
		case <-time.After(200 * time.Millisecond):
			t.Errorf("ctx.Done did not fire within 200ms; timeout not enforced")
		}
	})
}
