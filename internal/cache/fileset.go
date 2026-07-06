package cache

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"go/token"
)

// EncodeFileSet writes fset as a self-contained byte slice.
//
// token.FileSet.Write is callback-driven: it hands the implementation an
// internal serializableFileSet value and expects a single encode call.
// Calling gob directly on the FileSet does not work — the layer of
// indirection is required. This wrapper hides that detail and exposes
// a plain []byte API.
//
// The resulting bytes are not guaranteed to be deterministic across
// Go versions (gob plus token.FileSet's internal layout can change),
// but they are deterministic for a fixed Go version and a fixed FileSet
// shape, which is what the L2 cache requires.
//
// Callers that pair the encoded FileSet with an L2 entry must pass a
// per-package FileSet (containing only the files owned by the package
// whose entry is being written), not a batch-wide FileSet. Encoding a
// batch-wide FileSet once per package produces O(packages²) on-disk
// growth.
func EncodeFileSet(fset *token.FileSet) ([]byte, error) {
	if fset == nil {
		return nil, fmt.Errorf("EncodeFileSet: nil FileSet")
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := fset.Write(enc.Encode); err != nil {
		return nil, fmt.Errorf("EncodeFileSet: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeFileSet is the inverse of EncodeFileSet. It returns a fresh
// *token.FileSet populated from data; positions in the returned FileSet
// resolve to the same Filename/Line/Column as the original FileSet for
// any token.Pos that was valid in the original.
//
// The returned FileSet is a separate instance from any other FileSet in
// the program: a fresh instance is returned rather than merging into the
// snapshot FileSet at the L2 read boundary.
func DecodeFileSet(data []byte) (*token.FileSet, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("DecodeFileSet: empty data")
	}
	fset := token.NewFileSet()
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := fset.Read(dec.Decode); err != nil {
		return nil, fmt.Errorf("DecodeFileSet: %w", err)
	}
	return fset, nil
}
