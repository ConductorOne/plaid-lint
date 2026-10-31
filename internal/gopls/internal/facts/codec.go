// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"encoding/gob"
	"fmt"
	"io"
	"reflect"
	"sort"
	"sync"

	"golang.org/x/tools/go/analysis"
)

// A WireCodec gives one fact type a deterministic serialized form.
//
// gob iterates maps in random order, so a fact type with map-kinded
// fields encodes to different bytes on every run even though its
// value is unchanged. Such a type cannot be fixed at its definition
// when it belongs to a third-party analyzer, so the fact set swaps it
// for a map-free wire value on Encode and swaps it back on Decode.
//
// ToWire must return a value whose gob encoding is a pure function of
// the fact's value (maps flattened into sorted slices). FromWire must
// invert it exactly: the decoded fact is what the analyzer imports.
type WireCodec struct {
	ToWire   func(analysis.Fact) analysis.Fact
	FromWire func(analysis.Fact) analysis.Fact
}

type wireEntry struct {
	codec    WireCodec
	factType reflect.Type
	wireType reflect.Type
}

var (
	wireMu     sync.RWMutex
	wireByFact = map[reflect.Type]*wireEntry{}
	wireByWire = map[reflect.Type]*wireEntry{}
)

// RegisterWireCodec installs codec for facts of the same dynamic type
// as fact, serialized as values of the same dynamic type as wire. The
// wire type is gob-registered here. Registering the same pair twice
// is a no-op; any other conflict panics, since it is a wiring bug.
func RegisterWireCodec(fact, wire analysis.Fact, codec WireCodec) {
	ft, wt := reflect.TypeOf(fact), reflect.TypeOf(wire)
	wireMu.Lock()
	defer wireMu.Unlock()
	if e, ok := wireByFact[ft]; ok {
		if e.wireType == wt {
			return
		}
		panic(fmt.Sprintf("facts: wire codec for %s already registered (as %s)", ft, e.wireType))
	}
	if e, ok := wireByWire[wt]; ok {
		panic(fmt.Sprintf("facts: wire type %s already used by %s", wt, e.factType))
	}
	gob.Register(wire)
	e := &wireEntry{codec: codec, factType: ft, wireType: wt}
	wireByFact[ft] = e
	wireByWire[wt] = e
}

// WireType reports the wire type registered for fact type ft, if any.
func WireType(ft reflect.Type) (reflect.Type, bool) {
	wireMu.RLock()
	defer wireMu.RUnlock()
	e, ok := wireByFact[ft]
	if !ok {
		return nil, false
	}
	return e.wireType, true
}

// toWire returns the serialized form of fact: its wire value when a
// codec is registered for its type, fact itself otherwise.
func toWire(fact analysis.Fact) analysis.Fact {
	wireMu.RLock()
	e, ok := wireByFact[reflect.TypeOf(fact)]
	wireMu.RUnlock()
	if !ok {
		return fact
	}
	return e.codec.ToWire(fact)
}

// fromWire inverts toWire on a gob-decoded fact. Facts that are not
// wire values (including facts written before their type gained a
// codec) pass through unchanged.
func fromWire(fact analysis.Fact) analysis.Fact {
	wireMu.RLock()
	e, ok := wireByWire[reflect.TypeOf(fact)]
	wireMu.RUnlock()
	if !ok {
		return fact
	}
	return e.codec.FromWire(fact)
}

// PinTypeIDs assigns gob type ids to the fact envelope and to every
// type in factTypes (or its registered wire type), in a fixed order.
//
// gob numbers types process-wide, in the order an encoder first sees
// them, and writes those numbers into every stream. Without pinning,
// a long-lived process (a persistent worker) encodes the same fact
// set to different bytes depending on which fact types it happened to
// encode for earlier packages. Calling PinTypeIDs with the complete
// set of fact types before the first Set.Encode makes the numbering
// a function of that set alone. Decoding never assigns ids, so only
// encoders have to come after it.
//
// Types that already have ids keep them; types encoded later that are
// absent from factTypes still get history-dependent ids.
func PinTypeIDs(factTypes []reflect.Type) {
	types := make([]reflect.Type, 0, len(factTypes))
	seen := map[reflect.Type]bool{}
	for _, ft := range factTypes {
		if wt, ok := WireType(ft); ok {
			ft = wt
		}
		if !seen[ft] {
			seen[ft] = true
			types = append(types, ft)
		}
	}
	sort.Slice(types, func(i, j int) bool { return typeKey(types[i]) < typeKey(types[j]) })

	// The envelope first: every fact blob begins with its definition.
	pinTypeID([]gobFact{})
	for _, t := range types {
		if t.Kind() == reflect.Pointer {
			pinTypeID(reflect.New(t.Elem()).Interface())
		} else {
			pinTypeID(reflect.Zero(t).Interface())
		}
	}
}

// pinTypeID encodes v to nowhere. The encoder assigns v's type (and
// the types it contains) an id before it encodes the value, so an
// encode error or a panicking GobEncode on a zero value still pins.
func pinTypeID(v any) {
	defer func() { _ = recover() }()
	_ = gob.NewEncoder(io.Discard).Encode(v)
}

// typeKey orders types by package path and name, which unlike
// reflect.Type.String is unique across packages that share a name.
func typeKey(t reflect.Type) string {
	n := t
	prefix := ""
	for n.Kind() == reflect.Pointer && n.Name() == "" {
		prefix += "*"
		n = n.Elem()
	}
	if n.Name() == "" {
		return prefix + t.String()
	}
	return prefix + n.PkgPath() + "." + n.Name()
}
