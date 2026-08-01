// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"fmt"
)

// factsMagic prefixes every .plaidfacts file: "PLF" + one format
// version byte. The payload after the header is the facts fork's
// canonical Set encoding (facts sorted by (package, object, type),
// gob-encoded).
//
// The version byte is bumped only when the payload encoding changes
// incompatibly; readers reject unknown versions loudly so a build
// system surfaces a version skew as a clean action failure rather
// than silent fact loss.
var factsMagic = []byte{'P', 'L', 'F', 0x01}

// wrapFacts frames a canonical fact-set encoding as a .plaidfacts
// file body. An empty payload is legal (a package that produced no
// facts still writes its declared output).
func wrapFacts(payload []byte) []byte {
	body := make([]byte, 0, len(factsMagic)+len(payload))
	body = append(body, factsMagic...)
	return append(body, payload...)
}

// unwrapFacts validates the header and returns the payload.
func unwrapFacts(body []byte) ([]byte, error) {
	if len(body) < len(factsMagic) {
		return nil, fmt.Errorf("plaidfacts: truncated header (%d bytes)", len(body))
	}
	if !bytes.Equal(body[:3], factsMagic[:3]) {
		return nil, fmt.Errorf("plaidfacts: bad magic %q", body[:3])
	}
	if body[3] != factsMagic[3] {
		return nil, fmt.Errorf("plaidfacts: unsupported version %d (want %d)", body[3], factsMagic[3])
	}
	return body[len(factsMagic):], nil
}
