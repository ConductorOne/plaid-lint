// Package mapped is a vendored dependency whose compiler package path
// (importmap) differs from its import path.
package mapped

// Value is consumed by the exportgraph fixture.
func Value() int { return 1 }
