package mid4

import (
	"example.com/bench/medium/leaf4"
	"example.com/bench/medium/leaf5"
	"example.com/bench/medium/leaf6"
	"example.com/bench/medium/leaf7"
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
	leaf4.Use(leaf4.New("mid4_use_0"))
	leaf5.Use(leaf5.New("mid4_use_1"))
	leaf6.Use(leaf6.New("mid4_use_2"))
	leaf7.Use(leaf7.New("mid4_use_3"))
	return "mid4"
}
