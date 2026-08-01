// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseImportcfg reads a compiler-style importcfg file and returns the
// importpath → export-data-file map from its `packagefile` lines.
//
// The format is the one `go build` writes for `compile -importcfg` and
// rules_go writes for GoCompilePkg:
//
//	# comment
//	packagefile fmt=/path/to/fmt.a
//	importmap old=new
//
// `importmap` lines rewrite import paths (vendoring, test archives):
// an import of `old` resolves through the entry for `new`. Both
// spellings end up as keys in the returned map so the importer can
// look up either.
func parseImportcfg(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unit: open importcfg: %w", err)
	}
	defer f.Close()

	files := make(map[string]string)
	importmap := make(map[string]string)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, rest, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("unit: importcfg %s:%d: malformed line %q", path, ln, line)
		}
		switch verb {
		case "packagefile":
			ip, file, ok := strings.Cut(rest, "=")
			if !ok || ip == "" || file == "" {
				return nil, fmt.Errorf("unit: importcfg %s:%d: malformed packagefile %q", path, ln, rest)
			}
			files[ip] = file
		case "importmap":
			from, to, ok := strings.Cut(rest, "=")
			if !ok || from == "" || to == "" {
				return nil, fmt.Errorf("unit: importcfg %s:%d: malformed importmap %q", path, ln, rest)
			}
			importmap[from] = to
		case "packageshlib", "modinfo":
			// Recognized-but-unused verbs from the go command's
			// importcfg vocabulary.
		default:
			return nil, fmt.Errorf("unit: importcfg %s:%d: unknown verb %q", path, ln, verb)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("unit: read importcfg: %w", err)
	}

	// Resolve importmap aliases: an import of `from` uses `to`'s file.
	for from, to := range importmap {
		if file, ok := files[to]; ok {
			if _, exists := files[from]; !exists {
				files[from] = file
			}
		}
	}
	return files, nil
}
