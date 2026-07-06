package cache

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestFileSetRoundTripEmpty is a smoke test: encode/decode of an
// otherwise-empty FileSet must succeed and yield a usable instance.
func TestFileSetRoundTripEmpty(t *testing.T) {
	fset := token.NewFileSet()
	data, err := EncodeFileSet(fset)
	if err != nil {
		t.Fatalf("EncodeFileSet: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("EncodeFileSet returned empty bytes")
	}
	got, err := DecodeFileSet(data)
	if err != nil {
		t.Fatalf("DecodeFileSet: %v", err)
	}
	if got == nil {
		t.Fatalf("DecodeFileSet returned nil FileSet")
	}
}

// TestFileSetPositionFidelity is the explicit Codex-requested CI test:
// build a FileSet over a real fixture package, encode +
// decode, and verify every token.Pos in the original AST resolves to
// the same (Filename, Line, Column) in the decoded FileSet.
func TestFileSetPositionFidelity(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "fmt"

// Greet says hello.
func Greet(name string) string {
	if name == "" {
		return "hello, world"
	}
	return fmt.Sprintf("hello, %s", name)
}

var Pi = 3.14159

type Point struct {
	X, Y int
}

func (p Point) Add(q Point) Point {
	return Point{X: p.X + q.X, Y: p.Y + q.Y}
}
`
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	origFset := token.NewFileSet()
	file, err := parser.ParseFile(origFset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	// Collect every position the parser produced, including end positions.
	type posRec struct {
		label string
		pos   token.Pos
	}
	var recs []posRec
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		recs = append(recs,
			posRec{label: "pos", pos: n.Pos()},
			posRec{label: "end", pos: n.End()},
		)
		return true
	})
	if len(recs) < 20 {
		t.Fatalf("fixture too small: collected only %d positions", len(recs))
	}

	data, err := EncodeFileSet(origFset)
	if err != nil {
		t.Fatalf("EncodeFileSet: %v", err)
	}

	decodedFset, err := DecodeFileSet(data)
	if err != nil {
		t.Fatalf("DecodeFileSet: %v", err)
	}

	mismatches := 0
	for _, rec := range recs {
		want := origFset.Position(rec.pos)
		got := decodedFset.Position(rec.pos)
		if want.Filename != got.Filename || want.Line != got.Line || want.Column != got.Column {
			if mismatches < 10 {
				t.Errorf("position mismatch at %s pos=%d: want %s, got %s",
					rec.label, rec.pos, want, got)
			}
			mismatches++
		}
	}
	if mismatches != 0 {
		t.Fatalf("%d/%d positions diverged after FileSet round-trip",
			mismatches, len(recs))
	}
}

// TestFileSetRoundTripDeterministic: encoding the same FileSet twice
// produces identical bytes. This is required for the L2 same-action
// invariant — two writers must produce bit-identical L2 envelopes.
func TestFileSetRoundTripDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n\nvar Y = 1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, path, nil, 0); err != nil {
		t.Fatalf("parse: %v", err)
	}
	a, err := EncodeFileSet(fset)
	if err != nil {
		t.Fatalf("encode a: %v", err)
	}
	b, err := EncodeFileSet(fset)
	if err != nil {
		t.Fatalf("encode b: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("non-deterministic encoding: len(a)=%d len(b)=%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic encoding at byte %d: %x vs %x", i, a[i], b[i])
		}
	}
}

func TestEncodeFileSetRejectsNil(t *testing.T) {
	if _, err := EncodeFileSet(nil); err == nil {
		t.Errorf("EncodeFileSet(nil) = nil error, want error")
	}
}

func TestDecodeFileSetRejectsEmpty(t *testing.T) {
	if _, err := DecodeFileSet(nil); err == nil {
		t.Errorf("DecodeFileSet(nil) = nil error, want error")
	}
}
