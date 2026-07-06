package consumer

import "example.com/safety/leafbody/leaf"

// Use calls into leaf.Public. The consumer's L1 action ID depends
// on leaf.gcexportdata (DepTypeDigest) and leaf's published facts
// (DepFactsDigest). Editing helper (unexported) doesn't change
// either, so consumer must NOT re-analyze.

func Use(s string) string {
	return leaf.Public(s) + "-consumer"
}
