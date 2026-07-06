// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import "gopkg.in/yaml.v3"

// marshalForCompare returns a YAML-canonical byte slice for v. Used by
// the zero-value gate in Merge — the round-trip handles maps and
// pointers correctly without manual reflection.
func marshalForCompare(v any) ([]byte, error) {
	return yaml.Marshal(v)
}
