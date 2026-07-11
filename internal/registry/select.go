// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"sort"
	"strings"
)

// SelectAnalyzers returns a registry whose enabled set contains only the
// named analysis roots. Analyzer prerequisites are not part of the registry's
// enabled set; the engine adds their transitive Requires graph automatically.
func (r *Registry) SelectAnalyzers(names []string) (*Registry, error) {
	if r == nil {
		return nil, fmt.Errorf("select analyzers from a nil registry")
	}

	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			want[name] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("analyzer selector is empty")
	}

	available := make(map[string]struct{})
	selected := make([]Resolved, 0, len(want))
	for _, rr := range r.enabled {
		if rr.Analyzer == nil {
			continue
		}
		available[rr.Analyzer.Name] = struct{}{}
		if _, ok := want[rr.Analyzer.Name]; ok {
			selected = append(selected, rr)
		}
	}

	missing := make([]string, 0)
	for name := range want {
		if _, ok := available[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("analyzer(s) not enabled or unknown: %s", strings.Join(missing, ", "))
	}

	out := *r
	out.enabled = selected
	return &out, nil
}
