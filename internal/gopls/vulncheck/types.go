// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package vulncheck is a minimal stub. The forked cache code does not
// run vulnerability scanning (mod_vuln.go was dropped at fork time),
// but protocol/command's interface.go still references vulncheck.Result
// in its declared command signatures. Carrying the empty type here
// satisfies the type system without bringing in govulncheck.
package vulncheck

// Result is a stub for the vulnerability-scan output type.
type Result struct{}
