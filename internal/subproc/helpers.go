// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// sha256File returns the lower-hex sha256 of the file at path. The
// caller distinguishes "missing file" via errors.Is(err, fs.ErrNotExist).
//
// Originated in [UnusedRunner.linterVersion]. Retained here
// because [CustomRunner.linterVersion] still consumes it.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileSha256 is an alias of [sha256File] kept for backwards
// compatibility with [UnparamRunner.linterVersion]'s spelling, still
// used by [DuplRunner] and [pkgmain_shared].
func fileSha256(p string) (string, error) { return sha256File(p) }

// summarizeStderr trims stderr to a single-line summary suitable for
// embedding in a [ParseError].Detail or [InvokeError]. Long upstream
// panics get truncated to keep error messages tractable.
func summarizeStderr(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
