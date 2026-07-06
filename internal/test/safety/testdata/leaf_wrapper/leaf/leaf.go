package leaf

// Errorf is a printf-style wrapper. The printf analyzer detects
// this and publishes the `isWrapper` fact about Errorf, which
// propagates to any package importing leaf and calling Errorf.

func Errorf(format string, args ...any) error {
	return errImpl(format, args)
}

func errImpl(format string, args []any) error {
	_ = format
	_ = args
	return nil
}
