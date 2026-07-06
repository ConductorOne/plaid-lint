// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import (
	"sync"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// Stream is a per-Run streaming view over a Filter. Callers add
// diagnostics one package at a time as the analyzer driver produces
// them; the Stream applies every filter stage immediately and returns
// the kept diagnostics, so the engine never accumulates an
// all-diagnostics buffer before filtering. Per-file caches (nolint
// ranges, generated-file detection) are released by Finish.
//
// A Stream is safe for concurrent calls to AddPackage and AddBatch.
// Finish is not safe to call concurrently with AddPackage.
//
// The Filter the Stream wraps must outlive the Stream.
type Stream struct {
	f *Filter

	// uniqMu guards uniqSeen against concurrent AddPackage calls.
	uniqMu   sync.Mutex
	uniqSeen map[fileLineKey]struct{}
}

// fileLineKey is the (file, line) tuple for uniq-by-line dedup.
type fileLineKey struct {
	file string
	line int
}

// NewStream returns a fresh Stream backed by f. A nil receiver returns
// nil; callers should treat a nil Stream as "no filter pass" the same
// way Apply does.
func (f *Filter) NewStream() *Stream {
	if f == nil {
		return nil
	}
	s := &Stream{f: f}
	if f.uniqByLine {
		s.uniqSeen = make(map[fileLineKey]struct{})
	}
	return s
}

// AddPackage applies the filter to one package's diagnostics and
// returns the kept slice. The pkgID is informational (used for trace
// hooks); it is NOT folded into the filter decision. Returns an empty
// slice — never nil — so callers can range freely.
//
// If the receiver is nil, the input is returned unchanged: nil-Stream
// matches the nil-Filter pass-through contract.
func (s *Stream) AddPackage(_ string, diags []output.Diagnostic) []output.Diagnostic {
	if s == nil {
		return diags
	}
	if len(diags) == 0 {
		return diags
	}
	f := s.f

	// Fast-path: no stage of the filter is active. Mirrors the
	// equivalent check in Apply so the streaming and batch paths agree
	// on "no-op" semantics.
	if !f.anyStageActive() {
		return diags
	}

	out := make([]output.Diagnostic, 0, len(diags))
	for _, d := range diags {
		if f.dropDiagnostic(d) {
			continue
		}
		if f.uniqByLine {
			k := fileLineKey{file: d.Pos.Filename, line: d.Pos.Line}
			s.uniqMu.Lock()
			if _, dup := s.uniqSeen[k]; dup {
				s.uniqMu.Unlock()
				continue
			}
			s.uniqSeen[k] = struct{}{}
			s.uniqMu.Unlock()
		}
		out = append(out, d)
	}
	return out
}

// AddBatch applies the filter to an unowned batch of diagnostics —
// typically the side-channel "no owning package" set the engine
// produces. Same semantics as AddPackage but with no pkgID hook.
func (s *Stream) AddBatch(diags []output.Diagnostic) []output.Diagnostic {
	return s.AddPackage("", diags)
}

// Finish releases the per-file caches the filter accumulated during
// the streaming run (nolint AST maps, generated-file detection
// results). Safe to call multiple times.
//
// On a nil receiver Finish is a no-op.
func (s *Stream) Finish() {
	if s == nil {
		return
	}
	f := s.f
	if f == nil {
		return
	}
	// generated-file cache: drop the map so the underlying go/ast
	// FileSet + comment maps become unreachable.
	f.generatedCacheMu.Lock()
	f.generatedCache = map[string]bool{}
	f.generatedCacheMu.Unlock()

	// nolint per-file cache: same idea. The nolint filter holds
	// `[]ignoredRange` per file; replacing the map drops both the
	// ranges and (transitively) the go/parser FileSet handles they
	// were derived from.
	if f.nolint != nil {
		f.nolint.cacheMu.Lock()
		f.nolint.cache = map[string][]ignoredRange{}
		f.nolint.cacheMu.Unlock()
	}

	// uniqSeen index: drop the dedup set. Held only for the Run.
	s.uniqMu.Lock()
	s.uniqSeen = nil
	s.uniqMu.Unlock()
}

// anyStageActive reports whether any filter stage would actually drop
// or transform a diagnostic. Mirrors the inverted check in
// Apply.
func (f *Filter) anyStageActive() bool {
	if f == nil {
		return false
	}
	if len(f.pathPatterns) > 0 || len(f.pathExceptPatterns) > 0 || len(f.rules) > 0 {
		return true
	}
	if f.generatedMode != "" && f.generatedMode != config.GeneratedModeDisable {
		return true
	}
	if len(f.staticcheckDefaultDisabled) > 0 || len(f.targetDirs) > 0 {
		return true
	}
	if f.uniqByLine {
		return true
	}
	if f.nolint != nil {
		return true
	}
	return false
}
