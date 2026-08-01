// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package driverutil re-exports the analysis-driver helpers from the
// gopls fork (internal/gopls/internal/analysis/driverutil) across the
// fork's internal boundary, for use by the unit driver. See
// internal/gopls/facts for the rationale behind these named seams.
package driverutil

import (
	"github.com/conductorone/plaid-lint/internal/gopls/internal/analysis/driverutil"
)

// ValidateFixes validates the set of suggested fixes for a
// diagnostic, per the analysis-driver contract.
var ValidateFixes = driverutil.ValidateFixes

// CheckReadable enforces the pass.ReadFile access policy: only files
// belonging to the pass's package may be read.
var CheckReadable = driverutil.CheckReadable
