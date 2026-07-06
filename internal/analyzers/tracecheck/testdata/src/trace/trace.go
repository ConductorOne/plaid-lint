package trace

import (
	"context"
)

type ATracer struct{}

func (_ ATracer) Start(ctx context.Context, s string) (error, error) {
	return nil, nil
}

func Tracer(s string) ATracer {
	return ATracer{}
}

func HasValidTraceName() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "has_valid_trace_name")
}

func HasValidTraceNameWithIDs() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "has_valid_trace_name_with_ids")
}

func HasExactTraceName() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "HasExactTraceName")
}

func HasExactTraceNameWithIDs() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "HasExactTraceNameWithIDs")
}

func ListConnectorPrincipals() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "list_connector_principals")
}

func HasBadTraceName() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "has_wrong_trace_name") // want "Span Name does not match Function Name"
}

func hasEmptyTraceName() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "") // want "Span Name does not match Function Name"
}

func copyItem(a string, b string) (error, error) {
	return nil, nil
}

func variousNonTraceThings() {
	ctx := context.Background()
	_, _ = Tracer("tracer").Start(ctx, "various_non_trace_things")
	_, _ = copyItem("a", "b")
	_, _ = copyItem("a", "")
	_ = make([]string, 0)
	_ = make([]func(), 0)
	_ = make([]*CustomStruct, 0)
	thing := make([]byte, 32)
	_ = thing
}

type CustomStruct struct {
}

// archSqrt has no Go body — its implementation lives in a .s file
// (assembly-backed). A FuncDecl with a nil Body is legal AST and must
// not panic the analyzer. Regression guard for the nil-Body crash.
func archSqrt(x float64) float64

func NewRequest(a string, b string, c error) (string, error) {
	return "", nil
}

func Verify(ctx context.Context) (bool, error) {
	_ = func() {}
	// Using the features API endpoint to verify we can use credentials to make requests.
	url := "aurl"
	_, err := NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	_ = make([]*byte, 0)
	return false, err
}
