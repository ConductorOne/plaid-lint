package consumer

import "example.com/golden/factroundtrip/leaf"

// Use exercises the cross-package printf wrapper fact. Calling
// leaf.Errorf with a misformatted format string is what the warm-
// run printf analyzer detects when the leaf-side `isWrapper` fact
// successfully round-tripped from cold to warm via L1.
//
// The cold run produces the fact on leaf; the warm run hits L1 for
// leaf and must reconstruct an identical fact for consumer's pass
// to see the same diagnostic shape.
func Use() error {
	return leaf.Errorf("missing-arg %d") // intentional: %d with no arg
}
