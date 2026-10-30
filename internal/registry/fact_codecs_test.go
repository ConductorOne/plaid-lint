// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"go/token"
	"reflect"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
)

// fieldNames lists the fields of struct type rt with their types.
func fieldNames(rt reflect.Type) []string {
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		out = append(out, f.Name+" "+f.Type.Kind().String())
	}
	return out
}

// TestFactCodecs_UpstreamShape pins the upstream fact layouts the
// reflective codecs depend on. If a dependency bump changes them,
// update fact_codecs.go (and this test) so no field is dropped.
func TestFactCodecs_UpstreamShape(t *testing.T) {
	members := exhaustiveFactType.Elem().Field(0)
	if got, want := fieldNames(exhaustiveFactType.Elem()), []string{"Members struct"}; !slices.Equal(got, want) {
		t.Errorf("exhaustive fact fields = %v, want %v", got, want)
	}
	if got, want := fieldNames(members.Type), []string{
		"Names slice", "NameToPos map", "NameToValue map", "ValueToNames map",
	}; !slices.Equal(got, want) {
		t.Errorf("exhaustive enumMembers fields = %v, want %v", got, want)
	}

	if k := contextcheckFactType.Elem().Kind(); k != reflect.Map {
		t.Fatalf("contextcheck fact kind = %s, want map", k)
	}
	if got, want := fieldNames(contextcheckFactType.Elem().Elem()), []string{
		"Valid bool", "Funcs slice", "ReqCtx bool", "Skip bool", "EntryType int",
	}; !slices.Equal(got, want) {
		t.Errorf("contextcheck resInfo fields = %v, want %v", got, want)
	}
}

// newExhaustiveFact builds an enumMembersFact with n members (two of
// which share a value) via reflection, since the type is unexported.
func newExhaustiveFact(n int) analysis.Fact {
	fact := reflect.New(exhaustiveFactType.Elem())
	m := fact.Elem().FieldByName("Members")
	names := m.FieldByName("Names")
	pos, val, v2n := makeMap(m.FieldByName("NameToPos")), makeMap(m.FieldByName("NameToValue")), makeMap(m.FieldByName("ValueToNames"))
	for i := range n {
		name := fmt.Sprintf("M%02d", i)
		value := reflect.ValueOf(fmt.Sprint(i % (n - 1))).Convert(val.Type().Elem())
		names.Set(reflect.Append(names, reflect.ValueOf(name)))
		pos.SetMapIndex(reflect.ValueOf(name), reflect.ValueOf(token.Pos(100+i)))
		val.SetMapIndex(reflect.ValueOf(name), value)
		prev := v2n.MapIndex(value)
		if !prev.IsValid() {
			prev = reflect.ValueOf([]string(nil))
		}
		v2n.SetMapIndex(value, reflect.Append(prev, reflect.ValueOf(name)))
	}
	return fact.Interface().(analysis.Fact)
}

// newContextcheckFact builds a ctxFact with n entries via reflection.
func newContextcheckFact(n int) analysis.Fact {
	fact := reflect.New(contextcheckFactType.Elem())
	mp := makeMap(fact.Elem())
	for i := range n {
		v := reflect.New(mp.Type().Elem()).Elem()
		v.FieldByName("Valid").SetBool(i%2 == 0)
		v.FieldByName("Funcs").Set(reflect.ValueOf([]string{fmt.Sprintf("f%d", i), "g"}))
		v.FieldByName("ReqCtx").SetBool(i%3 == 0)
		v.FieldByName("Skip").SetBool(i%5 == 0)
		v.FieldByName("EntryType").SetInt(int64(i % 4))
		mp.SetMapIndex(reflect.ValueOf(fmt.Sprintf("pkg.F%02d", i)), v)
	}
	return fact.Interface().(analysis.Fact)
}

// gobRoundTrip encodes fact as an analysis.Fact interface value (as
// the fact set does) and decodes it back.
func gobRoundTrip(t *testing.T, fact analysis.Fact) ([]byte, analysis.Fact) {
	t.Helper()
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&fact); err != nil {
		t.Fatalf("gob encode %T: %v", fact, err)
	}
	enc := slices.Clone(buf.Bytes())
	var out analysis.Fact
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("gob decode %T: %v", fact, err)
	}
	return enc, out
}

// TestFactCodecs_RoundTripDeterministic checks, for each codec, that
// the wire form gob-encodes to the same bytes on every encoding of
// the same value, and that decoding reproduces the original fact
// exactly (reflect.DeepEqual), so analyzers importing it behave as
// if the map-bearing fact had been serialized directly.
func TestFactCodecs_RoundTripDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fact     analysis.Fact
		toWire   func(analysis.Fact) analysis.Fact
		fromWire func(analysis.Fact) analysis.Fact
	}{
		{"exhaustive", newExhaustiveFact(24), exhaustiveToWire, exhaustiveFromWire},
		{"contextcheck", newContextcheckFact(24), contextcheckToWire, contextcheckFromWire},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first []byte
			for i := range 10 {
				enc, decoded := gobRoundTrip(t, tc.toWire(tc.fact))
				if i == 0 {
					first = enc
				} else if !bytes.Equal(first, enc) {
					t.Fatalf("encoding %d differs from encoding 0", i)
				}
				got := tc.fromWire(decoded)
				if !reflect.DeepEqual(got, tc.fact) {
					t.Fatalf("round trip mismatch:\n got  %#v\n want %#v", got, tc.fact)
				}
			}
		})
	}
}
