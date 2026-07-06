package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// ActionID is the content-addressed key for a cache entry. It is the
// SHA-256 of a structured digest of all inputs that semantically determine
// the entry's bytes.
//
// Constructors are responsible for assembling the input slices in a
// deterministic order; ActionID does not normalize ordering on the
// caller's behalf.
type ActionID [sha256.Size]byte

// Hex returns the lowercase hex encoding of the action ID (64 chars).
func (id ActionID) Hex() string { return hex.EncodeToString(id[:]) }

// String implements fmt.Stringer.
func (id ActionID) String() string { return id.Hex() }

// NewActionID computes a SHA-256 over a structured concatenation of parts.
// Each part is length-prefixed (uint64 LE) so that ("ab","cd") and
// ("a","bcd") produce distinct digests. This is the canonical structured
// digest.
func NewActionID(parts ...[]byte) ActionID {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(p)
	}
	var out ActionID
	copy(out[:], h.Sum(nil))
	return out
}

// ShardPath returns the "<2-char-hex>/<actionID-hex>" relative path used
// for sharded on-disk storage. Exported for diagnostic / test use; the
// L1/L2 path helpers on Cache call it internally.
func ShardPath(id ActionID) string {
	hex := id.Hex()
	return hex[:2] + "/" + hex
}

// ParseActionID parses a 64-char lowercase hex action ID. Returns ok=false
// on any malformed input.
func ParseActionID(s string) (ActionID, bool) {
	var id ActionID
	if len(s) != 2*sha256.Size {
		return id, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, false
	}
	copy(id[:], b)
	return id, true
}
