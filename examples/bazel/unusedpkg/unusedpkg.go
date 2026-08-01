// Package unusedpkg is the acceptance fixture for unused
// enforcement semantics (UNUSED-AGGREGATION-HANDOFF.md):
//   - Exported is public API: never a finding (ExportedIsUsed).
//   - testedOnly is referenced only by the in-package test: the
//     internal test archive's run supersedes the library run's
//     finding, so the suite must NOT report it.
//   - neverUsed has no referent anywhere: the suite MUST report it.
package unusedpkg

// Exported has no in-repo caller; exported declarations are treated
// as used.
func Exported() int { return 1 }

func testedOnly() int { return 2 }

func neverUsed() int { return 3 }
