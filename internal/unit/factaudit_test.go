// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"encoding"
	"encoding/gob"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// factTypeAllowlist names fact types (keyed by reflect.Type.String()
// of the concrete FactTypes entry, pointer included) that the audit
// tolerates despite containing map- or interface-kinded payload
// fields. Facts are gob-encoded into .plaidfacts; gob iterates maps
// in random order and encodes interfaces by dynamic type, so any
// entry here is a latent nondeterminism (cache-poisoning) hazard and
// MUST carry a TODO explaining why it is tolerated and how it will
// be fixed.
// Every entry is also printed prominently on each test run so the
// debt stays visible; the audit still hard-fails on any NEW offender.
var factTypeAllowlist = map[string]string{
	// TODO(plaid-lint/unit-mode): contextcheck's fact type is itself a
	// map (kkHAIKE/contextcheck: `type ctxFact map[string]...`), so its
	// gob bytes vary run-to-run whenever contextcheck exports a
	// non-empty fact. Cross-package .plaidfacts containing it are NOT
	// byte-reproducible → cache-key instability in distributed builds.
	// Fix: canonicalize at export (fork the fact type to a sorted
	// slice), or strip contextcheck facts from the exported set.
	"*contextcheck.ctxFact": "upstream map-typed fact; needs canonical encoding before facts export",

	// TODO(plaid-lint/unit-mode): exhaustive's enumMembersFact carries
	// three map fields (Members.NameToPos / NameToValue / ValueToNames)
	// — same nondeterministic-gob hazard as contextcheck above, same
	// fix options (canonicalize on export or strip from .plaidfacts).
	"*exhaustive.enumMembersFact": "upstream fact with map fields; needs canonical encoding before facts export",
}

// gobEncoderType / binaryMarshalerType: a type implementing either
// interface is encoded by its own method, not by gob's reflective
// struct walk, so its fields are opaque to (and exempt from) this
// audit. Determinism is then the method's responsibility.
var (
	gobEncoderType      = reflect.TypeOf((*gob.GobEncoder)(nil)).Elem()
	binaryMarshalerType = reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem()
)

// TestFactTypes_AuditDeterministicEncoding is the G-1 fact-type
// audit: for EVERY analyzer wired into the registry (all linters, all
// govet passes, whole staticcheck family — enabled or not) and every
// analyzer reachable from those through Requires edges, reflect-walk
// each declared fact type and fail if any gob-reachable field is a
// map (random iteration order → nondeterministic fact bytes → build
// cache poisoning) or an interface (payload unknowable statically).
//
// The walk mirrors gob's encoding semantics: pointers deref, structs
// recurse over exported fields only (gob never encodes unexported
// fields), slices/arrays recurse into the element type, and types
// implementing gob.GobEncoder / encoding.BinaryMarshaler are opaque
// leaves.
func TestFactTypes_AuditDeterministicEncoding(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "all"
	// Pull in the off-by-default vet passes (shadow, fieldalignment,
	// …) so the govet family is audited in full.
	cfg.Linters.Settings.Govet.EnableAll = true

	reg, _, err := registry.BuildFromConfig(cfg)
	if err != nil {
		t.Fatalf("registry.BuildFromConfig: %v", err)
	}

	// Every wired analyzer, regardless of Status: disabled or
	// deferred rows still name real fact types someone can enable.
	var roots []*analysis.Analyzer
	for _, r := range reg.All() {
		if r.Analyzer != nil {
			roots = append(roots, r.Analyzer)
		}
	}
	if len(roots) == 0 {
		t.Fatal("registry produced no wired analyzers; audit has nothing to check")
	}

	// Transitive Requires closure.
	seen := map[*analysis.Analyzer]bool{}
	var closure []*analysis.Analyzer
	var walk func(a *analysis.Analyzer)
	walk = func(a *analysis.Analyzer) {
		if seen[a] {
			return
		}
		seen[a] = true
		closure = append(closure, a)
		for _, req := range a.Requires {
			walk(req)
		}
	}
	for _, a := range roots {
		walk(a)
	}

	// Audit each distinct fact type once, remembering every analyzer
	// that declares it for the failure report.
	type auditEntry struct {
		declaredBy []string
		offenders  []string
	}
	audited := map[reflect.Type]*auditEntry{}
	for _, a := range closure {
		for _, f := range a.FactTypes {
			ft := reflect.TypeOf(f)
			e, ok := audited[ft]
			if !ok {
				e = &auditEntry{offenders: auditFactType(ft)}
				audited[ft] = e
			}
			e.declaredBy = append(e.declaredBy, a.Name)
		}
	}
	if len(audited) == 0 {
		t.Fatalf("no FactTypes found across %d analyzers; audit is vacuous (registry wiring changed?)", len(closure))
	}

	var names []string
	for ft := range audited {
		names = append(names, ft.String())
	}
	sort.Strings(names)
	t.Logf("audited %d fact types across %d analyzers (%d registry roots):\n  %s",
		len(audited), len(closure), len(roots), strings.Join(names, "\n  "))

	for ft, e := range audited {
		if len(e.offenders) == 0 {
			continue
		}
		sort.Strings(e.declaredBy)
		report := fmt.Sprintf("fact type %s (declared by %s) has nondeterministically gob-encoded fields:\n  %s",
			ft, strings.Join(dedup(e.declaredBy), ", "), strings.Join(e.offenders, "\n  "))
		if reason, ok := factTypeAllowlist[ft.String()]; ok {
			t.Logf("ALLOWLISTED OFFENDER — %s\n  allowlist reason: %s", report, reason)
			continue
		}
		t.Errorf("%s\neither fix the fact type or allowlist it in factTypeAllowlist with a TODO", report)
	}
}

// auditFactType walks one concrete fact type and returns a report
// line per offending field path (type.Field.Field…), empty when the
// type's gob encoding is order-deterministic by construction.
func auditFactType(ft reflect.Type) []string {
	var out []string
	auditWalk(ft, ft.String(), map[reflect.Type]bool{}, &out)
	return out
}

func auditWalk(rt reflect.Type, path string, inProgress map[reflect.Type]bool, out *[]string) {
	// Custom encoders bypass gob's reflective walk entirely.
	if rt.Implements(gobEncoderType) || rt.Implements(binaryMarshalerType) ||
		(rt.Kind() != reflect.Pointer &&
			(reflect.PointerTo(rt).Implements(gobEncoderType) || reflect.PointerTo(rt).Implements(binaryMarshalerType))) {
		return
	}

	switch rt.Kind() {
	case reflect.Pointer:
		auditWalk(rt.Elem(), path, inProgress, out)
	case reflect.Map:
		*out = append(*out, fmt.Sprintf("%s: map type %s (gob iterates maps in random order)", path, rt))
	case reflect.Interface:
		*out = append(*out, fmt.Sprintf("%s: interface type %s (payload unknowable; gob encodes dynamic type)", path, rt))
	case reflect.Slice, reflect.Array:
		auditWalk(rt.Elem(), path+"[]", inProgress, out)
	case reflect.Struct:
		if inProgress[rt] {
			return // recursive type; already being audited on this path
		}
		inProgress[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			if !f.IsExported() {
				continue // gob never encodes unexported fields
			}
			auditWalk(f.Type, path+"."+f.Name, inProgress, out)
		}
		delete(inProgress, rt)
	default:
		// Scalar kinds (bool, ints, floats, complex, string) encode
		// deterministically. Chan/Func/UnsafePointer are not
		// gob-encodable at all — an analyzer shipping one fails at
		// runtime long before determinism matters.
	}
}

// dedup collapses adjacent duplicates in a sorted slice.
func dedup(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
