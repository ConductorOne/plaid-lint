// Package dependency is a main-repository vendored dependency fixture.
package dependency

// Used is consumed by the first-party example application.
func Used() string { return "dependency" }

func vendorDead() {}
