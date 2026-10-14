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
// module version stamped by the toolchain, else "devel". The stamped version
// is the real tag for `go install …@vX.Y.Z`; for a `go build` inside a git
// checkout (Go 1.24+) it is a pseudo-version, "+dirty" when the tree has
// uncommitted changes. "devel" appears only for builds with no VCS info
// (-buildvcs=false, outside a repository). See localBuild.
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

// localBuild reports a binary built from a source checkout rather than
// installed from the module proxy: only checkout builds carry vcs.* build
// settings. Its stamped version describes the checkout, not a release, so
// comparing it with the latest published version is meaningless.
func localBuild() bool {
	if injectedVersion != "" {
		return false
	}
	bi, ok := debug.ReadBuildInfo()
	return ok && hasVCSInfo(bi)
}

func hasVCSInfo(bi *debug.BuildInfo) bool {
	for _, s := range bi.Settings {
		if s.Key == "vcs" {
			return true
		}
	}
	return false
}

func cmdVersion() { fmt.Printf("am %s\n", version()) }
