package leaf

// helper is non-exported; mutating its body changes the leaf
// package's InputDigest but does NOT change gcexportdata (the
// function is unexported) and does NOT change the printf-fact set
// (it's not a printf wrapper). Cascade should be {leaf} only.

func helper(s string) string {
	return s + "-leaf"
}

// Public is exported but stays untouched; it pins the package's
// exported surface so the consumer's DepTypeDigest doesn't shift
// when the leaf body changes.
func Public(s string) string {
	return helper(s)
}
