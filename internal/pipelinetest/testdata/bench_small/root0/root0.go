package root0

import (
	"example.com/bench/small/mid0"
	"example.com/bench/small/mid1"
)

// Run is the root's only entrypoint; the harness's Analyze loop
// reaches every imported mid-layer through this function.
func Run() {
	_ = mid0.Touch()
	_ = mid1.Touch()
}
