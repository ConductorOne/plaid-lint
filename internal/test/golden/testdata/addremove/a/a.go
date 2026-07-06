package a

// Base file: defines a type and constructor with no diagnostics.
// The file-add subtest writes a NEW file (added.go) into this
// directory and re-runs; the file-delete subtest removes that
// added file and re-runs again. Both subtests verify the
// re-analysis covers the modified file set.

type Base struct {
	Name string
}

func NewBase(name string) *Base { return &Base{Name: name} }
