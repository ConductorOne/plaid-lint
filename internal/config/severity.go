// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"errors"
	"fmt"
)

// Severity mirrors v2's `severity:` block. v1's `default-severity` is
// migrated to `default` by the legacy decoder.
type Severity struct {
	Default string         `yaml:"default,omitempty" json:"default,omitempty"`
	Rules   []SeverityRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// Validate checks that Default is set when Rules are non-empty and
// recurses into each rule.
func (s *Severity) Validate() error {
	if len(s.Rules) > 0 && s.Default == "" {
		return errors.New("severity.default must be set when severity.rules is non-empty")
	}
	for i, rule := range s.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("severity.rules[%d]: %w", i, err)
		}
	}
	return nil
}

// SeverityRule overrides the severity of issues that match the
// embedded BaseRule.
type SeverityRule struct {
	BaseRule `yaml:",inline" json:",inline"`
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"`
}

// Validate checks that severity is set and recurses into BaseRule.
func (s *SeverityRule) Validate() error {
	if s.Severity == "" {
		return errors.New("severity must be set")
	}
	return s.BaseRule.Validate(severityRuleMinConditions)
}
