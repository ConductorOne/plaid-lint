package root3

import (
	"example.com/bench/cascade/mid0"
)

// Run is the root's only entrypoint; the harness's Analyze loop
// reaches every imported mid-layer through this function.
func Run() {
	_ = mid0.Touch()
}
