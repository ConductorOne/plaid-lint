// External test package with its own unused private declaration —
// analyzed normally, so xHelper MUST be reported.
package unusedpkg_test

import (
	"testing"

	"example.com/plaidexample/unusedpkg"
)

func xHelper() int { return 4 }

func TestExported(t *testing.T) {
	if unusedpkg.Exported() != 1 {
		t.Fatal("nope")
	}
}
