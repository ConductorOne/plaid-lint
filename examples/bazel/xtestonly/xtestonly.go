// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package xtestonly's test target has ONLY external (package
// xtestonly_test) test sources and no embed — the c1 //pkg/ssf/caep
// shape. Its internal test archive's importpath is inferred from the
// go_test label and therefore ALSO ends in "_test"; classification
// must use source.testfilter, not the importpath suffix, or both
// archives collide on the .xtest output names.
package xtestonly

// Double doubles v.
func Double(v int) int { return v * 2 }
