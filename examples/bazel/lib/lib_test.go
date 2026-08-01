package lib

import "testing"

func TestTestOnly(t *testing.T) {
	if testOnly() != 42 {
		t.Fatal("nope")
	}
}
