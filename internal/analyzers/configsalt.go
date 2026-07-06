// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
)

// ConfigSalt computes a stable 32-byte hash over a Go value
// representing an analyzer's config block. The hash is deterministic
// across runs and across whitespace-only edits of the source config:
// the value is canonicalized first (sorted map keys, preserved array
// order for precedence-sensitive arrays, type-coerced, null/empty
// dropped) and then SHA-256'd.
//
// The canonicalization rules:
//
//   - Map keys are sorted recursively.
//   - Array order is preserved (revive.rules, forbidigo.forbid, and
//     other precedence-sensitive arrays must not be re-ordered).
//   - Nested maps/arrays are recursively canonicalized.
//   - nil, empty maps, empty arrays, and empty strings are dropped.
//   - Numeric types are normalised to canonical JSON numbers; booleans
//     pass through.
//
// The function accepts any Go value the analyzer's config struct can
// be marshalled to via encoding/json — typically a map[string]any or a
// dedicated struct. A nil value produces a fixed zero-options digest
// (so analyzers with no config still get a deterministic, non-zero
// salt distinct from the all-zero "uninitialised" sentinel).
//
// Phase 1 ships the canonicalizer over an internal normalised config
// struct. Full .golangci.yml v1+v2 YAML parsing is Phase 2; the
// caller is responsible for translating raw YAML into a
// canonical Go value before invoking ConfigSalt.
func ConfigSalt(analyzer string, cfg any) [32]byte {
	h := sha256.New()
	// Mix in the analyzer name with a length prefix so two analyzers
	// that happen to share a config struct still salt differently.
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(analyzer)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(analyzer))

	canonical, err := canonicalEncode(cfg)
	if err != nil {
		// A non-canonicalizable config is a bug. Fall back to a
		// best-effort representation that still produces a stable hash
		// per (analyzer, type, error). Cache correctness is preserved
		// because the same broken cfg will hash the same way on every
		// run.
		canonical = []byte(fmt.Sprintf("invalid-cfg:%T:%v", cfg, err))
	}
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(canonical)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write(canonical)

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// canonicalEncode turns v into a deterministic byte representation
// suitable for hashing. It uses canonical JSON (sorted object keys, no
// whitespace) layered on top of a recursive map/slice canonicalisation
// pass. The result is identical for two inputs that differ only in
// non-semantic ways (key order, whitespace, nil-vs-empty distinctions).
func canonicalEncode(v any) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	// Round-trip through encoding/json to normalize typed inputs
	// (e.g. structs, named slice types) into the limited set of types
	// canonicalize handles below.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonicalEncode: marshal: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("canonicalEncode: unmarshal: %w", err)
	}
	canonical := canonicalize(generic)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonical); err != nil {
		return nil, fmt.Errorf("canonicalEncode: encode: %w", err)
	}
	// Drop the trailing newline encoding/json adds; we want byte-exact
	// determinism.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// canonicalize walks v and returns a JSON-marshallable value with
// deterministic shape. Maps become *orderedObject; arrays preserve
// order; nil / empty values are dropped to flatten "absent" vs "empty"
// distinctions.
func canonicalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if len(t) == 0 {
			return nil
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := &orderedObject{Keys: make([]string, 0, len(keys)), Values: make([]any, 0, len(keys))}
		for _, k := range keys {
			cv := canonicalize(t[k])
			if cv == nil {
				continue
			}
			obj.Keys = append(obj.Keys, k)
			obj.Values = append(obj.Values, cv)
		}
		if len(obj.Keys) == 0 {
			return nil
		}
		return obj
	case []any:
		// Array order is preserved (revive.rules and other
		// precedence-sensitive arrays). Filter out nils so a trailing
		// or leading null doesn't shift the index of meaningful items.
		out := make([]any, 0, len(t))
		for _, e := range t {
			ce := canonicalize(e)
			if ce == nil {
				continue
			}
			out = append(out, ce)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return t
	case bool, float64:
		return t
	default:
		// Numeric types other than float64 (encoding/json's default
		// for any) shouldn't occur after the marshal round-trip, but
		// be defensive.
		return t
	}
}

// orderedObject is a JSON-marshallable record that preserves key
// insertion order. canonicalize returns these for every map so the
// encoded form matches the canonicalisation pass byte-for-byte.
type orderedObject struct {
	Keys   []string
	Values []any
}

// MarshalJSON emits the object with keys in slice order.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(o.Values[i])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// NoConfig is the sentinel used by descriptors whose analyzer has no
// configuration. Its salt is stable across runs.
type NoConfig struct{}

// SaltNoConfig is the [32]byte digest produced by ConfigSalt(name, nil)
// for an analyzer named ""; descriptors typically use ConfigSalt with
// their own analyzer name to get a name-distinguished digest.
//
// Exposed for tests that want to assert "no-config analyzers all
// produce a stable, distinct-from-zero salt".
func SaltNoConfig() [32]byte { return ConfigSalt("", nil) }
