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

	bin := installedBinary(goBin)
	if v := installedVersion(bin); v != "" {
		fmt.Println("agentman: updated " + bin + " — now " + v + ".")
		// go install writes to GOBIN/GOPATH/bin; an `am` run from anywhere
		// else (a copied binary, /usr/local/bin) is left untouched — say so.
		if exe, err := os.Executable(); err == nil && !sameFile(bin, exe) {
			fmt.Printf("agentman: warning — this `am` (%s) was NOT updated; put %s first on PATH or replace the old copy.\n", exe, filepath.Dir(bin))
		}
	} else {
		fmt.Println("agentman: updated.")
	}
	fmt.Println("Restart any running `am serve` to apply (and hard-refresh the dashboard).")
}

// installedBinary is where `go install` puts am: GOBIN, else GOPATH/bin
// ("" when neither resolves).
func installedBinary(goBin string) string {
	dir := goEnv(goBin, "GOBIN")
	if dir == "" {
		if gp := goEnv(goBin, "GOPATH"); gp != "" {
			dir = filepath.Join(gp, "bin")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "am")
}

// installedVersion runs the freshly-installed binary to report its version.
func installedVersion(bin string) string {
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "am ")
}

// sameFile reports whether two paths name the same file (symlinks followed).
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
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
	if updateCheckWanted() {
		go reportUpdate(proxyURL, version())
	}
}

// proxyURL is the module proxy checkForUpdate queries.
const proxyURL = "https://proxy.golang.org"

// updateCheckWanted is false when the user opted out or the binary is a local
// build — nothing meaningful to compare against.
func updateCheckWanted() bool {
	if os.Getenv("AGENTMAN_NO_UPDATE_CHECK") != "" {
		return false
	}
	return version() != "devel" && !localBuild()
}

// reportUpdate asks the proxy at base for the latest version and logs when it
// is newer than cur.
func reportUpdate(base, cur string) {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(base + "/" + escapeModule(modulePath) + "/@latest")
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
	if ls == "" && cs == "" {
		// Neither is a pseudo-version: compare prerelease suffixes per semver —
		// a stable tag beats any prerelease (v0.5.0 > v0.5.0-rc1), and prereleases
		// order lexically (rc1 < rc2), which is good enough for this scheme.
		lpre, cpre := preRelease(latest), preRelease(cur)
		if cpre != "" && lpre == "" {
			return true
		}
		if lpre != "" && cpre != "" {
			return lpre > cpre
		}
	}
	return false
}

// preRelease returns the suffix after the first '-' (e.g. "rc1" from
// "v0.5.0-rc1"), or "" for a plain release version.
func preRelease(v string) string {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[i+1:]
	}
	return ""
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

// pseudoStamp returns the 14-digit timestamp from a no-tag pseudo-version like
// v0.0.0-20260605203447-0327a4ce5320, or "" otherwise. It deliberately does
// not match post-tag pseudo-versions (v0.5.1-0.20260605203447-abc,
// v0.5.0-rc1.0.20260605203447-abc), where the timestamp follows a '.': those
// compare correctly in updateAvailable's prerelease branch because the
// timestamp leads the fixed-width suffix, while reading their stamp here
// would make a pre-release pseudo-version look older than its own base tag.
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
