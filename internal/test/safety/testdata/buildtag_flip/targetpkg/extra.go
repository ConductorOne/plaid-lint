//go:build linux || darwin

package targetpkg

// Extra is exported but unused by any consumer. The safety subtest
// flips this file's build tag to exclude it on the current platform.
// The exclusion removes Extra from targetpkg's exported surface, so
// targetpkg's gcexportdata changes. Every consumer of targetpkg
// therefore sees a different DepTypeDigest and re-analyzes — even
// though the consumers never referenced Extra.

type Extra struct {
	Tag string
}

func NewExtra(tag string) *Extra { return &Extra{Tag: tag} }
