package cache

import (
	"bytes"
	"fmt"
)

// ComputeL2ActionID derives the content-addressed key for e from the
// inputs that semantically determine the entry's bytes.
//
// The inputs are: package ID; the sorted (source-file,
// content-hash) pairs; the build constraints; the Go version; and the
// sorted recursive list of dep action IDs. The L2Entry shape from W4
// pre-aggregates those into [InputDigest, DepTypeDigest, BuildEnv,
// GoVersion, ToolVersion]: the caller is responsible for SHA-256'ing
// the sorted source-file set into InputDigest and the sorted dep
// action-ID list into DepTypeDigest (we kept
// W4's pre-digested schema rather than re-expanding the per-field
// list). ComputeL2ActionID just folds these into the canonical
// length-prefixed action-ID hash.
//
// Excluded by design (output, not input): ExportData, FactsBlob.
func ComputeL2ActionID(e *L2Entry) ActionID {
	return NewActionID(
		[]byte(e.PackageID),
		[]byte(e.GoVersion),
		[]byte(e.BuildEnv),
		e.InputDigest[:],
		e.DepTypeDigest[:],
		[]byte(e.ToolVersion),
	)
}

// L2Entry is the per-package typecheck cache record.
// L2 stores the inputs to "package X compiled cleanly" — the export blob
// other packages can import-bind against, plus a facts blob.
//
// As with L1, the cache layer guarantees deterministic encoding; correctness
// of the digest fields is the caller's responsibility (W5).
type L2Entry struct {
	PackageID     string
	GoVersion     string // e.g. "go1.26"
	BuildEnv      string // GOOS/GOARCH/cgo bits, joined deterministically by the caller
	InputDigest   [32]byte
	DepTypeDigest [32]byte
	ToolVersion   string

	ExportData []byte // gcexportdata blob (opaque to the cache)
	FactsBlob  []byte // serialized facts (opaque to the cache)
	// FileSetSnapshot is future-proofing for cross-process consumers
	// (e.g. W8's daemon-shared cache reads): a serialized token.FileSet
	// — produced by EncodeFileSet — that lets a different process
	// rehydrate file/line/column positions for the symbols in
	// ExportData.
	//
	// The current in-process L2 read path in
	// internal/gopls/cache/check.go ignores FileSetSnapshot and decodes
	// ExportData straight into the batch's master FileSet via
	// gcexportdata.Read(b.fset, ...). Within a single process that path
	// is correct and cheaper than round-tripping through the snapshot,
	// so FileSetSnapshot is not yet a correctness dependency; it is
	// stored to keep the on-disk L2 format forward-compatible.
	//
	// FileSetSnapshot is per-package: it carries only the *token.File
	// entries owned by this package, with their original Base offsets
	// preserved so positions remain consistent with ExportData. Writing
	// a batch-wide FileSet here once per package produces O(packages²)
	// on-disk growth on large closures.
	FileSetSnapshot []byte
}

// Encode returns the deterministic byte representation of e.
func (e *L2Entry) Encode() ([]byte, error) {
	sections := [][]byte{
		[]byte(e.PackageID),
		[]byte(e.GoVersion),
		[]byte(e.BuildEnv),
		e.InputDigest[:],
		e.DepTypeDigest[:],
		[]byte(e.ToolVersion),
		nonNil(e.ExportData),
		nonNil(e.FactsBlob),
		nonNil(e.FileSetSnapshot),
	}
	var buf bytes.Buffer
	if err := writeEnvelope(&buf, kindL2, sections); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeL2 is the inverse of L2Entry.Encode.
func DecodeL2(b []byte) (*L2Entry, error) {
	secs, err := readEnvelope(bytes.NewReader(b), kindL2)
	if err != nil {
		return nil, err
	}
	if len(secs) != 9 {
		return nil, fmt.Errorf("L2 envelope: want 9 sections, got %d", len(secs))
	}
	e := &L2Entry{
		PackageID:       string(secs[0]),
		GoVersion:       string(secs[1]),
		BuildEnv:        string(secs[2]),
		ToolVersion:     string(secs[5]),
		ExportData:      emptyToNil(secs[6]),
		FactsBlob:       emptyToNil(secs[7]),
		FileSetSnapshot: emptyToNil(secs[8]),
	}
	if err := fillDigest(&e.InputDigest, secs[3], "InputDigest"); err != nil {
		return nil, err
	}
	if err := fillDigest(&e.DepTypeDigest, secs[4], "DepTypeDigest"); err != nil {
		return nil, err
	}
	return e, nil
}

// WriteL2 atomically writes e to the cache under typecheck/<shard>/<id>.
func (c *Cache) WriteL2(e *L2Entry, id ActionID) error {
	data, err := e.Encode()
	if err != nil {
		return err
	}
	return c.backend.Put(nsL2, id, data)
}

// ReadL2 reads and decodes the L2 entry for id. Returns (nil, fs.ErrNotExist)
// on miss.
func (c *Cache) ReadL2(id ActionID) (*L2Entry, error) {
	data, err := c.backend.Get(nsL2, id)
	if err != nil {
		return nil, err
	}
	return DecodeL2(data)
}

// HasL2 reports whether an L2 entry already exists on disk for id. Cheap:
// performs a single os.Stat and does not decode the entry. Callers that
// already have the entry's input identity (ActionID) can use HasL2 to
// elide redundant WriteL2 calls — the on-disk format is content-
// addressed, so a present file is by construction valid for that id.
func (c *Cache) HasL2(id ActionID) bool {
	return c.backend.Has(nsL2, id)
}

// L2PathForTest exposes the on-disk path of an L2 entry. Test-only:
// production callers do not need to know the layout.
func (c *Cache) L2PathForTest(id ActionID) string {
	return c.l2Path(id)
}
