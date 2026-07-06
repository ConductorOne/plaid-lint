// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"sort"
	"strings"
	"sync"
)

// catalog is the per-process static linter inventory. Populated at
// init time from [seedCatalog] + [wireAnalyzerFns]. Tests that want a
// custom view construct one via [NewTestRegistry].
//
// The catalog is read-only after init; the locking is purely for the
// "init from multiple files in parallel" defensive case during package
// load.
type catalog struct {
	mu      sync.RWMutex
	byName  map[string]*Entry
	byAlias map[string]string // alias → canonical name
	all     []*Entry          // stable order: alphabetical by Name
}

// defaultCatalog is the process-global catalog used by [Build] when
// the caller doesn't pass an explicit *Catalog. The shape mirrors
// `analyzers.BundledRegistry`: a single global is fine because all
// catalog entries are immutable once seeded.
var defaultCatalog = newCatalog()

func newCatalog() *catalog {
	return &catalog{
		byName:  make(map[string]*Entry),
		byAlias: make(map[string]string),
	}
}

// add inserts e into the catalog. Panics on duplicate canonical name
// because that's a programming error, not user input.
func (c *catalog) add(e *Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byName[e.Name]; ok {
		panic("registry: duplicate linter name " + e.Name)
	}
	c.byName[e.Name] = e
	c.all = append(c.all, e)
	for _, a := range e.Aliases {
		if _, ok := c.byAlias[a]; ok {
			panic("registry: duplicate linter alias " + a)
		}
		c.byAlias[a] = e.Name
	}
}

// resolve maps a user-supplied linter name to a catalog entry,
// following aliases. Returns (entry, true) on hit, (nil, false) on
// miss. Names are matched case-insensitively to mirror upstream
// (`Errcheck` ≡ `errcheck`).
func (c *catalog) resolve(name string) (*Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := strings.ToLower(name)
	if e, ok := c.byName[n]; ok {
		return e, true
	}
	if canon, ok := c.byAlias[n]; ok {
		return c.byName[canon], true
	}
	return nil, false
}

// names returns every canonical + alias name as a sorted slice.
// Used for the did-you-mean suggestion in [Validate].
func (c *catalog) names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.byName)+len(c.byAlias))
	for n := range c.byName {
		out = append(out, n)
	}
	for a := range c.byAlias {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// entries returns a copy of the catalog's entries in stable
// alphabetical order. Callers may mutate the slice (it's a copy), but
// the *Entry pointers point at the shared catalog rows — do not
// mutate fields on them.
func (c *catalog) entries() []*Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Entry, len(c.all))
	copy(out, c.all)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// seed populates the catalog with the upstream linter set. The
// per-linter Entry definitions are split into [seedCatalog] (catalog
// metadata) and [wireAnalyzerFns] (the runtime analyzer hookup).
// Splitting the two keeps the inventory readable.
func (c *catalog) seed() {
	for _, row := range seedRows() {
		groups := map[Group]bool{
			GroupNone:     false,
			GroupStandard: row.standard,
			GroupFast:     row.fast,
			GroupAll:      true, // every non-deprecated linter is in `all`
		}
		// Deprecated linters drop out of the `all` group so they don't
		// accidentally light up by default.
		if row.deprecated != "" {
			groups[GroupAll] = false
		}
		e := &Entry{
			Name:             row.name,
			Aliases:          row.aliases,
			Shape:            row.shape,
			InGroup:          groups,
			Deprecated:       row.deprecated,
			HasGoVersion:     row.hasGoVersion,
			SubprocessWired:  row.subprocessWired,
		}
		c.add(e)
	}
	wireAnalyzerFns(c)
}

// seedRow is the input shape consumed by catalog.seed. Kept private —
// the public surface is the Entry it produces.
type seedRow struct {
	name             string
	aliases          []string
	shape            Shape
	standard         bool   // member of the v2 `standard` default group
	fast             bool   // member of the v2 `fast` default group
	deprecated       string // empty if not deprecated
	hasGoVersion     bool   // wants Run.Go propagation
	subprocessWired  bool   // ShapeSubprocess with a Runner wired in engine.planFromRegistry
}

func init() {
	defaultCatalog.seed()
}
