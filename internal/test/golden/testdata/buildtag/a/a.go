package a

// Common file: always included regardless of build tag. Provides a
// non-empty package surface so the package compiles even when the
// tagged file is excluded.

type T struct {
	Name string
}

func New(name string) *T { return &T{Name: name} }
