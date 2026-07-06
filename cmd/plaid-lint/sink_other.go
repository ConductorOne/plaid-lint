// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package main

import "os"

// openOutputFile creates (truncating) a report file for writing. Platforms
// without O_NOFOLLOW (Windows) fall back to a plain create; the path is
// still confined to the working tree by resolveConfinedOutputPath, and the
// symlinked-directory re-check there covers redirection via parent dirs.
func openOutputFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}
