package leaf

type LeafT struct {
	Name string
}

func New(name string) *LeafT { return &LeafT{Name: name} }
