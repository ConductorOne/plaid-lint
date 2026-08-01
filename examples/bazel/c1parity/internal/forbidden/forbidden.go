// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package forbidden exists to be denied: the fixture config's
// depguard rule (deny-forbidden) blocks importing it. The package
// itself is lint-clean — the finding belongs to the importer.
package forbidden

// Value is what the violating import consumes.
const Value = "forbidden"
