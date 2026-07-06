package leaf

// Errorf is a placeholder printf-style wrapper. The printf analyzer
// detects "isWrapper" via call-site analysis (a wrapper forwards
// format+args to another printf-family function); Errorf does NOT
// forward to stdlib here on purpose — the golden fixture is
// deliberately stdlib-free so L2 counters stay deterministic. The
// fact-roundtrip claim is the observable: when the leaf package's
// L1 entry round-trips correctly on warm, consumer's L1 lookup
// hits and the warm-run digest matches the cold-run digest.

func Errorf(format string, args ...any) error {
	return errImpl(format, args)
}

func errImpl(format string, args []any) error {
	_ = format
	_ = args
	return nil
}
