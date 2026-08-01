// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package facts re-exports the gopls fork's facts package
// (internal/gopls/internal/facts) across the fork's internal
// boundary.
//
// The fork keeps upstream's directory layout, so its facts package
// sits below internal/gopls/internal/ and is unimportable outside the
// fork tree. The unit driver (internal/unit) needs exactly the
// fact-set machinery — Decode dependency facts, back an
// analysis.Pass, Encode the package's facts — and must produce
// byte-identical encodings to the engine's, so re-exporting the one
// implementation is the correctness-preserving choice (a second facts
// implementation would fork the wire format).
//
// This package is a deliberate, named seam: only the surface the unit
// driver consumes is re-exported.
package facts

import (
	"github.com/conductorone/plaid-lint/internal/gopls/internal/facts"
)

// Set is a serializable set of analysis.Facts for one package. See
// the forked package's docs for the encode/decode contract.
type Set = facts.Set

// Decoder decodes fact sets from dependency fact blobs.
type Decoder = facts.Decoder

// GetPackageFunc resolves a package path to the *types.Package the
// decoder should attach facts to. Implementations must be
// concurrency-safe and may return nil to drop that package's facts.
type GetPackageFunc = facts.GetPackageFunc

// NewDecoderFunc returns a decoder for the target package whose
// package lookups go through getPackage rather than an eagerly
// computed transitive import map.
var NewDecoderFunc = facts.NewDecoderFunc
