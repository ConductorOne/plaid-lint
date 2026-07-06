package mid2

import (
	"example.com/bench/medium/leaf2"
	"example.com/bench/medium/leaf3"
	"example.com/bench/medium/leaf4"
	"example.com/bench/medium/leaf5"
)

// MidT mirrors the leaf type so the root layer sees a
// non-trivial mid-package surface.
type MidT struct {
	Name string
}

// New constructs a MidT; called by the root layer so the
// mid-package is non-trivially used.
func New(name string) *MidT { return &MidT{Name: name} }

// Touch runs every imported leaf's Use(New(...)) so the analyzer
// graph has cross-package work to do.
func Touch() string {
	leaf2.Use(leaf2.New("mid2_use_0"))
	leaf3.Use(leaf3.New("mid2_use_1"))
	leaf4.Use(leaf4.New("mid2_use_2"))
	leaf5.Use(leaf5.New("mid2_use_3"))
	return "mid2"
}
