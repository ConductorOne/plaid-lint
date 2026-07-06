// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

// TestCanonicalURIJSONRoundTrip pins the round-trip invariant: a gobDiagnostic whose
// Location.URI carries the canonical scheme
// ("plaid-canonical:<pkgPath>/<basename>") must round-trip through
// json.Unmarshal and resolveGobDiagnostic without tripping
// protocol.DocumentURI.UnmarshalText's file-scheme validator at
// internal/gopls/protocol/uri.go:170.
//
// Pre-fix, json.Unmarshal returns
// `DocumentURI scheme is not 'file': plaid-canonical:foo/bar.go`
// and the caller falls through as a cache miss + L1Metrics.Errors++.
// Post-fix, the L1 read path rewrites the raw JSON bytes to a
// file-scheme sentinel before unmarshal; resolveURI maps the sentinel
// back to the absolute file URI via pkgDirs.
func TestCanonicalURIJSONRoundTrip(t *testing.T) {
	const pkgPath = "example.com/p"
	const absDir = "/tmp/plaid-test/example.com/p"

	// Construct a gobDiagnostic exactly as canonicalizeGobDiagnostic
	// would produce, then marshal to JSON — that's the on-disk shape.
	canonical := protocol.DocumentURI(canonicalURIScheme + pkgPath + "/x.go")
	d := gobDiagnostic{
		Location: protocol.Location{URI: canonical},
		Message:  "synth diag",
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), canonicalURIScheme) {
		t.Fatalf("on-disk JSON missing canonical scheme: %s", raw)
	}

	// Bare Unmarshal must fail under the upstream LSP validator —
	// this is the regression the byte-rewrite workaround papered over. If this branch
	// stops failing, DocumentURI's contract has changed and the
	// byte-rewrite workaround should be revisited.
	var sanity gobDiagnostic
	if err := json.Unmarshal(raw, &sanity); err == nil {
		t.Fatalf("bare Unmarshal of canonical-scheme URI unexpectedly succeeded; "+
			"protocol.DocumentURI.UnmarshalText must reject %q",
			string(canonical))
	}

	// Read-path round-trip: rewrite bytes, unmarshal, resolve back to
	// absolute. The result must be the absolute file URI for absDir/x.go.
	rewritten := rewriteCanonicalToFileForm(raw)
	if !strings.Contains(string(rewritten), canonicalURIJSONFileForm) {
		t.Fatalf("rewriteCanonicalToFileForm did not insert sentinel: %s", rewritten)
	}
	if strings.Contains(string(rewritten), canonicalURIScheme) {
		t.Fatalf("rewriteCanonicalToFileForm left canonical scheme intact: %s", rewritten)
	}

	var got gobDiagnostic
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("Unmarshal of rewritten bytes failed: %v", err)
	}
	resolveGobDiagnostic(&got, map[string]string{pkgPath: absDir})

	want := protocol.URIFromPath(absDir + "/x.go")
	if got.Location.URI != want {
		t.Errorf("resolved URI mismatch: got %q, want %q", got.Location.URI, want)
	}
}

// TestRewriteCanonicalToFileForm_NoCanonicalIsIdentity guards that the
// pre-Unmarshal byte rewrite is a no-op on JSON that doesn't carry a
// canonical URI. Prior entries and entries for cgo / vendored
// files (uriPkg miss → URI written unchanged) must pass through
// untouched.
func TestRewriteCanonicalToFileForm_NoCanonicalIsIdentity(t *testing.T) {
	abs := protocol.URIFromPath("/tmp/plaid-test/foo.go")
	d := gobDiagnostic{
		Location: protocol.Location{URI: abs},
		Message:  "absolute-URI diag",
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := rewriteCanonicalToFileForm(raw)
	if string(got) != string(raw) {
		t.Errorf("rewriteCanonicalToFileForm mutated non-canonical bytes:\n got %s\nwant %s", got, raw)
	}
}

// TestResolveURI_FileFormSentinel guards the resolveURI branch added
// for the file-form sentinel: a DocumentURI carrying the file-scheme sentinel must
// resolve back to an absolute file URI via pkgDirs, parallel to the
// existing canonicalURIScheme branch.
func TestResolveURI_FileFormSentinel(t *testing.T) {
	const pkgPath = "example.com/p"
	const absDir = "/tmp/plaid-test/example.com/p"
	pkgDirs := map[string]string{pkgPath: absDir}

	sentinel := protocol.DocumentURI(canonicalURIJSONFileForm + pkgPath + "/x.go")
	got := resolveURI(sentinel, pkgDirs)
	want := protocol.URIFromPath(absDir + "/x.go")
	if got != want {
		t.Errorf("resolveURI(sentinel) = %q, want %q", got, want)
	}

	// pkgDirs miss → unchanged (canonical-shape stays canonical-shape;
	// downstream consumers would refuse it as malformed, which is the
	// loud-fail invariant we set out to preserve).
	gotMiss := resolveURI(sentinel, map[string]string{})
	if gotMiss != sentinel {
		t.Errorf("resolveURI(sentinel, empty pkgDirs) = %q, want unchanged %q", gotMiss, sentinel)
	}
}
