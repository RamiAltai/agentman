package main

import (
	"fmt"
	"runtime/debug"
)

// injectedVersion can be set at build time for release binaries:
//
//	go build -ldflags "-X main.injectedVersion=v0.3.0"
var injectedVersion string

// version reports the build version. Precedence: -ldflags injection, then the
// module version from `go install …@vX.Y.Z`, else "devel" for a plain `go build`.
func version() string {
	if injectedVersion != "" {
		return injectedVersion
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "devel"
}

func cmdVersion() { fmt.Printf("am %s\n", version()) }
