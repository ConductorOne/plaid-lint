package cache

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/gcexportdata"
)

// EncodeExportData serialises the exported types of pkg into the gcexportdata
// wire format that ReadExportData understands. fset must contain positions for
// every declaration in pkg; gcexportdata.Write records file/line/column
// information against it.
//
// The blob is opaque to the cache layer (it is stored in L2Entry.ExportData)
// and round-trips through ReadExportData without consulting the producing
// package again. Callers should pair this with EncodeFileSet so that the
// reader side can rehydrate the FileSet the positions resolve against.
func EncodeExportData(fset *token.FileSet, pkg *types.Package) ([]byte, error) {
	if fset == nil {
		return nil, fmt.Errorf("EncodeExportData: nil FileSet")
	}
	if pkg == nil {
		return nil, fmt.Errorf("EncodeExportData: nil package")
	}
	var buf bytes.Buffer
	if err := gcexportdata.Write(&buf, fset, pkg); err != nil {
		return nil, fmt.Errorf("EncodeExportData: %w", err)
	}
	return buf.Bytes(), nil
}

// ReadExportData decodes a blob produced by EncodeExportData (or by the
// standard Go compiler) and returns the resulting *types.Package.
//
// fset receives file/line/column entries for the decoded positions; the
// caller controls whether that is the snapshot's master FileSet or a
// per-dep instance.
//
// imports may be nil; if non-nil, ReadExportData consults it for already-
// loaded transitive deps and inserts the result for path. path is the
// package import path; it cannot be empty.
func ReadExportData(fset *token.FileSet, imports map[string]*types.Package, path string, data []byte) (*types.Package, error) {
	if fset == nil {
		return nil, fmt.Errorf("ReadExportData: nil FileSet")
	}
	if path == "" {
		return nil, fmt.Errorf("ReadExportData: empty package path")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("ReadExportData: empty data")
	}
	if imports == nil {
		imports = make(map[string]*types.Package)
	}
	pkg, err := gcexportdata.Read(bytes.NewReader(data), fset, imports, path)
	if err != nil {
		return nil, fmt.Errorf("ReadExportData(%q): %w", path, err)
	}
	return pkg, nil
}
