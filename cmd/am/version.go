package main

import (
	"fmt"
	"runtime/debug"
)

// version reports the build version. With `go install …@vX.Y.Z` it reflects the
// tag; with a plain `go build` it shows "devel".
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "devel"
}

func cmdVersion() { fmt.Printf("am %s\n", version()) }
