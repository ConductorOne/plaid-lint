package mid

import "example.com/safety/midtype/leaf"

// MidT is the mid-graph struct whose exported field set will be
// extended by the midtype safety subtest. Adding a field changes
// leaf's transitive consumers' gcexportdata-derived DepTypeDigest,
// so every consumer of mid must re-analyze.

type MidT struct {
	Underlying *leaf.LeafT
}

func New(name string) *MidT {
	return &MidT{Underlying: leaf.New(name)}
}
