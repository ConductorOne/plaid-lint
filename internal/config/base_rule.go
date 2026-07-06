// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"errors"
	"fmt"
	"regexp"
)

// BaseRule is the common field set for exclude and severity rules. The
// non-blank-condition count is rule-type-specific (2 for exclude, 1
// for severity) and is supplied via the [Validate] caller.
type BaseRule struct {
	Linters    []string `yaml:"linters,omitempty" json:"linters,omitempty"`
	Path       string   `yaml:"path,omitempty" json:"path,omitempty"`
	PathExcept string   `yaml:"path-except,omitempty" json:"path-except,omitempty"`
	Text       string   `yaml:"text,omitempty" json:"text,omitempty"`
	Source     string   `yaml:"source,omitempty" json:"source,omitempty"`
}

// Validate checks the rule's regex fields, mutual exclusivity, and
// non-blank count. The minConditions argument matches upstream's
// excludeRuleMinConditionsCount (2) / severityRuleMinConditionsCount (1).
func (b *BaseRule) Validate(minConditions int) error {
	if err := validateOptionalRegex("path", b.Path); err != nil {
		return err
	}
	if err := validateOptionalRegex("path-except", b.PathExcept); err != nil {
		return err
	}
	if err := validateOptionalRegex("text", b.Text); err != nil {
		return err
	}
	if err := validateOptionalRegex("source", b.Source); err != nil {
		return err
	}

	if b.Path != "" && b.PathExcept != "" {
		return errors.New("path and path-except cannot both be set")
	}

	nonBlank := 0
	if len(b.Linters) > 0 {
		nonBlank++
	}
	if b.Path != "" || b.PathExcept != "" {
		nonBlank++
	}
	if b.Text != "" {
		nonBlank++
	}
	if b.Source != "" {
		nonBlank++
	}

	if nonBlank < minConditions {
		return fmt.Errorf("at least %d of (text, source, path[-except], linters) must be set", minConditions)
	}
	return nil
}

func validateOptionalRegex(field, value string) error {
	if value == "" {
		return nil
	}
	if _, err := regexp.Compile(value); err != nil {
		return fmt.Errorf("invalid %s regex: %w", field, err)
	}
	return nil
}

// excludeRuleMinConditions is the floor for [ExcludeRule.Validate].
const excludeRuleMinConditions = 2

// severityRuleMinConditions is the floor for [SeverityRule.Validate].
const severityRuleMinConditions = 1

// ExcludeRule narrows which issues are dropped. At least 2 of
// {text, source, path[-except], linters} must be set.
type ExcludeRule struct {
	BaseRule `yaml:",inline" json:",inline"`
}

// Validate checks the exclude rule.
func (e *ExcludeRule) Validate() error {
	return e.BaseRule.Validate(excludeRuleMinConditions)
}
