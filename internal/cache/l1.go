package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// L1Entry is the per-(analyzer, package) cache record. Its
// content-addressed bytes are determined by the digest
// fields plus the analyzer/tool versions and the config salt.
//
// The cache layer encodes the entry deterministically; correctness of the
// digest fields themselves (i.e. that they actually summarize the right
// inputs) is the caller's responsibility — see W6.
type L1Entry struct {
	Analyzer        string
	PackageID       string
	InputDigest     [32]byte
	DepFactsDigest  [32]byte
	DepTypeDigest   [32]byte
	AnalyzerVersion string
	ConfigSalt      [32]byte
	ToolVersion     string

	Diagnostics  []json.RawMessage
	ObjectFacts  []byte
	PackageFacts []byte

	// Result is the optional serialised analyser Result for analysers
	// that the wider DAG consumes via analysis.Analyzer.Requires. When
	// non-empty, the L1 hit path restores it so downstream actions
	// see a non-nil pass.ResultOf entry. When empty, the analyser's
	// descriptor opted out of Result caching and a hit alone is not
	// sufficient to drive downstream actions — the W6 prereq-bypass
	// path is the fallback.
	Result []byte
}

// Encode returns the deterministic byte representation of e suitable for
// content-addressed storage. The same inputs always produce the same bytes.
func (e *L1Entry) Encode() ([]byte, error) {
	diagBytes, err := marshalDiagnostics(e.Diagnostics)
	if err != nil {
		return nil, fmt.Errorf("encode L1: diagnostics: %w", err)
	}
	sections := [][]byte{
		[]byte(e.Analyzer),
		[]byte(e.PackageID),
		e.InputDigest[:],
		e.DepFactsDigest[:],
		e.DepTypeDigest[:],
		[]byte(e.AnalyzerVersion),
		e.ConfigSalt[:],
		[]byte(e.ToolVersion),
		diagBytes,
		nonNil(e.ObjectFacts),
		nonNil(e.PackageFacts),
		nonNil(e.Result),
	}
	var buf bytes.Buffer
	if err := writeEnvelope(&buf, kindL1, sections); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeL1 is the inverse of L1Entry.Encode.
func DecodeL1(b []byte) (*L1Entry, error) {
	secs, err := readEnvelope(bytes.NewReader(b), kindL1)
	if err != nil {
		return nil, err
	}
	if len(secs) != 12 {
		return nil, fmt.Errorf("L1 envelope: want 12 sections, got %d", len(secs))
	}
	e := &L1Entry{
		Analyzer:        string(secs[0]),
		PackageID:       string(secs[1]),
		AnalyzerVersion: string(secs[5]),
		ToolVersion:     string(secs[7]),
		ObjectFacts:     emptyToNil(secs[9]),
		PackageFacts:    emptyToNil(secs[10]),
		Result:          emptyToNil(secs[11]),
	}
	if err := fillDigest(&e.InputDigest, secs[2], "InputDigest"); err != nil {
		return nil, err
	}
	if err := fillDigest(&e.DepFactsDigest, secs[3], "DepFactsDigest"); err != nil {
		return nil, err
	}
	if err := fillDigest(&e.DepTypeDigest, secs[4], "DepTypeDigest"); err != nil {
		return nil, err
	}
	if err := fillDigest(&e.ConfigSalt, secs[6], "ConfigSalt"); err != nil {
		return nil, err
	}
	diags, err := unmarshalDiagnostics(secs[8])
	if err != nil {
		return nil, err
	}
	e.Diagnostics = diags
	return e, nil
}

// WriteL1 atomically writes e to the cache under analyzer/<e.Analyzer>/<shard>/<id>.
// First-writer-wins: if another goroutine/process publishes the same id
// concurrently, this call succeeds without overwriting.
func (c *Cache) WriteL1(e *L1Entry, id ActionID) error {
	if err := validateAnalyzerName(e.Analyzer); err != nil {
		return fmt.Errorf("WriteL1: %w", err)
	}
	data, err := e.Encode()
	if err != nil {
		return err
	}
	return c.backend.Put(l1Namespace(e.Analyzer), id, data)
}

// ReadL1 reads and decodes the L1 entry for analyzer/id. Returns
// (nil, fs.ErrNotExist) on miss.
func (c *Cache) ReadL1(analyzer string, id ActionID) (*L1Entry, error) {
	if err := validateAnalyzerName(analyzer); err != nil {
		return nil, fmt.Errorf("ReadL1: %w", err)
	}
	data, err := c.backend.Get(l1Namespace(analyzer), id)
	if err != nil {
		return nil, err
	}
	return DecodeL1(data)
}

// HasL1 reports whether an L1 entry already exists on disk for
// (analyzer, id). Cheap: performs a single os.Stat and does not decode
// the entry. Callers that already have the entry's input identity
// (ActionID) can use HasL1 to elide redundant WriteL1 calls — the
// on-disk format is content-addressed, so a present file is by
// construction valid for that id. Mirrors HasL2.
//
// HasL1 returns false on any error, including validateAnalyzerName
// rejections: the WriteL1 path already enforces name validity, so a
// false negative here just falls through to the existing WriteL1 path
// which will surface the error explicitly.
func (c *Cache) HasL1(analyzer string, id ActionID) bool {
	if err := validateAnalyzerName(analyzer); err != nil {
		return false
	}
	return c.backend.Has(l1Namespace(analyzer), id)
}

// L1PathForTest exposes the on-disk path of an L1 entry. Test-only:
// production callers do not need to know the layout.
func (c *Cache) L1PathForTest(analyzer string, id ActionID) string {
	return c.l1Path(analyzer, id)
}

// ComputeL1ActionID derives the content-addressed key for e. The
// action ID is the SHA-256 of a structured digest over
// the inputs that semantically determine the entry's bytes:
//
//	actionID = sha256(Analyzer || PackageID || InputDigest ||
//	                  DepFactsDigest || DepTypeDigest ||
//	                  AnalyzerVersion || ConfigSalt || ToolVersion)
//
// Excluded by design (output, not input): Diagnostics, ObjectFacts,
// PackageFacts. Order matches the digest formula above.
func ComputeL1ActionID(e *L1Entry) ActionID {
	return NewActionID(
		[]byte(e.Analyzer),
		[]byte(e.PackageID),
		e.InputDigest[:],
		e.DepFactsDigest[:],
		e.DepTypeDigest[:],
		[]byte(e.AnalyzerVersion),
		e.ConfigSalt[:],
		[]byte(e.ToolVersion),
	)
}

// ConfigSaltForAnalyzer returns a ConfigSalt for an analyzer whose
// canonicalized config is the JSON bytes in canonical (W6 ships a stub:
// for analyzers with no config, pass nil and the salt is sha256 of the
// analyzer name alone; for analyzers with simple options pass the
// canonical-JSON bytes of the sorted-key options struct). W7 replaces
// this with the schema-aware canonicalizer.
func ConfigSaltForAnalyzer(analyzer string, canonical []byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(analyzer))
	if len(canonical) > 0 {
		// Length-prefix separates the two streams so that
		// ConfigSaltForAnalyzer("foobar", nil) and
		// ConfigSaltForAnalyzer("foo", []byte("bar")) produce different
		// salts even though their concatenations are equal.
		var lenBuf [8]byte
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(canonical)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(canonical)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ErrInvalidAnalyzerName is returned when an analyzer name supplied to
// WriteL1 / ReadL1 fails path-segment validation. Names are used as a
// directory component, so traversal patterns and control bytes are rejected.
type ErrInvalidAnalyzerName struct {
	Name   string
	Reason string
}

func (e *ErrInvalidAnalyzerName) Error() string {
	return fmt.Sprintf("invalid analyzer name %q: %s", e.Name, e.Reason)
}

// validateAnalyzerName rejects names that cannot safely be used as a single
// path segment under analyzer/. Reject criteria: empty; "." or ".."; any
// ".." path component; path separators ("/" or "\"); ":" (Windows drive /
// ADS); NUL or other control bytes; leading or trailing whitespace.
func validateAnalyzerName(name string) error {
	if name == "" {
		return &ErrInvalidAnalyzerName{Name: name, Reason: "empty"}
	}
	if name == "." || name == ".." {
		return &ErrInvalidAnalyzerName{Name: name, Reason: "reserved path component"}
	}
	if strings.TrimSpace(name) != name {
		return &ErrInvalidAnalyzerName{Name: name, Reason: "leading or trailing whitespace"}
	}
	for _, r := range name {
		switch r {
		case '/', '\\', ':':
			return &ErrInvalidAnalyzerName{Name: name, Reason: "contains path separator"}
		}
		if r < 0x20 || r == 0x7f {
			return &ErrInvalidAnalyzerName{Name: name, Reason: "contains control character"}
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return &ErrInvalidAnalyzerName{Name: name, Reason: "contains .. component"}
		}
	}
	return nil
}

func fillDigest(dst *[32]byte, src []byte, name string) error {
	if len(src) != 32 {
		return fmt.Errorf("%s: want 32 bytes, got %d", name, len(src))
	}
	copy(dst[:], src)
	return nil
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func emptyToNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Silence "imported and not used" if the file ever loses its uses; the
// import-list is small so this is just defensive.
var _ io.Reader = (*bytes.Reader)(nil)
