// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
)

// runCache dispatches `plaid-lint cache <subcmd>`.
//
// T2.4 stub: the cache root resolution mirrors upstream's discovery
// order (XDG_CACHE_HOME → ~/.cache → tmp fallback), but the actual
// cache management is engine-side and lands in Phase 3. The clean
// subcommand removes the directory if it exists; status prints
// metadata about what would be cleaned.
func (a *app) runCache(args []string) int {
	if len(args) == 0 {
		printCacheHelp(a.stdout, a.stderr)
		return exitCLIError
	}
	first := args[0]
	rest := args[1:]
	switch first {
	case "clean":
		return a.runCacheClean(rest)
	case "status":
		return a.runCacheStatus(rest)
	case "--help", "-h", "help":
		printCacheHelp(a.stdout, a.stderr)
		return exitSuccess
	default:
		fmt.Fprintf(a.stderr, "plaid-lint cache: unknown subcommand %q\n", first)
		printCacheHelp(a.stdout, a.stderr)
		return exitCLIError
	}
}

// cacheRoot returns the resolved path of the on-disk cache root,
// delegating to clcache.DefaultRoot so the CLI and the engine agree on
// where to read and write. See clcache.DefaultRoot for resolution order.
func cacheRoot() string {
	// clcache.DefaultRoot's error path is unreachable in practice (it
	// always falls back to os.TempDir); discard the err and trust the
	// returned string.
	root, _ := clcache.DefaultRoot()
	return root
}

func (a *app) runCacheClean(_ []string) int {
	root := cacheRoot()
	if _, err := os.Stat(root); err == nil {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(a.stderr, "plaid-lint cache: clean %s: %v\n", root, err)
			return exitInternalError
		}
		fmt.Fprintf(a.stdout, "Cleaned cache at %s\n", root)
		return exitSuccess
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(a.stderr, "plaid-lint cache: stat %s: %v\n", root, err)
		return exitInternalError
	}
	fmt.Fprintf(a.stdout, "No cache to clean at %s\n", root)
	return exitSuccess
}

func (a *app) runCacheStatus(_ []string) int {
	root := cacheRoot()
	fmt.Fprintf(a.stdout, "Dir: %s\n", root)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(a.stdout, "Size: 0 B (cache not yet populated)")
			return exitSuccess
		}
		fmt.Fprintf(a.stderr, "plaid-lint cache: stat %s: %v\n", root, err)
		return exitInternalError
	}
	if !info.IsDir() {
		fmt.Fprintf(a.stdout, "Size: ??? (cache root is not a directory)\n")
		return exitSuccess
	}
	var size int64
	var count int64
	_ = filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		if !fi.IsDir() {
			size += diskUsage(fi)
			count++
		}
		return nil
	})
	fmt.Fprintf(a.stdout, "Size: %s on disk (%d files)\n", humanizeBytes(size), count)
	return exitSuccess
}

// humanizeBytes formats a byte count in 1024-based units matching the
// du -h convention ("256K", "1.8G", "0 B" for sub-K values). One
// decimal place above K avoids "1024K" / "1024M" rollover ambiguity.
func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

func printCacheHelp(stdout, _ interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(stdout, `Cache control and information.

Usage:
  plaid-lint cache [command]

Available Commands:
  clean    Clean cache
  status   Show cache status

Global Flags:
      --color string   Use color when printing (default "auto")
  -v, --verbose        Verbose output
  -h, --help           Help for a command`)
}
