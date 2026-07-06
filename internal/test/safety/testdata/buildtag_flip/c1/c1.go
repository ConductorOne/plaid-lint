package c1

import "example.com/safety/buildtagflip/targetpkg"

func Use1() *targetpkg.Core { return targetpkg.NewCore("c1") }
