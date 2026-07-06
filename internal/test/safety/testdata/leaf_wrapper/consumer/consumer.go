package consumer

import "example.com/safety/leafwrapper/leaf"

// Use calls leaf.Errorf with a malformed format string. The cold
// run produces a printf diagnostic on consumer.go because the
// printf analyzer consumed leaf's isWrapper fact.

func Use() error {
	return leaf.Errorf("missing-arg %d") // intentional: %d with no arg
}
