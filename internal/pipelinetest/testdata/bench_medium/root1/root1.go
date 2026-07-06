package root1

import (
	"example.com/bench/medium/mid1"
	"example.com/bench/medium/mid2"
	"example.com/bench/medium/mid3"
	"example.com/bench/medium/mid4"
	"example.com/bench/medium/mid5"
	"example.com/bench/medium/mid0"
)

// Run is the root's only entrypoint; the harness's Analyze loop
// reaches every imported mid-layer through this function.
func Run() {
	_ = mid1.Touch()
	_ = mid2.Touch()
	_ = mid3.Touch()
	_ = mid4.Touch()
	_ = mid5.Touch()
	_ = mid0.Touch()
}
