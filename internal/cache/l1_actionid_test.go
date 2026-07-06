package cache

import (
	"encoding/json"
	"testing"
)

// TestComputeL1ActionIDDeterministic — same inputs → same key.
func TestComputeL1ActionIDDeterministic(t *testing.T) {
	e := sampleL1()
	a := ComputeL1ActionID(e)
	b := ComputeL1ActionID(e)
	if a != b {
		t.Errorf("same entry → different action IDs: %x vs %x", a, b)
	}
}

// Each L1 input field individually moves the action ID.
func TestComputeL1ActionIDSensitivity(t *testing.T) {
	base := ComputeL1ActionID(sampleL1())
	mutators := []struct {
		name string
		fn   func(e *L1Entry)
	}{
		{"Analyzer", func(e *L1Entry) { e.Analyzer = "errcheck" }},
		{"PackageID", func(e *L1Entry) { e.PackageID = "github.com/example/bar" }},
		{"InputDigest", func(e *L1Entry) { e.InputDigest[0] ^= 0xff }},
		{"DepFactsDigest", func(e *L1Entry) { e.DepFactsDigest[5] ^= 0xff }},
		{"DepTypeDigest", func(e *L1Entry) { e.DepTypeDigest[10] ^= 0xff }},
		{"AnalyzerVersion", func(e *L1Entry) { e.AnalyzerVersion = "v9.9.9" }},
		{"ConfigSalt", func(e *L1Entry) { e.ConfigSalt[15] ^= 0xff }},
		{"ToolVersion", func(e *L1Entry) { e.ToolVersion = "plaid-lint-9.9" }},
	}
	for _, m := range mutators {
		e := sampleL1()
		m.fn(e)
		got := ComputeL1ActionID(e)
		if got == base {
			t.Errorf("%s change did not change action ID", m.name)
		}
	}
}

// Diagnostics/facts are outputs, not inputs — they must NOT feed the action ID.
func TestComputeL1ActionIDOutputsInsensitive(t *testing.T) {
	base := ComputeL1ActionID(sampleL1())
	mutators := []struct {
		name string
		fn   func(e *L1Entry)
	}{
		{"Diagnostics", func(e *L1Entry) {
			e.Diagnostics = []json.RawMessage{json.RawMessage(`{"message":"different"}`)}
		}},
		{"ObjectFacts", func(e *L1Entry) { e.ObjectFacts = []byte("other-object-facts") }},
		{"PackageFacts", func(e *L1Entry) { e.PackageFacts = []byte("other-pkg-facts") }},
	}
	for _, m := range mutators {
		e := sampleL1()
		m.fn(e)
		got := ComputeL1ActionID(e)
		if got != base {
			t.Errorf("%s leaked into action ID", m.name)
		}
	}
}

// TestConfigSaltForAnalyzer — same inputs → same salt; nil vs empty equivalent.
func TestConfigSaltForAnalyzer(t *testing.T) {
	a := ConfigSaltForAnalyzer("printf", nil)
	b := ConfigSaltForAnalyzer("printf", nil)
	if a != b {
		t.Errorf("nil config: same inputs → different salts")
	}
	c := ConfigSaltForAnalyzer("printf", []byte{})
	if a != c {
		t.Errorf("nil vs empty config: expected same salt, got %x vs %x", a, c)
	}
	d := ConfigSaltForAnalyzer("printf", []byte(`{"strict":true}`))
	if a == d {
		t.Errorf("config bytes did not affect salt")
	}
	e := ConfigSaltForAnalyzer("errcheck", nil)
	if a == e {
		t.Errorf("analyzer name did not affect salt")
	}
}

// Length-prefix discriminates against pathological collisions across
// (analyzer, canonical) boundary.
func TestConfigSaltLengthPrefixed(t *testing.T) {
	a := ConfigSaltForAnalyzer("foobar", nil)
	b := ConfigSaltForAnalyzer("foo", []byte("bar"))
	if a == b {
		t.Errorf("length-prefix not enforced: ('foobar', nil) collides with ('foo', 'bar')")
	}
}

// Whitespace-equivalent configs must produce identical salts when the caller
// canonicalizes (the canonicalization itself is W7 — W6 stubs it with the
// caller-provided canonical bytes).
//
// This test documents the W6 contract: if the *caller* canonicalizes
// whitespace away before passing canonical bytes, the salt is invariant.
// (The salt invariant is verified end-to-end in the pipeline
// equivalence test; this is the unit-level statement.)
func TestConfigSaltStableUnderCanonicalization(t *testing.T) {
	canonical := []byte(`{"strict":true}`)
	a := ConfigSaltForAnalyzer("printf", canonical)
	b := ConfigSaltForAnalyzer("printf", canonical)
	if a != b {
		t.Errorf("salt non-deterministic for identical canonical bytes")
	}
}
