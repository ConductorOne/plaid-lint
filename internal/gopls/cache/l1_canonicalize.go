// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"bytes"
	"fmt"
	"hash"
	"path/filepath"
	"strings"

	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

// writeCanonicalIdentity writes a machine-portable rendering of fh's
// (URI, Hash) pair into h. The URI is canonicalised to
// "<pkgPath>/<basename>"; the Hash is folded in unchanged.
// Two machines that have the same source content at different
// absolute paths produce byte-identical writes through this helper,
// which is the L1/L2 ActionID keyspace-portability fix.
//
// pkgPath is the owning package's PackagePath. An empty pkgPath
// falls back to filepath.Base of the URI's path — same shape as the
// L0 source-hash fix.
//
// The output format mirrors file.Identity.String()
// ("<URI><HashHex>") but with the canonical URI substituted; the
// fmt.Fprintln-trailing newline mirrors the prior caller's
// fmt.Fprintln(hasher, fh.Identity()) shape so that running with
// localPackageKey on either side of the bump produces deterministic
// (but different — by cache-version-bump) bytes.
func writeCanonicalIdentity(h hash.Hash, fh file.Handle, pkgPath string) {
	id := fh.Identity()
	uri := canonicalLocalPath(string(id.URI), pkgPath)
	fmt.Fprintf(h, "%s%s\n", uri, id.Hash)
}

// canonicalLocalPath returns the machine-portable form of a
// "file:///abs/path/x.go" URI string. The exact canonical form is
// "<pkgPath>/<basename>" — same as canonicalpath.Canonicalize but
// fed the URI's path component rather than an arbitrary absPath.
// An empty pkgPath falls back to filepath.Base of the URI's path
// component, matching the sourceHashOf fix.
func canonicalLocalPath(uri, pkgPath string) string {
	// Strip the "file://" scheme if present. URI strings here are
	// produced by protocol.URIFromPath, which always uses the file
	// scheme on POSIX.
	abs := uri
	if rest, ok := strings.CutPrefix(uri, "file://"); ok {
		abs = rest
	}
	if abs == "" {
		return ""
	}
	if pkgPath == "" {
		return filepath.Base(abs)
	}
	return canonicalpath.Canonicalize(abs, pkgPath)
}

// canonicalURIScheme tags a URI value as a plaid-lint canonical form
// of "<pkgPath>/<basename>". A canonical URI string is
// "plaid-canonical:<pkgPath>/<basename>" — never a "file:///abs"
// path. The scheme is deliberately unique (not "file") so that
// downstream consumers that read the URI before the L1 read-side
// resolver runs would fail loudly rather than silently dereferencing a
// canonical-shape value as if it were an absolute file path. Within
// the L1 read path, every canonical URI is resolved back to an
// absolute file URI before the gobDiagnostic is handed to a consumer.
const canonicalURIScheme = "plaid-canonical:"

// canonicalURIJSONFileForm is the on-the-wire-but-pre-unmarshal alias
// that survives protocol.DocumentURI.UnmarshalText's file-scheme gate.
// L1 reads rewrite "plaid-canonical:<X>" → "file:///<sentinel>/<X>"
// in the raw JSON bytes, unmarshal, and then resolveURI maps the
// sentinel form back to an absolute file URI via pkgDirs. The on-disk
// JSON format remains "plaid-canonical:<X>" — the rewrite is a
// read-side workaround for the DocumentURI validator.
const canonicalURIJSONFileForm = "file:///__plaid_canonical__/"

// canonicalizeURI rewrites a "file:///abs/path/x.go" DocumentURI into
// its canonical plaid-lint form using uriPkg, the map from absolute
// DocumentURI → owning PkgPath built from the batch's package handles.
// URIs not in uriPkg fall through unchanged: the canonical form
// requires an owning package, and a missing entry is safer to leave
// absolute than to fabricate. This mirrors engine.canonicalizeName.
//
// The empty URI ("" — synthetic / no-position diagnostic) is passed
// through unchanged.
func canonicalizeURI(uri protocol.DocumentURI, uriPkg map[protocol.DocumentURI]string) protocol.DocumentURI {
	if uri == "" {
		return ""
	}
	pkgPath := uriPkg[uri]
	if pkgPath == "" {
		return uri
	}
	abs := uri.Path()
	if abs == "" {
		return uri
	}
	canon := canonicalpath.Canonicalize(abs, pkgPath)
	if canon == abs {
		// Canonicalize returned the input unchanged (empty pkgPath
		// after re-derivation). Keep absolute.
		return uri
	}
	return protocol.DocumentURI(canonicalURIScheme + canon)
}

