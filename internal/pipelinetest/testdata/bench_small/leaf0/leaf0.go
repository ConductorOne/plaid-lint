package leaf0

// T is a marker type the mid-layer references.
type T struct {
	Name string
}

// New is the leaf's only constructor; touched transitively by
// every importer.
func New(name string) *T { return &T{Name: name} }

// Use is a no-op the importer calls so the import is non-trivial.
func Use(t *T) string {
	if t == nil {
		return ""
	}
	return t.Name
}
