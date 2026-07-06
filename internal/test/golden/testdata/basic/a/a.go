package a

// Self-assignment triggers the `assign` analyzer (the W6 root set
// includes assign; see internal/analyzers/bundled.go). One known
// diagnostic per cold run is enough to verify the diagnostic stream
// is non-empty; we don't depend on the exact message text to avoid
// coupling to upstream wording bumps.

type T struct {
	Name string
}

func New(name string) *T { return &T{Name: name} }

func Use(t *T) string {
	if t == nil {
		return ""
	}
	t.Name = t.Name // assign self-assignment trigger
	return t.Name
}
