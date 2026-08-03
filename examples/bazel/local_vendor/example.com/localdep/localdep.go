// Package localdep is a main-repository local-vendor dependency fixture.
package localdep

// Used is consumed by the first-party example application.
func Used() string { return "localdep" }

func localVendorDead() {}
