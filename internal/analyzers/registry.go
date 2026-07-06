// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"fmt"
	"sync"

	"golang.org/x/tools/go/analysis"
)

// Registry holds the set of AnalyzerDescriptors plaid-lint knows
// about. Tests construct a Registry directly; the production binary
// uses the package-level default (BundledRegistry) populated by the
// init() in bundled.go.
type Registry struct {
	mu    sync.RWMutex
	byPtr map[*analysis.Analyzer]*AnalyzerDescriptor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byPtr: make(map[*analysis.Analyzer]*AnalyzerDescriptor)}
}

// Register adds d to the registry, keyed on d.Analyzer. Replaces any
// previous descriptor for the same analyzer pointer. Panics if
// d.Analyzer is nil or d.ConfigSalt is nil — those are required
// schema fields.
//
// If d.AnalyzerVersion is empty, the registry fills it with the
// process binary version (see ProcessBinaryVersion). All bundled
// analyzers share the same binary version because they all live in
// the plaid-lint executable.
func (r *Registry) Register(d *AnalyzerDescriptor) {
	if d == nil {
		panic("analyzers: Register(nil)")
	}
	if d.Analyzer == nil {
		panic("analyzers: Register: descriptor has nil Analyzer")
	}
	if d.ConfigSalt == nil {
		panic(fmt.Sprintf("analyzers: Register %q: descriptor has nil ConfigSalt", d.Analyzer.Name))
	}
	if d.AnalyzerVersion == "" {
		d.AnalyzerVersion = ProcessBinaryVersion()
	}
	r.mu.Lock()
	r.byPtr[d.Analyzer] = d
	r.mu.Unlock()
}

// Lookup returns the descriptor for a, or nil if a is not registered.
func (r *Registry) Lookup(a *analysis.Analyzer) *AnalyzerDescriptor {
	if r == nil || a == nil {
		return nil
	}
	r.mu.RLock()
	d := r.byPtr[a]
	r.mu.RUnlock()
	return d
}

// All returns a snapshot of all registered descriptors. The slice
// order is unspecified.
func (r *Registry) All() []*AnalyzerDescriptor {
	r.mu.RLock()
	out := make([]*AnalyzerDescriptor, 0, len(r.byPtr))
	for _, d := range r.byPtr {
		out = append(out, d)
	}
	r.mu.RUnlock()
	return out
}

// BundledRegistry is the process-global registry populated at init
// time by the bundled descriptors. Production callers (the gopls
// fork's analysis driver) consult this registry; tests can pass a
// custom Registry through the L1 wiring.
var BundledRegistry = NewRegistry()
