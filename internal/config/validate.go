// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"errors"
)

// Validate aggregates all per-block validators and returns a slice of
// errors, each scoped to its source block. The caller can join them
// (with [errors.Join]) for a single error return, or surface them
// individually with field-path context.
//
// Returns an empty slice (NOT nil) when the config is valid, so
// callers can chain without nil-checking. Callers can use
// `len(Validate(cfg)) == 0` as the success condition.
//
// Per-block validators are also exposed on the relevant types (e.g.
// [Run.Validate], [Linters.Validate]); [Validate] is convenience.
func Validate(cfg *Config) []error {
	if cfg == nil {
		return []error{errors.New("config: nil Config")}
	}
	var errs []error
	if err := cfg.Run.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.Output.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.Linters.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.Linters.Settings.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.Formatters.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.Severity.Validate(); err != nil {
		errs = append(errs, err)
	}
	if errs == nil {
		return []error{}
	}
	return errs
}
