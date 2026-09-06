// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package queryscope detects query-builder calls that require an explicit
// scope predicate but have no configured scoping evidence.
package queryscope

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Config describes the query-builder syntax that proves a query is scoped.
// The analyzer is deliberately syntax-only: it can run incrementally without
// importing the query builder or requiring type information.
type Config struct {
	// UnscopedMethods add selectors that require separate scoping evidence.
	UnscopedMethods []string `yaml:"unscoped-methods,omitempty" json:"unscoped-methods,omitempty"`
	// ScopedMethods add selectors that carry or establish scope directly.
	ScopedMethods []string `yaml:"scoped-methods,omitempty" json:"scoped-methods,omitempty"`
	// ScopedTwoArgStringMethods establish scope when called with exactly two
	// arguments and a string literal as the second argument.
	ScopedTwoArgStringMethods []string `yaml:"scoped-two-arg-string-methods,omitempty" json:"scoped-two-arg-string-methods,omitempty"`
	// ScopePredicateMethods contain scope when an argument refers to a name in
	// ScopeFieldNames.
	ScopePredicateMethods []string `yaml:"scope-predicate-methods,omitempty" json:"scope-predicate-methods,omitempty"`
	ScopeFieldNames       []string `yaml:"scope-field-names,omitempty" json:"scope-field-names,omitempty"`
	// ScopeStringSubstrings recognize raw predicates represented as string
	// literals.
	ScopeStringSubstrings []string `yaml:"scope-string-substrings,omitempty" json:"scope-string-substrings,omitempty"`
	// OptOutMethodContains and OptOutMethods recognize deliberate escapes from
	// the query builder's default scope behavior.
	OptOutMethodContains []string `yaml:"opt-out-method-contains,omitempty" json:"opt-out-method-contains,omitempty"`
	OptOutMethods        []string `yaml:"opt-out-methods,omitempty" json:"opt-out-methods,omitempty"`
	// QueryParameterTypeNames skip wrappers that receive a caller-built query.
	QueryParameterTypeNames []string `yaml:"query-parameter-type-names,omitempty" json:"query-parameter-type-names,omitempty"`
	// ScopedMethodMinArgs establishes scope for a method only when it has at
	// least the configured number of arguments.
	ScopedMethodMinArgs map[string]int `yaml:"scoped-method-min-args,omitempty" json:"scoped-method-min-args,omitempty"`
	// IgnoreDirective is a doc-comment prefix whose non-empty suffix suppresses
	// a finding for a known-safe function.
	IgnoreDirective string `yaml:"ignore-directive,omitempty" json:"ignore-directive,omitempty"`
}

// Validate rejects ambiguous or ineffective non-zero configuration.
func (c Config) Validate() error {
	if c.empty() {
		return nil
	}
	if err := validateNames("unscoped-methods", c.UnscopedMethods, true); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"scoped-methods":                c.ScopedMethods,
		"scoped-two-arg-string-methods": c.ScopedTwoArgStringMethods,
		"scope-predicate-methods":       c.ScopePredicateMethods,
		"scope-field-names":             c.ScopeFieldNames,
		"scope-string-substrings":       c.ScopeStringSubstrings,
		"opt-out-method-contains":       c.OptOutMethodContains,
		"opt-out-methods":               c.OptOutMethods,
		"query-parameter-type-names":    c.QueryParameterTypeNames,
	} {
		if err := validateNames(name, values, false); err != nil {
			return err
		}
	}
	for method, minArgs := range c.ScopedMethodMinArgs {
		if strings.TrimSpace(method) == "" {
			return fmt.Errorf("scoped-method-min-args contains an empty method name")
		}
		if minArgs < 0 {
			return fmt.Errorf("scoped-method-min-args[%q] must be non-negative", method)
		}
	}
	if strings.TrimSpace(c.IgnoreDirective) == "" {
		return fmt.Errorf("ignore-directive must not be empty")
	}
	return nil
}

func (c Config) empty() bool {
	return len(c.UnscopedMethods) == 0 &&
		len(c.ScopedMethods) == 0 &&
		len(c.ScopedTwoArgStringMethods) == 0 &&
		len(c.ScopePredicateMethods) == 0 &&
		len(c.ScopeFieldNames) == 0 &&
		len(c.ScopeStringSubstrings) == 0 &&
		len(c.OptOutMethodContains) == 0 &&
		len(c.OptOutMethods) == 0 &&
		len(c.QueryParameterTypeNames) == 0 &&
		len(c.ScopedMethodMinArgs) == 0 &&
		c.IgnoreDirective == ""
}

func validateNames(field string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s must contain at least one method", field)
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains an empty value", field)
		}
	}
	return nil
}

// New constructs a queryscope analyzer with cfg. Callers should validate cfg
// during configuration loading; invalid zero-value configurations are a no-op.
func New(cfg Config) *analysis.Analyzer {
	settings := newSettings(cfg)
	return &analysis.Analyzer{
		Name: "queryscope",
		Doc:  "detects query-builder selectors with no configured scope evidence",
		Run: func(pass *analysis.Pass) (any, error) {
			for _, file := range pass.Files {
				checkFile(pass, file, settings)
			}
			return nil, nil
		},
	}
}

