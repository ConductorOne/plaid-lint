package mid

import "example.com/safety/gomodbump/leaf"

type MidT struct {
	Underlying *leaf.LeafT
}

func New(name string) *MidT { return &MidT{Underlying: leaf.New(name)} }
