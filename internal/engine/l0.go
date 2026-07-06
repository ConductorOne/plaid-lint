// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/l0"
)

// l0Context bundles the per-Run state needed to compute L0 keys for
// every workspace package. It is constructed once at the top of
// runInProcess and reused for each package's key derivation.
type l0Context struct {
	// snap is the cache snapshot used to read file contents (we need
	// each compiled-Go-file's hash for the package's SourceHash).
	snap *cache.Snapshot

	// graph caches every metadata.Package the loader produced so dep
	// closures can be walked without repeated Snapshot.Metadata calls.
	graph map[metadata.PackageID]*metadata.Package

	// sourceHashCache memoises per-package source-file hashes so a
	// package visited multiple times in dep walks isn't re-hashed.
	sourceHashCache map[metadata.PackageID][32]byte

	// depHashCache memoises per-package transitive-dep hashes.
	depHashCache map[metadata.PackageID][32]byte

	// analyzerSetHash is the digest over the enabled analyzer set
	// (name, version, configSalt) — identical for every package in a
	// given Run.
	analyzerSetHash [32]byte

	// filterConfigHash is the digest over the effective suppression /
	// exclusion configuration (exclusion.Filter.ConfigDigest). Folded
	// into every package's L0 key so a change to suppression rules
	// invalidates the cached POST-filter diagnostic stream. Identical
	// for every package in a given Run.
	filterConfigHash [32]byte

	// toolVersion / buildEnv / goVersion mirror RunInput plumbing.
	toolVersion string
	buildEnv    string
	goVersion   string
}

// newL0Context builds an l0Context for the given snapshot + plan +
// resolved Run identity. Returns nil if L0 cannot be derived for this
// run (e.g. metadata graph empty); callers treat nil as "skip L0,
// fall back to direct Analyze".
func newL0Context(
	ctx context.Context,
	snap *cache.Snapshot,
	plan *runPlan,
	registry *analyzers.Registry,
	filterConfigHash [32]byte,
	toolVersion, buildEnv, goVersion string,
) (*l0Context, error) {
	all, err := snap.AllMetadata(ctx)
	if err != nil {
		return nil, err
	}
	graph := make(map[metadata.PackageID]*metadata.Package, len(all))
	for _, mp := range all {
		if mp != nil {
			graph[mp.ID] = mp
		}
	}
	return &l0Context{
		snap:             snap,
		graph:            graph,
		sourceHashCache:  make(map[metadata.PackageID][32]byte, len(graph)),
		depHashCache:     make(map[metadata.PackageID][32]byte, len(graph)),
		analyzerSetHash:  computeAnalyzerSetHash(plan, registry),
		filterConfigHash: filterConfigHash,
		toolVersion:      toolVersion,
		buildEnv:         buildEnv,
		goVersion:        goVersion,
	}, nil
}

// keyFor computes the L0 key for mp. Returns the zero ActionID and
// false if any required input (e.g. a compiled-go-file) couldn't be
// hashed — in that case the engine treats this package as a miss and
// skips L0 caching for it on this run.
func (lc *l0Context) keyFor(ctx context.Context, mp *metadata.Package) (clcache.ActionID, bool) {
	if mp == nil {
		return clcache.ActionID{}, false
	}
	src, ok := lc.sourceHashOf(ctx, mp)
	if !ok {
		return clcache.ActionID{}, false
	}
	dep, ok := lc.depHashOf(ctx, mp)
	if !ok {
		return clcache.ActionID{}, false
	}
	parts := l0.KeyParts{
		PackageID:        string(mp.ID),
		PackagePath:      string(mp.PkgPath),
		SourceHash:       src,
		DepHash:          dep,
		AnalyzerSetHash:  lc.analyzerSetHash,
		FilterConfigHash: lc.filterConfigHash,
		ToolVersion:      lc.toolVersion,
		BuildEnv:         lc.buildEnv,
		GoVersion:        lc.goVersion,
	}
	return l0.ComputeKey(parts), true
}

