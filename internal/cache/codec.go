package cache

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// envelopeMagic identifies a plaid-lint cache entry blob. Changing the
// magic forces a CacheVersion bump and effectively invalidates all entries.
var envelopeMagic = []byte{'C', '1', 'C', 'L'} // "C1CL" = plaid-lint cache (C1 namespace)

// kindL1 / kindL2 disambiguate entry envelopes. If a future caller asks
// for an L1 path but the bytes on disk happen to decode as a valid L2,
// the kind byte makes that an explicit error.
const (
	kindL1 byte = 1
	kindL2 byte = 2
)

// writeEnvelope frames a single byte slice with magic + kind + length.
// Format:
//
//	[0..3] magic "C1CL"
//	[4]    kind (kindL1 or kindL2)
//	[5..]  uint32-LE length-prefixed sections...
//
// The framing is hand-rolled rather than using gob/proto because we need
// the byte representation to be deterministic per the same-action
// invariant: identical inputs MUST produce identical bytes so that the
// link(2) O_EXCL primitive's first-writer-wins semantics are observable
// at the bytes level, not just the actionID level.
func writeEnvelope(w io.Writer, kind byte, sections [][]byte) error {
	if _, err := w.Write(envelopeMagic); err != nil {
		return err
	}
	if _, err := w.Write([]byte{kind}); err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(sections)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	for _, s := range sections {
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(s)))
		if _, err := w.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := w.Write(s); err != nil {
			return err
		}
	}
	return nil
}

// readEnvelope is the inverse of writeEnvelope. Returns the sections list
// and an error if the magic/kind don't match.
func readEnvelope(r io.Reader, wantKind byte) ([][]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("read envelope header: %w", err)
	}
	if !bytes.Equal(hdr[:4], envelopeMagic) {
		return nil, fmt.Errorf("bad envelope magic: %q", hdr[:4])
	}
	if hdr[4] != wantKind {
		return nil, fmt.Errorf("envelope kind mismatch: want %d, got %d", wantKind, hdr[4])
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read section count: %w", err)
	}
	nSections := binary.LittleEndian.Uint32(lenBuf[:])
	if nSections > 1<<20 {
		return nil, fmt.Errorf("absurd section count: %d", nSections)
	}
	sections := make([][]byte, nSections)
	for i := range sections {
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return nil, fmt.Errorf("read section %d length: %w", i, err)
		}
		n := binary.LittleEndian.Uint32(lenBuf[:])
		if n > 1<<28 { // 256 MiB cap per section; far above any expected payload.
			return nil, fmt.Errorf("section %d too large: %d", i, n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("read section %d body: %w", i, err)
		}
		sections[i] = buf
	}
	return sections, nil
}

// marshalDiagnostics encodes a slice of json.RawMessage diagnostics into a
// single deterministic byte stream. Each element is re-encoded with
// canonicalJSON to sort object keys; the result is a JSON array.
//
// Determinism note: callers SHOULD construct diagnostics with sorted keys
// up front, but canonicalJSON acts as a safety net for cases where a
// diagnostic was built via map[string]any and Go's map iteration randomized
// the order, preserving the same-action invariant.
func marshalDiagnostics(diags []json.RawMessage) ([]byte, error) {
	if len(diags) == 0 {
		return []byte("[]"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, d := range diags {
		if i > 0 {
			buf.WriteByte(',')
		}
		canon, err := canonicalJSON(d)
		if err != nil {
			return nil, fmt.Errorf("canonicalize diagnostic %d: %w", i, err)
		}
		buf.Write(canon)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// unmarshalDiagnostics decodes the inverse of marshalDiagnostics.
func unmarshalDiagnostics(b []byte) ([]json.RawMessage, error) {
	if len(b) == 0 || bytes.Equal(b, []byte("[]")) {
		return nil, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal diagnostics: %w", err)
	}
	return out, nil
}

// canonicalJSON re-encodes a JSON value with object keys sorted
// lexicographically and whitespace stripped. It is deterministic on any
// valid JSON input.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case json.Number:
		buf.WriteString(string(x))
		return nil
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sortStrings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	default:
		// Float fallback for any path that didn't go through UseNumber.
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}

// sortStrings sorts in place; small wrapper to avoid pulling "sort" into
// every call site for a one-liner.
func sortStrings(s []string) {
	// Simple insertion sort: callers pass JSON-object key sets which are
	// typically small (<32 keys). Avoids the sort.Strings allocation.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
