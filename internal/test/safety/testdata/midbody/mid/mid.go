package mid

import "example.com/safety/midbody/leaf"

// MidT and its constructor stay untouched on the midbody edit.
// The midbody subtest changes the helper (unexported) function's
// body instead.

type MidT struct {
	Underlying *leaf.LeafT
}

func New(name string) *MidT {
	return &MidT{Underlying: leaf.New(helper(name))}
}

// helper is unexported; the safety subtest edits its body. Because
// gcexportdata only includes exported symbols, mid's exported
// surface is byte-stable across the edit, and consumers' L1 action
// IDs do not change.

func helper(s string) string {
	return s + "-midbody"
}
