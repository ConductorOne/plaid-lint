// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/gcexportdata"
)

// encodeExport renders the type-checked package as deep gc export
// data (gcexportdata.Write), the out.export payload.
//
// Deep — not the shallow format gopls and the engine's L2 cache use —
// because a dependent action's importcfg names only its DIRECT deps:
// every type reachable from this package's API, including those
// declared in its own dependencies, must be decodable from this one
// file, exactly as with compiler export data.
//
// A package that failed to type-check still gets an export file (its
// dependents' actions need their declared input), describing whatever
// the checker managed to type; the export writer encodes invalid types
// for that case. If encoding still fails, an empty package with the
// same path and name is written instead and the returned warning says
// so: dependents then report the missing declarations as typecheck
// findings, which is how a broken dependency surfaces anyway.
func encodeExport(cp *checkedPackage) (blob []byte, warning string, err error) {
	blob, encErr := writeExportData(cp.fset, cp.pkg)
	if encErr == nil {
		return blob, "", nil
	}
	empty := types.NewPackage(cp.pkg.Path(), cp.pkg.Name())
	empty.MarkComplete()
	blob, err = writeExportData(token.NewFileSet(), empty)
	if err != nil {
		return nil, "", fmt.Errorf("unit: encode export data for %s: %w", cp.pkg.Path(), err)
	}
	return blob, fmt.Sprintf("package %s: export data could not be encoded (%v); wrote an empty package", cp.pkg.Path(), encErr), nil
}

// writeExportData is gcexportdata.Write into a buffer, with a panic in
// the encoder (it asserts on malformed type graphs) turned into an
// error.
func writeExportData(fset *token.FileSet, pkg *types.Package) (blob []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("export writer panicked: %v", r)
		}
	}()
	var buf bytes.Buffer
	if err := gcexportdata.Write(&buf, fset, pkg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