// sourceHashOf returns a deterministic hash over mp's compiled-Go-file
// contents. Uses the snapshot's ReadFile to honor any file overlays.
//
// The "URI" folded into the hash is reduced to filepath.Base
// so the resulting source-hash is machine-portable. The pkgPath
// already participates in the higher-level L0 key (KeyParts.PackagePath),
// so identifying files by basename inside a package is enough to
// disambiguate (each Go file's basename is unique within its package).
func (lc *l0Context) sourceHashOf(ctx context.Context, mp *metadata.Package) ([32]byte, bool) {
	if h, ok := lc.sourceHashCache[mp.ID]; ok {
		return h, true
	}
	uris := make([]string, 0, len(mp.CompiledGoFiles))
	hashes := make([][32]byte, 0, len(mp.CompiledGoFiles))
	for _, uri := range mp.CompiledGoFiles {
		fh, err := lc.snap.ReadFile(ctx, uri)
		if err != nil {
			return [32]byte{}, false
		}
		ident := fh.Identity()
		uris = append(uris, filepath.Base(uri.Path()))
		hashes = append(hashes, [32]byte(ident.Hash))
	}
	h := l0.HashFiles(uris, hashes)
	lc.sourceHashCache[mp.ID] = h
	return h, true
}

// depHashOf returns a deterministic hash over mp's transitive dep
// closure (PackageID + source hash). Cycles are broken by the
// in-progress set tracked across the recursion.
func (lc *l0Context) depHashOf(ctx context.Context, mp *metadata.Package) ([32]byte, bool) {
	if h, ok := lc.depHashCache[mp.ID]; ok {
		return h, true
	}
	closure := make(map[metadata.PackageID]struct{})
	visiting := make(map[metadata.PackageID]struct{})
	if !lc.collectClosure(ctx, mp, closure, visiting) {
		return [32]byte{}, false
	}
	delete(closure, mp.ID) // dep closure excludes self

	ids := make([]string, 0, len(closure))
	hashes := make([][32]byte, 0, len(closure))
	for id := range closure {
		dep := lc.graph[id]
		if dep == nil {
			// Missing dep metadata — treat as cache-blocker so we
			// don't write an L0 entry that omits real inputs.
			return [32]byte{}, false
		}
		src, ok := lc.sourceHashOf(ctx, dep)
		if !ok {
			return [32]byte{}, false
		}
		ids = append(ids, string(id))
		hashes = append(hashes, src)
	}
	out := l0.HashDepClosure(ids, hashes)
	lc.depHashCache[mp.ID] = out
	return out, true
}

// collectClosure walks DepsByPkgPath transitively, populating closure
// with every reachable PackageID (including self). Returns false if a
// dep can't be resolved — the engine then skips L0 for the root.
func (lc *l0Context) collectClosure(
	ctx context.Context,
	mp *metadata.Package,
	closure, visiting map[metadata.PackageID]struct{},
) bool {
	if _, seen := closure[mp.ID]; seen {
		return true
	}
	if _, cycle := visiting[mp.ID]; cycle {
		return true // break cycles silently
	}
	visiting[mp.ID] = struct{}{}
	closure[mp.ID] = struct{}{}
	for _, depID := range mp.DepsByPkgPath {
		dep := lc.graph[depID]
		if dep == nil {
			continue // missing dep — caller handles via depHashOf
		}
		if !lc.collectClosure(ctx, dep, closure, visiting) {
			return false
		}
	}
	delete(visiting, mp.ID)
	return true
}

