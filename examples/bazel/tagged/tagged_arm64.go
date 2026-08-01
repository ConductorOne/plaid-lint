// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build arm64

package tagged

// hexExpand is the arm64 implementation; tagged_noasm.go declares the
// same function for every other arch.
func hexExpand(s string) string { return s + "/arm64" }
