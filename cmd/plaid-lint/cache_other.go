// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package main

import "os"

// diskUsage falls back to logical fi.Size() on platforms whose
// os.FileInfo.Sys() doesn't return a POSIX *syscall.Stat_t (Windows).
// The reported total will be smaller than `du -sh` would show on Unix
// but is still a useful approximation.
func diskUsage(fi os.FileInfo) int64 { return fi.Size() }
