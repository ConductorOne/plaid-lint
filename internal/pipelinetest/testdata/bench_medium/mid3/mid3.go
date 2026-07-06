package mid3

import (
	"example.com/bench/medium/leaf3"
	"example.com/bench/medium/leaf4"
	"example.com/bench/medium/leaf5"
	"example.com/bench/medium/leaf6"
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
	leaf3.Use(leaf3.New("mid3_use_0"))
	leaf4.Use(leaf4.New("mid3_use_1"))
	leaf5.Use(leaf5.New("mid3_use_2"))
	leaf6.Use(leaf6.New("mid3_use_3"))
	return "mid3"
}
