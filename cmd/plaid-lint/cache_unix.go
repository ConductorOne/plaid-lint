// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package main

import (
	"os"
	"syscall"
)

// diskUsage returns the on-disk block-allocated bytes for fi, matching
// `du -sh` semantics: each small file still occupies a full filesystem
// block (typically 4 KiB on APFS/ext4), so for caches with many tiny
// L1 entries summed fi.Size() can understate actual disk pressure by
// ~50%. The block count comes from POSIX stat()'s st_blocks, always
// reported in 512-byte units regardless of the underlying block size.
func diskUsage(fi os.FileInfo) int64 {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(stat.Blocks) * 512
	}
	return fi.Size()
}
