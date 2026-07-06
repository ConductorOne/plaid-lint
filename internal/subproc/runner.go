// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"context"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// Runner abstracts subprocess invocation for the whole-program
// linters carved out for subprocess execution (unused, unparam, custom plugins).
//
// One Runner is constructed per linter. The engine calls Run with the
// resolved config and a [WorkspaceRef] identifying where the
// subprocess should discover packages; the Runner is responsible for
// honoring opt-out (via [SkipResult]), consulting the [Cache], and
// translating the subprocess's native output into
// [output.Diagnostic] values per the rules in diagnostic.go.
type Runner interface {
	// Name returns the linter name as it appears in the user's
	// `linters.enable` (e.g. "unused", "unparam"). It is the value
	// surfaced through [output.Diagnostic.Linter] for every emitted
	// diagnostic regardless of what the underlying subprocess emits.
	Name() string

	// Run invokes the subprocess (or returns cached results) for
	// workspace under cfg. Run obeys ctx cancellation. On a clean
	// no-op (cache hit replay, opt-out, disabled-by-config) Run
	// returns the cached / empty diagnostic slice and a nil error.
	Run(ctx context.Context, cfg *config.Config, workspace WorkspaceRef) ([]output.Diagnostic, error)
}

// WorkspaceRef carries the inputs a subprocess wrapper needs to
// discover packages and reproduce a deterministic environment. The
// external subprocess does its own `go list ./...`-style discovery;
// WorkspaceRef tells it where to discover and which build tags /
// envs to use.
//
// WorkspaceRef is also the workspace-identity input to [CacheKey];
// see workspaceContentHash for the hashing rules.
type WorkspaceRef struct {
	// ModuleRoot is the absolute path to the module root. The
	// subprocess is spawned with this as its working directory.
	ModuleRoot string

	// BuildTags are propagated from cfg.Run.BuildTags. Each wrapper
	// is responsible for translating these into the form its
	// subprocess expects (typically `-tags=a,b,c`).
	BuildTags []string

	// Env contains additional KEY=VALUE entries appended to the
	// inherited process environment. The wrapper typically sets at
	// least CGO_ENABLED=0 and (when running under a Squire dev box)
	// the GOCACHE / GOMODCACHE overrides.
	Env []string
}

// SkipResult is the canonical zero-diagnostic, nil-error return value
// for the "0 diagnostics, not an error" path: cache hit replay with
// an empty list, opt-out via registry Status != StatusEnabled, or
// per-linter "no packages matched" early exit.
//
// Wrappers should prefer returning SkipResult() over manually
// constructing nil so the intent is grep-able.
func SkipResult() ([]output.Diagnostic, error) {
	return nil, nil
}
