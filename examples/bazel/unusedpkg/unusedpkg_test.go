package unusedpkg

import "testing"

func TestTestedOnly(t *testing.T) {
	if testedOnly() != 2 {
		t.Fatal("nope")
	}
}
