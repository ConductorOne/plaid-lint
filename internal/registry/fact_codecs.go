// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/token"
	"reflect"
	"slices"

	contextcheckpass "github.com/kkHAIKE/contextcheck"
	exhaustivepass "github.com/nishanths/exhaustive"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/gopls/facts"
)

// Upstream fact types whose gob encoding is nondeterministic because
// they hold maps (gob iterates maps in random order). Both types are
// unexported upstream, so the codecs below reach their fields by
// reflection and swap them for sorted-slice wire values when a fact
// set is serialized. TestFactCodecs_* pin the upstream field sets so
// a dependency bump that adds a field fails loudly instead of
// silently dropping it on the wire.
//
//   - exhaustive: *enumMembersFact{Members enumMembers{Names []string;
//     NameToPos map[string]token.Pos; NameToValue
//     map[string]constantValue; ValueToNames map[constantValue][]string}}
//   - contextcheck: *ctxFact (map[string]resInfo{Valid bool; Funcs
//     []string; ReqCtx bool; Skip bool; EntryType entryType})
var (
	exhaustiveFact   = exhaustivepass.Analyzer.FactTypes[0]
	contextcheckFact = contextcheckpass.NewAnalyzer(contextcheckpass.Configuration{}).FactTypes[0]

	exhaustiveFactType   = reflect.TypeOf(exhaustiveFact)
	contextcheckFactType = reflect.TypeOf(contextcheckFact)
)

func init() {
	facts.RegisterWireCodec(exhaustiveFact, (*exhaustiveFactWire)(nil), facts.WireCodec{
		ToWire:   exhaustiveToWire,
		FromWire: exhaustiveFromWire,
	})
	facts.RegisterWireCodec(contextcheckFact, (*contextcheckFactWire)(nil), facts.WireCodec{
		ToWire:   contextcheckToWire,
		FromWire: contextcheckFromWire,
	})
}

// exhaustiveFactWire is the serialized form of exhaustive's
// enumMembersFact: each map flattened into a slice sorted by key.
// A nil and an empty map both encode as an empty slice and decode as
// nil, which is what gob itself does for an empty map.
type exhaustiveFactWire struct {
	Names        []string
	NameToPos    []exhaustiveNamePos
	NameToValue  []exhaustiveNameValue
	ValueToNames []exhaustiveValueNames
}

type exhaustiveNamePos struct {
	Name string
	Pos  token.Pos
}

type exhaustiveNameValue struct {
	Name, Value string
}

type exhaustiveValueNames struct {
	Value string
	Names []string
}

func (*exhaustiveFactWire) AFact() {}

func exhaustiveToWire(f analysis.Fact) analysis.Fact {
	m := reflect.ValueOf(f).Elem().FieldByName("Members")
	w := &exhaustiveFactWire{Names: m.FieldByName("Names").Interface().([]string)}
	for _, k := range sortedMapKeys(m.FieldByName("NameToPos")) {
		v := m.FieldByName("NameToPos").MapIndex(k)
		w.NameToPos = append(w.NameToPos, exhaustiveNamePos{Name: k.String(), Pos: token.Pos(v.Int())})
	}
	for _, k := range sortedMapKeys(m.FieldByName("NameToValue")) {
		v := m.FieldByName("NameToValue").MapIndex(k)
		w.NameToValue = append(w.NameToValue, exhaustiveNameValue{Name: k.String(), Value: v.String()})
	}
	for _, k := range sortedMapKeys(m.FieldByName("ValueToNames")) {
		v := m.FieldByName("ValueToNames").MapIndex(k)
		w.ValueToNames = append(w.ValueToNames, exhaustiveValueNames{Value: k.String(), Names: v.Interface().([]string)})
	}
	return w
}

func exhaustiveFromWire(f analysis.Fact) analysis.Fact {
	w := f.(*exhaustiveFactWire)
	fact := reflect.New(exhaustiveFactType.Elem())
	m := fact.Elem().FieldByName("Members")
	m.FieldByName("Names").Set(reflect.ValueOf(w.Names))
	if len(w.NameToPos) > 0 {
		mp := makeMap(m.FieldByName("NameToPos"))
		for _, e := range w.NameToPos {
			mp.SetMapIndex(reflect.ValueOf(e.Name), reflect.ValueOf(e.Pos))
		}
	}
	if len(w.NameToValue) > 0 {
		mp := makeMap(m.FieldByName("NameToValue"))
		vt := mp.Type().Elem()
		for _, e := range w.NameToValue {
			mp.SetMapIndex(reflect.ValueOf(e.Name), reflect.ValueOf(e.Value).Convert(vt))
		}
	}
	if len(w.ValueToNames) > 0 {
		mp := makeMap(m.FieldByName("ValueToNames"))
		kt := mp.Type().Key()
		for _, e := range w.ValueToNames {
			mp.SetMapIndex(reflect.ValueOf(e.Value).Convert(kt), reflect.ValueOf(e.Names))
		}
	}
	return fact.Interface().(analysis.Fact)
}

// contextcheckFactWire is the serialized form of contextcheck's
// ctxFact map, as entries sorted by key.
type contextcheckFactWire struct {
	Entries []contextcheckEntry
}

type contextcheckEntry struct {
	Key       string
	Valid     bool
	Funcs     []string
	ReqCtx    bool
	Skip      bool
	EntryType int64
}

func (*contextcheckFactWire) AFact() {}

func contextcheckToWire(f analysis.Fact) analysis.Fact {
	m := reflect.ValueOf(f).Elem()
	w := &contextcheckFactWire{}
	for _, k := range sortedMapKeys(m) {
		v := m.MapIndex(k)
		w.Entries = append(w.Entries, contextcheckEntry{
			Key:       k.String(),
			Valid:     v.FieldByName("Valid").Bool(),
			Funcs:     v.FieldByName("Funcs").Interface().([]string),
			ReqCtx:    v.FieldByName("ReqCtx").Bool(),
			Skip:      v.FieldByName("Skip").Bool(),
			EntryType: v.FieldByName("EntryType").Int(),
		})
	}
	return w
}

func contextcheckFromWire(f analysis.Fact) analysis.Fact {
	w := f.(*contextcheckFactWire)
	fact := reflect.New(contextcheckFactType.Elem())
	if len(w.Entries) > 0 {
		mp := makeMap(fact.Elem())
		vt := mp.Type().Elem()
		for _, e := range w.Entries {
			v := reflect.New(vt).Elem()
			v.FieldByName("Valid").SetBool(e.Valid)
			v.FieldByName("Funcs").Set(reflect.ValueOf(e.Funcs))
			v.FieldByName("ReqCtx").SetBool(e.ReqCtx)
			v.FieldByName("Skip").SetBool(e.Skip)
			v.FieldByName("EntryType").SetInt(e.EntryType)
			mp.SetMapIndex(reflect.ValueOf(e.Key), v)
		}
	}
	return fact.Interface().(analysis.Fact)
}

// sortedMapKeys returns the keys of a string-kinded map in sorted order.
func sortedMapKeys(m reflect.Value) []reflect.Value {
	keys := m.MapKeys()
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		switch x, y := a.String(), b.String(); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return keys
}

// makeMap allocates a map into the settable map-typed field v and
// returns it.
func makeMap(v reflect.Value) reflect.Value {
	v.Set(reflect.MakeMap(v.Type()))
	return v
}
