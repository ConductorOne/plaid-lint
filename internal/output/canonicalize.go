// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package output

import (
	"github.com/conductorone/plaid-lint/internal/canonicalpath"
)

// ResolveDiagnostics rewrites every Pos.Filename (primary, Related,
// and SuggestedFixes' TextEdit ranges) in diags back to an absolute
// path using r. Diagnostics whose owning pkgPath isn't in r's map
// (cgo / vendored / external-test) are left in canonical form.
//
// The slice is mutated in place. Cache-layer callers DO NOT call this
// — only the CLI's render-time path does. This is the reverse half of
// the engine's canonicalisation pass (engine.canonicalizeDiagnostics).
func ResolveDiagnostics(r *canonicalpath.Resolver, diags []Diagnostic) {
	if r == nil || len(diags) == 0 {
		return
	}
	for i := range diags {
		d := &diags[i]
		d.Pos.Filename = r.Resolve(d.Pos.Filename)
		for j := range d.Related {
			d.Related[j].Position.Filename = r.Resolve(d.Related[j].Position.Filename)
		}
		for j := range d.SuggestedFixes {
			for k := range d.SuggestedFixes[j].TextEdits {
				te := &d.SuggestedFixes[j].TextEdits[k]
				te.Start.Filename = r.Resolve(te.Start.Filename)
				te.End.Filename = r.Resolve(te.End.Filename)
			}
		}
	}
}