// computeAnalyzerSetHash returns the digest over the (name, version,
// configSalt) triples for every analyzer in plan. Registry-resolved
// descriptors supply version + salt; fallback values mirror the L1
// stub when no descriptor is registered (in-tree tests).
func computeAnalyzerSetHash(plan *runPlan, registry *analyzers.Registry) [32]byte {
	if plan == nil || len(plan.analyzers) == 0 {
		return [32]byte{}
	}
	// Dedup by analyzer pointer (matches wrapAnalyzers).
	seen := make(map[*analysis.Analyzer]bool, len(plan.analyzers))
	names := make([]string, 0, len(plan.analyzers))
	versions := make([]string, 0, len(plan.analyzers))
	salts := make([][32]byte, 0, len(plan.analyzers))
	for _, e := range plan.analyzers {
		if e.analyzer == nil || seen[e.analyzer] {
			continue
		}
		seen[e.analyzer] = true
		desc := registry.Lookup(e.analyzer)
		names = append(names, e.analyzer.Name)
		versions = append(versions, analyzerVersionFor(e.analyzer, desc))
		salts = append(salts, configSaltFor(e.analyzer, desc))
	}
	return l0.HashAnalyzerSet(names, versions, salts)
}

// analyzerVersionFor mirrors gopls/cache/l1.go's analyzerVersionFor
// (the L1 path's version source) so the L0 set hash invalidates on
// the same triggers L1 entries do. Folds in the per-wrapper
// CacheVersion and engine-level EngineCacheVersion so a bump
// invalidates L0 entries the same way it invalidates L1 entries.
func analyzerVersionFor(a *analysis.Analyzer, d *analyzers.AnalyzerDescriptor) string {
	base := stubAnalyzerVersion(a)
	if d != nil && d.AnalyzerVersion != "" {
		base = d.AnalyzerVersion
	}
	var wrapper uint8
	if d != nil {
		wrapper = d.CacheVersion
	}
	return fmt.Sprintf("%s.cv%d.e%d", base, wrapper, analyzers.EngineCacheVersion)
}

// stubAnalyzerVersion is the W6 fallback (sha256(name)[:8]) used when
// no descriptor is registered. Kept private here — production paths
// land on descriptor.AnalyzerVersion via analyzerVersionFor.
func stubAnalyzerVersion(a *analysis.Analyzer) string {
	h := sha256.Sum256([]byte(a.Name))
	const hexChars = "0123456789abcdef"
	out := make([]byte, 16)
	for i, b := range h[:8] {
		out[2*i] = hexChars[b>>4]
		out[2*i+1] = hexChars[b&0x0f]
	}
	return string(out)
}

// configSaltFor mirrors gopls/cache/l1.go's configSaltFor.
func configSaltFor(a *analysis.Analyzer, d *analyzers.AnalyzerDescriptor) [32]byte {
	if d != nil && d.ConfigSalt != nil {
		return d.ConfigSalt(nil)
	}
	// Fallback: name-only salt.
	return sha256.Sum256([]byte(a.Name))
}

// uriPkgMap returns a map from compiled-Go-file URI → owning workspace
// PackageID, built over pkgs. Used post-Analyze to partition
// diagnostics back to their producing package for the L0 write.
//
// Diagnostics whose URI does not appear in any workspace package's
// CompiledGoFiles are not attributed and won't be cached — the engine
// treats them as side-channel output that's safer to recompute on
// every run than to risk attributing to the wrong package.
func uriPkgMap(pkgs map[metadata.PackageID]*metadata.Package) map[protocol.DocumentURI]metadata.PackageID {
	out := make(map[protocol.DocumentURI]metadata.PackageID, len(pkgs)*4)
	for id, mp := range pkgs {
		if mp == nil {
			continue
		}
		for _, uri := range mp.CompiledGoFiles {
			// First write wins on URI collisions across ITV variants;
			// the engine doesn't analyse ITVs separately so this is a
			// non-issue in practice.
			if _, ok := out[uri]; !ok {
				out[uri] = id
			}
		}
	}
	return out
}

// uriFromFilename converts an OS path string (as produced by
// output.FromAnalysis from cache.Diagnostic.URI.Path()) back into a
// protocol.DocumentURI suitable for use as a map key against the
// reverse map built by uriPkgMap.
func uriFromFilename(path string) protocol.DocumentURI {
	return protocol.URIFromPath(path)
}

// Compile-time guard: file.Hash is [32]byte; cast above is the
// canonical identity.
var _ = func() bool {
	var z file.Hash
	_ = [32]byte(z)
	return true
}()