// resolveURI is the inverse of canonicalizeURI. It rewrites a
// "plaid-canonical:<pkgPath>/<basename>" URI back to a
// "file:///abs/path/x.go" DocumentURI using pkgDirs (PkgPath →
// absolute directory). URIs without the canonical scheme prefix are
// returned unchanged — they were either always absolute (the cgo /
// vendored / external-test fallback path written by canonicalizeURI)
// or were produced by a writer that pre-dated this layer.
//
// When the canonical URI's pkgPath prefix is not in pkgDirs the URI is
// also passed through unchanged (as a non-file canonical-scheme URI),
// which downstream code would refuse as malformed. Test fixtures
// exercise both branches.
func resolveURI(uri protocol.DocumentURI, pkgDirs map[string]string) protocol.DocumentURI {
	if uri == "" {
		return ""
	}
	s := string(uri)
	var canon string
	switch {
	case strings.HasPrefix(s, canonicalURIScheme):
		canon = strings.TrimPrefix(s, canonicalURIScheme)
	case strings.HasPrefix(s, canonicalURIJSONFileForm):
		// Rewritten by rewriteCanonicalToFileForm before unmarshal so
		// the value survived protocol.DocumentURI.UnmarshalText's
		// file-scheme gate. Strip the sentinel to recover the
		// "<pkgPath>/<basename>" payload.
		canon = strings.TrimPrefix(s, canonicalURIJSONFileForm)
	default:
		return uri
	}
	idx := strings.LastIndex(canon, "/")
	if idx <= 0 {
		return uri
	}
	pkgPath := canon[:idx]
	base := canon[idx+1:]
	dir, ok := pkgDirs[pkgPath]
	if !ok {
		return uri
	}
	abs := filepath.Join(dir, base)
	return protocol.URIFromPath(abs)
}

// canonicalizeGobDiagnostic rewrites every Location.URI inside d to
// its canonical plaid-lint form. Inputs are mutated in place. The
// uriPkg map covers absolute-URI → PkgPath for every compiled-Go-file
// owned by a workspace package in this batch; URIs outside the map
// (cgo build cache, vendored dep, external-test variant fallback)
// fall through unchanged.
//
// Walks: primary Location, Related[i].Location, SuggestedFixes[j]
// .TextEdits[k].Location. The gobCommand.Arguments field is left
// alone — analyzer-emitted SuggestedFixes never set Command in
// plaid-lint, and the per-arg JSON layout is opaque to this layer.
// Adding it is a follow-up if a wrapper ever sets Command.
func canonicalizeGobDiagnostic(d *gobDiagnostic, uriPkg map[protocol.DocumentURI]string) {
	if d == nil {
		return
	}
	d.Location.URI = canonicalizeURI(d.Location.URI, uriPkg)
	for i := range d.Related {
		d.Related[i].Location.URI = canonicalizeURI(d.Related[i].Location.URI, uriPkg)
	}
	for i := range d.SuggestedFixes {
		for j := range d.SuggestedFixes[i].TextEdits {
			d.SuggestedFixes[i].TextEdits[j].Location.URI = canonicalizeURI(
				d.SuggestedFixes[i].TextEdits[j].Location.URI, uriPkg,
			)
		}
	}
}

// resolveGobDiagnostic is the inverse of canonicalizeGobDiagnostic.
// Inputs are mutated in place. URIs that don't carry the canonical
// scheme prefix are passed through unchanged (they were either always
// absolute or the writer didn't canonicalize them).
func resolveGobDiagnostic(d *gobDiagnostic, pkgDirs map[string]string) {
	if d == nil {
		return
	}
	d.Location.URI = resolveURI(d.Location.URI, pkgDirs)
	for i := range d.Related {
		d.Related[i].Location.URI = resolveURI(d.Related[i].Location.URI, pkgDirs)
	}
	for i := range d.SuggestedFixes {
		for j := range d.SuggestedFixes[i].TextEdits {
			d.SuggestedFixes[i].TextEdits[j].Location.URI = resolveURI(
				d.SuggestedFixes[i].TextEdits[j].Location.URI, pkgDirs,
			)
		}
	}
}

