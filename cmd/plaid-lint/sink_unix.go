// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package main

import (
	"os"
	"syscall"
)

// openOutputFile creates (truncating) a report file for writing. The
// O_NOFOLLOW flag makes the open fail rather than follow a symlink at the
// final path component, so an attacker-controlled checkout cannot redirect
// the report to a file outside the working tree by planting a symlink at
// the destination. The path is already confined to the working tree by
// resolveConfinedOutputPath before reaching here.
func openOutputFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
}
