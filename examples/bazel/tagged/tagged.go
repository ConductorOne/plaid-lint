// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tagged pins the driver's build-constraint file selection:
// tagged_arm64.go and tagged_noasm.go declare the SAME private helper
// under mutually exclusive //go:build constraints (the c1
// //pkg/randkey shape). The archive declares both files; only the one
// matching the configured GOARCH may reach the type-checker, or the
// lint action reports a spurious "hexExpand redeclared" finding.
package tagged

// Expand returns the arch-specific expansion of s.
func Expand(s string) string { return hexExpand(s) }
