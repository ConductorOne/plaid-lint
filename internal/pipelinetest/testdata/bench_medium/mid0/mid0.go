package mid0

import (
	"example.com/bench/medium/leaf0"
	"example.com/bench/medium/leaf1"
	"example.com/bench/medium/leaf2"
	"example.com/bench/medium/leaf3"
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
	leaf0.Use(leaf0.New("mid0_use_0"))
	leaf1.Use(leaf1.New("mid0_use_1"))
	leaf2.Use(leaf2.New("mid0_use_2"))
	leaf3.Use(leaf3.New("mid0_use_3"))
	return "mid0"
}
