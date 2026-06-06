package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	modulePath  = "github.com/RamiAltai/agentman"
	installPath = modulePath + "/cmd/am"
)

// cmdUpdate re-installs am via `go install …@<version>` (default @latest).
func cmdUpdate(a Args) {
	ref := a.at(0)
	if ref == "" {
		ref = "latest"
	}
	target := installPath + "@" + ref

	goBin, err := exec.LookPath("go")
	if err != nil {
		fail(1, "update: Go toolchain not found on PATH.\n"+
			"  Install Go (https://go.dev/dl) — or grab a prebuilt release binary.")
	}

	fmt.Printf("agentman: installing %s …\n", target)
	cmd := exec.Command(goBin, "install", target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		fail(1, "update failed: %v", err)
	}

	if v := installedVersion(goBin); v != "" {
		fmt.Println("agentman: updated — now " + v + ".")
	} else {
		fmt.Println("agentman: updated.")
	}
	fmt.Println("Restart any running `am serve` to apply (and hard-refresh the dashboard).")
}

// installedVersion runs the freshly-installed binary to report its version.
func installedVersion(goBin string) string {
	dir := goEnv(goBin, "GOBIN")
	if dir == "" {
		if gp := goEnv(goBin, "GOPATH"); gp != "" {
			dir = filepath.Join(gp, "bin")
		}
	}
	if dir == "" {
		return ""
	}
	out, err := exec.Command(filepath.Join(dir, "am"), "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "am ")
}

func goEnv(goBin, key string) string {
	out, err := exec.Command(goBin, "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkForUpdate logs (once, on `am serve` startup) if a newer published
// version exists. Non-blocking and silent on any error; opt out with
// AGENTMAN_NO_UPDATE_CHECK=1.
func checkForUpdate() {
	if os.Getenv("AGENTMAN_NO_UPDATE_CHECK") != "" {
		return
	}
	cur := version()
	if cur == "devel" {
		return // local build — nothing meaningful to compare against
	}
	go func() {
		client := &http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get("https://proxy.golang.org/" + escapeModule(modulePath) + "/@latest")
		if err != nil || resp == nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var info struct{ Version string }
		if json.NewDecoder(resp.Body).Decode(&info) != nil {
			return
		}
		if updateAvailable(info.Version, cur) {
			log.Printf("agentman: update available — %s (you have %s). Run `am update`.", info.Version, cur)
		}
	}()
}

// escapeModule encodes a module path for the Go module proxy: uppercase letters
// become "!" + lowercase (e.g. RamiAltai -> !rami!altai).
func escapeModule(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// updateAvailable reports whether `latest` is strictly newer than `cur`.
func updateAvailable(latest, cur string) bool {
	if latest == "" || latest == cur || cur == "devel" {
		return false
	}
	lp, cp := semverParts(latest), semverParts(cur)
	for i := 0; i < 3; i++ {
		if lp[i] != cp[i] {
			return lp[i] > cp[i]
		}
	}
	// Same release triple: a real tag beats a pseudo-version; otherwise compare
	// pseudo-version timestamps so newer untagged commits are still detected.
	ls, cs := pseudoStamp(latest), pseudoStamp(cur)
	if cs != "" && ls == "" {
		return true
	}
	if ls != "" && cs != "" {
		return ls > cs
	}
	return false
}

func semverParts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(s)
	}
	return out
}

// pseudoStamp returns the 14-digit timestamp from a pseudo-version like
// v0.0.0-20260605203447-0327a4ce5320, or "" if not a pseudo-version.
func pseudoStamp(v string) string {
	for _, part := range strings.Split(v, "-") {
		if len(part) == 14 && isDigits(part) {
			return part
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
