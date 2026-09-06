// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/analyzers/queryscope"
)

// wireAnalyzerFnsQueryscope wires the configurable syntax-only queryscope
// analyzer. Consumers describe their builder's scoping conventions in
// linters.settings.queryscope; plaid-lint itself has no dependency on a
// specific query builder or tenancy model.
func wireAnalyzerFnsQueryscope(c *catalog) {
	wireNativeFn(c, "queryscope", wireQueryscope)
}

func wireQueryscope(cfg any) []*analysis.Analyzer {
	settings := queryscope.Config{}
	if value, ok := cfg.(*queryscope.Config); ok && value != nil {
		settings = *value
	}
	return []*analysis.Analyzer{analyzers.RegisterSyntaxOnlyWithConfig(queryscope.New(settings), settings, 1)}
}
