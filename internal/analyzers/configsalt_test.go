// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"testing"
)

// TestConfigSaltDeterministic — same inputs → same salt.
func TestConfigSaltDeterministic(t *testing.T) {
	cfg := map[string]any{
		"opt-a": "value",
		"opt-b": []any{"x", "y", "z"},
		"opt-c": map[string]any{"nested": true},
	}
	a := ConfigSalt("alpha", cfg)
	b := ConfigSalt("alpha", cfg)
	if a != b {
		t.Errorf("non-deterministic salt: %x vs %x", a, b)
	}
}

// TestConfigSaltMapKeyOrderInsensitive — two maps with the same
// content but different go-runtime insertion order hash equal.
func TestConfigSaltMapKeyOrderInsensitive(t *testing.T) {
	a := map[string]any{"a": 1, "b": 2, "c": 3}
	b := map[string]any{"c": 3, "a": 1, "b": 2}
	if ConfigSalt("k", a) != ConfigSalt("k", b) {
		t.Errorf("map key order changed salt")
	}
}

// TestConfigSaltArrayOrderSensitive — array order is preserved
// (revive.rules and other precedence-sensitive arrays).
func TestConfigSaltArrayOrderSensitive(t *testing.T) {
	a := []any{"rule1", "rule2", "rule3"}
	b := []any{"rule3", "rule2", "rule1"}
	if ConfigSalt("revive", a) == ConfigSalt("revive", b) {
		t.Errorf("array order should affect salt but didn't")
	}
}

// TestConfigSaltAnalyzerNameSensitive — two analyzers with the same
// config hash differently.
func TestConfigSaltAnalyzerNameSensitive(t *testing.T) {
	cfg := map[string]any{"k": "v"}
	if ConfigSalt("alpha", cfg) == ConfigSalt("beta", cfg) {
		t.Errorf("analyzer name didn't affect salt")
	}
}

// TestConfigSaltNilAndEmpty — nil config and the all-empty equivalent
// produce the same salt (canonicalize collapses empties).
func TestConfigSaltNilAndEmpty(t *testing.T) {
	a := ConfigSalt("alpha", nil)
	b := ConfigSalt("alpha", map[string]any{})
	c := ConfigSalt("alpha", []any{})
	d := ConfigSalt("alpha", map[string]any{"empty-string": "", "empty-map": map[string]any{}})
	if a != b || b != c || c != d {
		t.Errorf("nil vs empty produce different salts: a=%x b=%x c=%x d=%x", a, b, c, d)
	}
}

// TestConfigSaltWhitespaceInvariant — two semantically-identical
// configs differing only in surface formatting produce the same salt
// because canonicalisation re-encodes both to the same canonical form.
func TestConfigSaltWhitespaceInvariant(t *testing.T) {
	a := map[string]any{
		"option-a": "value",
		"option-b": float64(42),
	}
	b := map[string]any{
		"option-b": float64(42),
		"option-a": "value",
	}
	if ConfigSalt("foo", a) != ConfigSalt("foo", b) {
		t.Errorf("whitespace-equivalent configs produced different salts")
	}
}

// TestConfigSaltNestedMap — nested map keys are also sorted.
func TestConfigSaltNestedMap(t *testing.T) {
	a := map[string]any{
		"outer": map[string]any{
			"inner-a": 1,
			"inner-b": 2,
		},
	}
	b := map[string]any{
		"outer": map[string]any{
			"inner-b": 2,
			"inner-a": 1,
		},
	}
	if ConfigSalt("foo", a) != ConfigSalt("foo", b) {
		t.Errorf("nested map order affected salt")
	}
}

// TestSaltNoConfigNonZero — the sentinel salt for analyzers without a
// config is not the all-zero sentinel, so analyzers can't be confused
// with "uninitialised" state.
func TestSaltNoConfigNonZero(t *testing.T) {
	salt := SaltNoConfig()
	var zero [32]byte
	if salt == zero {
		t.Errorf("no-config salt is all zero; want non-zero sentinel")
	}
}
