package memtag_test

import (
	"testing"

	"example.com/plaidexample/memtag"
)

func TestDoubleExternal(t *testing.T) {
	if memtag.Double(3) != 6 {
		t.Fatal("Double(3) != 6")
	}
}
