// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/conductorone/plaid-lint/internal/unit"
)

// runStdlib executes `plaid-lint stdlib`: type-check a Go SDK's
// standard library from source and write deep export data per package
// in the layout `unit`'s deps.stdlib_dir resolves. The build-system
// replacement for compiling the standard library just to read its
// types.
func (a *app) runStdlib(args []string) int {
	fs := newRunFlagSet("stdlib", a.stderr)
	g := bindGlobalFlags(fs)
	goroot := fs.String("goroot", "", "Go SDK root whose src/ is exported (required)")
	goos := fs.String("goos", "", "target GOOS (required)")
	goarch := fs.String("goarch", "", "target GOARCH (required)")
	tags := fs.String("tags", "", "comma-separated build tags")
	cgo := fs.Bool("cgo", false, "select files as with CGO_ENABLED=1")
	goVersion := fs.String("go-version", "", "SDK language version, e.g. 1.26 (default: read GOROOT/VERSION)")
	out := fs.String("out", "", "output directory; files land at <out>/<goos>_<goarch>/<importpath>.a (required)")
	jobs := fs.Int("jobs", 0, "type-checking parallelism (default GOMAXPROCS)")

	args, aerr := expandArgsFiles(args)
	if aerr != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: stdlib: %v\n", aerr)
		return exitCLIError
	}
	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		printStdlibHelp(a.stdout)
		return exitSuccess
	}
	if *goroot == "" || *goos == "" || *goarch == "" || *out == "" {
		fmt.Fprintln(a.stderr, "plaid-lint: stdlib: --goroot, --goos, --goarch and --out are required")
		return exitCLIError
	}
	var tagList []string
	for t := range strings.SplitSeq(*tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tagList = append(tagList, t)
		}
	}
	res, err := unit.ExportStdlib(context.Background(), unit.StdlibConfig{
		GOROOT:    *goroot,
		GOOS:      *goos,
		GOARCH:    *goarch,
		Tags:      tagList,
		Cgo:       *cgo,
		GoVersion: *goVersion,
		Out:       *out,
		Jobs:      *jobs,
		Files:     fs.Args(),
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitInternalError
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: stdlib: %s\n", w)
	}
	if g.Verbose {
		fmt.Fprintf(a.stderr, "plaid-lint: stdlib: exported %d packages, skipped %d\n", len(res.Packages), len(res.Skipped))
		for dir, why := range res.Skipped {
			fmt.Fprintf(a.stderr, "plaid-lint: stdlib: skipped %s: %s\n", dir, why)
		}
	}
	return exitSuccess
}

func printStdlibHelp(w io.Writer) {
	fmt.Fprintln(w, `plaid-lint stdlib — write standard-library export data for unit actions.

Usage:
  plaid-lint stdlib --goroot DIR --goos OS --goarch ARCH --out DIR [flags] [FILE...]

Type-checks GOROOT/src (excluding cmd/) with go/types in dependency
order and writes deep gcexportdata for every package to
<out>/<goos>_<goarch>/<importpath>.a — the tree a unit action's
deps.stdlib_dir names. Reads only GOROOT/src and GOROOT/VERSION; no
go command, no compiled packages. FILE arguments (typically through an
@params file) restrict the run to exactly those SDK sources, so the
result does not depend on whether the executor sandboxes the action.

Flags:
      --goroot string      Go SDK root (required)
      --goos string        target GOOS (required)
      --goarch string      target GOARCH (required)
      --tags string        comma-separated build tags
      --cgo                select files as with CGO_ENABLED=1
      --go-version string  SDK language version (default: GOROOT/VERSION)
      --out string         output directory (required)
      --jobs int           type-checking parallelism (default GOMAXPROCS)`)
}