type settings struct {
	unscopedMethods           map[string]bool
	scopedMethods             map[string]bool
	scopedTwoArgStringMethods map[string]bool
	scopePredicateMethods     map[string]bool
	scopeFieldNames           map[string]bool
	scopeStringSubstrings     []string
	optOutMethodContains      []string
	optOutMethods             map[string]bool
	queryParameterTypeNames   map[string]bool
	scopedMethodMinArgs       map[string]int
	ignoreDirective           string
}

func newSettings(cfg Config) settings {
	return settings{
		unscopedMethods:           names(cfg.UnscopedMethods),
		scopedMethods:             names(cfg.ScopedMethods),
		scopedTwoArgStringMethods: names(cfg.ScopedTwoArgStringMethods),
		scopePredicateMethods:     names(cfg.ScopePredicateMethods),
		scopeFieldNames:           names(cfg.ScopeFieldNames),
		scopeStringSubstrings:     append([]string(nil), cfg.ScopeStringSubstrings...),
		optOutMethodContains:      append([]string(nil), cfg.OptOutMethodContains...),
		optOutMethods:             names(cfg.OptOutMethods),
		queryParameterTypeNames:   names(cfg.QueryParameterTypeNames),
		scopedMethodMinArgs:       cfg.ScopedMethodMinArgs,
		ignoreDirective:           cfg.IgnoreDirective,
	}
}

func names(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func checkFile(pass *analysis.Pass, file *ast.File, cfg settings) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || callerBuiltQuery(fn.Type, cfg) || ignoreDirective(fn.Doc, cfg.ignoreDirective) {
			continue
		}
		checkFunction(pass, fn.Body, cfg)
	}

	// Function literals inside declarations belong to their enclosing function.
	// Only package-level literals require their own analysis unit.
	ast.Inspect(file, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncDecl); ok {
			return false
		}
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		if !callerBuiltQuery(literal.Type, cfg) {
			checkFunction(pass, literal.Body, cfg)
		}
		return false
	})
}

func checkFunction(pass *analysis.Pass, body *ast.BlockStmt, cfg settings) {
	evidence := functionEvidence{}
	ast.Inspect(body, func(node ast.Node) bool {
		evidence.visit(node, cfg)
		return true
	})
	if evidence.unscopedPos.IsValid() && !evidence.hasScope {
		pass.Reportf(evidence.unscopedPos, "queryscope: selector requires an explicit configured scope")
	}
}

func callerBuiltQuery(fn *ast.FuncType, cfg settings) bool {
	if fn.Params == nil {
		return false
	}
	for _, field := range fn.Params.List {
		found := false
		ast.Inspect(field.Type, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if ok && cfg.queryParameterTypeNames[identifier.Name] {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func ignoreDirective(doc *ast.CommentGroup, directive string) bool {
	if doc == nil || directive == "" {
		return false
	}
	for _, comment := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		if reason, ok := strings.CutPrefix(text, directive); ok {
			return strings.TrimSpace(reason) != ""
		}
	}
	return false
}

type functionEvidence struct {
	unscopedPos token.Pos
	hasScope    bool
}

func (e *functionEvidence) visit(node ast.Node, cfg settings) {
	switch node := node.(type) {
	case *ast.CallExpr:
		selector, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		method := selector.Sel.Name
		if cfg.scopePredicateMethods[method] && hasScopeField(node.Args, cfg.scopeFieldNames) {
			e.hasScope = true
		}
		switch {
		case cfg.unscopedMethods[method] && !e.unscopedPos.IsValid():
			e.unscopedPos = selector.Sel.Pos()
		case cfg.scopedMethods[method]:
			e.hasScope = true
		case cfg.scopedTwoArgStringMethods[method] && isTwoArgStringCall(node):
			e.hasScope = true
		case cfg.optOutMethods[method]:
			e.hasScope = true
		case contains(method, cfg.optOutMethodContains):
			e.hasScope = true
		case hasMinArgs(method, len(node.Args), cfg.scopedMethodMinArgs):
			e.hasScope = true
		}
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return
		}
		value, err := strconv.Unquote(node.Value)
		if err == nil && contains(value, cfg.scopeStringSubstrings) {
			e.hasScope = true
		}
	}
}

func hasScopeField(args []ast.Expr, fields map[string]bool) bool {
	for _, arg := range args {
		found := false
		ast.Inspect(arg, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && fields[selector.Sel.Name] {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func isTwoArgStringCall(call *ast.CallExpr) bool {
	if len(call.Args) != 2 {
		return false
	}
	literal, ok := call.Args[1].(*ast.BasicLit)
	return ok && literal.Kind == token.STRING
}

func hasMinArgs(method string, args int, methods map[string]int) bool {
	minimum, ok := methods[method]
	return ok && args >= minimum
}

func contains(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
