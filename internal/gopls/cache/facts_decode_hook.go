// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"github.com/conductorone/plaid-lint/internal/gopls/internal/facts"
)

// SetFactsDecodeCountHook re-exports [facts.SetDecodeCountHook] so
// the bench test package (outside gopls/internal/) can install a
// gob.Decode counter for regression pins. Test-only.
func SetFactsDecodeCountHook(fn func()) func() {
	return facts.SetDecodeCountHook(fn)
}
