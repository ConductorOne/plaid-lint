// Package exportgraph depends on a vendored package with a distinct
// importmap, a go_proto_library (whose types come from the protobuf
// runtime), and the standard library.
package exportgraph

import (
	"strings"

	"example.com/mapped"
	"example.com/plaidexample/exportgraph/greetpb"
)

// Describe returns a greeting's text and message name.
func Describe(text string) string {
	g := &greetpb.Greeting{Text: strings.Repeat(text, mapped.Value())}
	return g.GetText() + " " + string(g.ProtoReflect().Descriptor().FullName())
}