// l1CanonicalMaps returns the (URI → PkgPath, PkgPath → directory)
// pair the L1 canonicalize/resolve helpers consume for this batch.
// Maps are derived from every packageHandle currently registered
// against b._handles; first-write-wins on PackageID collisions
// (parallel to engine.uriPkgPathMap / pkgDirsFor).
//
// Cheap to recompute (one O(packages * files) walk) but called from
// the hot L1 read/write path, so the result is cached on the batch:
// see l1CanonicalCache. A nil receiver / empty handle map returns
// nil maps; canonicalizeURI / resolveURI tolerate nil maps as
// no-ops, which is the test-scaffold path.
func (b *typeCheckBatch) l1CanonicalMaps() (map[protocol.DocumentURI]string, map[string]string) {
	if b == nil {
		return nil, nil
	}
	b.l1CanonicalCacheMu.Lock()
	defer b.l1CanonicalCacheMu.Unlock()
	if b.l1CanonicalCache != nil {
		return b.l1CanonicalCache.uriPkg, b.l1CanonicalCache.pkgDirs
	}
	b.handleMu.Lock()
	handles := make([]*packageHandle, 0, len(b._handles))
	for _, ph := range b._handles {
		if ph != nil && ph.mp != nil {
			handles = append(handles, ph)
		}
	}
	b.handleMu.Unlock()
	uriPkg := make(map[protocol.DocumentURI]string, len(handles)*4)
	pkgDirs := make(map[string]string, len(handles))
	for _, ph := range handles {
		mp := ph.mp
		if mp.PkgPath == "" {
			continue
		}
		key := string(mp.PkgPath)
		for _, uri := range mp.CompiledGoFiles {
			if _, ok := uriPkg[uri]; !ok {
				uriPkg[uri] = key
			}
		}
		if _, ok := pkgDirs[key]; ok {
			continue
		}
		if len(mp.CompiledGoFiles) > 0 {
			pkgDirs[key] = filepath.Dir(mp.CompiledGoFiles[0].Path())
			continue
		}
		if len(mp.GoFiles) > 0 {
			pkgDirs[key] = filepath.Dir(mp.GoFiles[0].Path())
		}
	}
	b.l1CanonicalCache = &l1CanonicalCacheT{uriPkg: uriPkg, pkgDirs: pkgDirs}
	return uriPkg, pkgDirs
}

// l1CanonicalCacheT is the cached pair l1CanonicalMaps returns. Held
// behind l1CanonicalCacheMu so concurrent first-readers don't compute
// it twice.
type l1CanonicalCacheT struct {
	uriPkg  map[protocol.DocumentURI]string
	pkgDirs map[string]string
}

// cloneGobDiagnostic returns a copy of d safe to mutate without
// affecting the original. Used at L1 write so the engine's in-memory
// summary keeps absolute URIs (needed for its post-engine L0
// canonicalize pass which reads `Location.URI.Path()`).
func cloneGobDiagnostic(d gobDiagnostic) gobDiagnostic {
	if len(d.Related) > 0 {
		cp := make([]gobRelatedInformation, len(d.Related))
		copy(cp, d.Related)
		d.Related = cp
	}
	if len(d.SuggestedFixes) > 0 {
		fixes := make([]gobSuggestedFix, len(d.SuggestedFixes))
		copy(fixes, d.SuggestedFixes)
		for i := range fixes {
			if len(fixes[i].TextEdits) > 0 {
				edits := make([]gobTextEdit, len(fixes[i].TextEdits))
				copy(edits, fixes[i].TextEdits)
				fixes[i].TextEdits = edits
			}
		}
		d.SuggestedFixes = fixes
	}
	return d
}

// rewriteCanonicalToFileForm replaces every JSON string occurrence of
// the canonical scheme prefix `"plaid-canonical:` with
// `"file:///__plaid_canonical__/` so the bytes survive
// protocol.DocumentURI.UnmarshalText's file-scheme gate. The mapping
// is reversed inside resolveURI on the in-memory DocumentURI. The
// on-disk JSON format is unchanged: the rewrite is read-side only.
//
// Rewriting bytes — rather than the string after Unmarshal — is what
// makes this work at all: UnmarshalText runs on the field's bytes
// before any code in this package can see the value.
//
// Both quote variants are checked because json.Marshal could in
// principle escape the colon (it does not in current encoding/json,
// but we rewrite a uniform anchor of `"plaid-canonical:` so the
// post-rewrite raw bytes still parse as JSON regardless).
func rewriteCanonicalToFileForm(raw []byte) []byte {
	const oldAnchor = `"plaid-canonical:`
	const newAnchor = `"file:///__plaid_canonical__/`
	if !bytes.Contains(raw, []byte(oldAnchor)) {
		return raw
	}
	return bytes.ReplaceAll(raw, []byte(oldAnchor), []byte(newAnchor))
}
