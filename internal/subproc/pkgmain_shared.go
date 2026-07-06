// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// readGoSumHash returns the sha256 of the workspace's go.sum file, or
// the empty string if go.sum is absent. Missing go.sum is not an
// error — a single-package workspace with no deps is legitimate.
// Used by every `package main` subprocess-wrap runner to fold the
// module graph into linterVersion.
//
// Mirrors the unparam/unused linterVersion go.sum fold. Extracted
// so the eight package-main wrap runners
// share one implementation rather than each re-implementing the
// missing-file dance.
func readGoSumHash(ws WorkspaceRef) (string, error) {
	if ws.ModuleRoot == "" {
		return "", nil
	}
	sumPath := filepath.Join(ws.ModuleRoot, "go.sum")
	sumHash, err := fileSha256(sumPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return sumHash, nil
}

// skipDirNames are the directory names every package-main subprocess
// wrap runner skips when enumerating .go files from a workspace root.
// `./...` passed to a Go binary that resolves it via filepath-walk
// (rather than module-aware go/packages) drags in `.git/logs/...`
// reflog files whose paths can end in `.go` when a feature branch is
// named after a Go file — those files are zero-byte and trip the Go
// parser. The skip list also drops vendor/, testdata/, common
// IDE-state dirs, and node_modules (in case a monorepo mixes Go with
// a JS package).
//
// Mirrors WorkspaceContentHash's skip rules so cache identity and
// runner input agree on what counts as workspace content.
var skipDirNames = map[string]bool{
	".git":         true,
	".idea":        true,
	".vscode":      true,
	"node_modules": true,
	"testdata":     true,
	"vendor":       true,
}

// enumerateGoFiles returns paths to every `.go` file under root,
// skipping directories named in [skipDirNames]. Paths are relative to
// root and sorted for deterministic argv ordering.
//
// Used by every package-main subprocess wrap runner whose upstream
// CLI resolves bare path args via filepath-walk (gochecknoinits,
// dupl, gocyclo, lll, nestif). Module-aware runners (dogsled,
// unconvert, unparam) pass `./...` directly because go/packages
// already skips dot-dirs and never crosses into `.git/`.
func enumerateGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// argvChunkBudgetBytes bounds the combined length of positional
// argv strings handed to a single chunked invocation. Linux's
// ARG_MAX is ~2 MiB (kernel) less ~128 KiB of environment, so 96
// KiB leaves comfortable headroom for the binary path, flags, and
// any env grown by callers. On c1 (~10K Go files, avg path ~92 B)
// the unchunked argv was ~800 KiB and tripped E2BIG.
const argvChunkBudgetBytes = 96 * 1024

// chunkArgv splits a positional-arg slice into chunks so each chunk's
// joined byte length stays under [argvChunkBudgetBytes]. Single args
// larger than the budget become their own chunk (callers can't help
// us with a path that's bigger than ARG_MAX by itself; Linux's
// MAX_ARG_STRLEN is 128 KiB and no realistic Go file path approaches
// that). Returns the input slice in [][]string form preserving order.
func chunkArgv(args []string) [][]string {
	if len(args) == 0 {
		return nil
	}
	var chunks [][]string
	var cur []string
	curBytes := 0
	for _, a := range args {
		// +1 accounts for the NUL separator the kernel adds between
		// argv entries — argv is laid out as packed C strings, so the
		// effective per-arg cost is len(a)+1.
		cost := len(a) + 1
		if len(cur) > 0 && curBytes+cost > argvChunkBudgetBytes {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, a)
		curBytes += cost
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// chunkedInvokeResult is the merged output of a [chunkedInvoke] call.
// stdout and stderr are concatenated in input-chunk order with a
// single newline guaranteed between chunks (no merge across chunk
// boundaries — parsers run line-by-line). exitCode is the highest
// code seen across chunks; the first non-context error wins.
type chunkedInvokeResult struct {
	stdout, stderr []byte
	exitCode       int
	err            error
}

// chunkedInvoke invokes binary once per [chunkArgv] chunk under cwd
// with env extension, passing [flags...positional_chunk] as argv.
// Used by every wrap runner whose upstream CLI takes positional file
// or directory args — the file walk in c1-scale workspaces produces
// argv larger than ARG_MAX, so the file list is split into chunks
// and the per-chunk outputs are stitched.
//
// Behavior:
//
//   - stdout / stderr from each chunk are concatenated in input
//     order. Parsers in this package are line-based (one diagnostic
//     per line; lines never span chunks), so concatenation is safe.
//   - exitCode is the maximum across chunks. A chunk that reports
//     findings via non-zero exit (gochecknoinits, gocyclo) lifts the
//     aggregate exit accordingly.
//   - The first non-nil invokeErr wins, but the per-chunk stdout
//     captured so far is still returned so the parser can surface
//     real findings alongside the error context.
//   - If ctx is cancelled mid-loop, the loop bails and the
//     partial result is returned with the ctx error.
//
// Callers pass the binary's flag args in fixedFlags; positional
// chunked args follow per invocation. The order is
// `binary fixedFlags... chunk...`.
func chunkedInvoke(ctx context.Context, binary, cwd string, env, fixedFlags, positional []string) chunkedInvokeResult {
	chunks := chunkArgv(positional)
	if len(chunks) == 0 {
		return chunkedInvokeResult{}
	}
	var outBuf, errBuf bytes.Buffer
	var firstErr error
	maxExit := 0
	for _, chunk := range chunks {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if firstErr == nil {
				firstErr = ctxErr
			}
			break
		}
		args := make([]string, 0, len(fixedFlags)+len(chunk))
		args = append(args, fixedFlags...)
		args = append(args, chunk...)
		stdout, stderr, code, err := invokeInDir(ctx, binary, cwd, env, args)
		if len(stdout) > 0 {
			if outBuf.Len() > 0 && !bytes.HasSuffix(outBuf.Bytes(), []byte{'\n'}) {
				outBuf.WriteByte('\n')
			}
			outBuf.Write(stdout)
		}
		if len(stderr) > 0 {
			if errBuf.Len() > 0 && !bytes.HasSuffix(errBuf.Bytes(), []byte{'\n'}) {
				errBuf.WriteByte('\n')
			}
			errBuf.Write(stderr)
		}
		if code > maxExit {
			maxExit = code
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return chunkedInvokeResult{
		stdout:   outBuf.Bytes(),
		stderr:   errBuf.Bytes(),
		exitCode: maxExit,
		err:      firstErr,
	}
}

// invokeWithStdinFiles invokes binary under cwd with env extension,
// writing the newline-separated file paths to the child's stdin.
// Used by wrap runners whose upstream CLI supports a file-list-via-
// stdin flag (`dupl -files`, `lll --files`). A single invocation
// covers the whole workspace, sidestepping argv chunking.
//
// Capture and error taxonomy mirror [invokeInDir] exactly; stdin
// is drained fully before the child is allowed to exit.
func invokeWithStdinFiles(ctx context.Context, binary, cwd string, env, args, files []string) (stdout, stderr []byte, exitCode int, err error) {
	var stdin bytes.Buffer
	for _, f := range files {
		stdin.WriteString(f)
		stdin.WriteByte('\n')
	}
	return invokeInDirWithStdin(ctx, binary, cwd, env, args, &stdin)
}

// enumerateGoDirs returns the set of directories under root that
// contain at least one `.go` file, skipping directories named in
// [skipDirNames]. Paths are relative to root, sorted, and deduped.
//
// Used by godox, whose upstream CLI consumes one or more directory
// arguments and calls `go/parser.ParseDir` on each (non-recursive
// at each given dir).
func enumerateGoDirs(root string) ([]string, error) {
	files, err := enumerateGoFiles(root)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(files))
	dirs := make([]string, 0, len(files))
	for _, f := range files {
		d := filepath.Dir(f)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs, nil
}
