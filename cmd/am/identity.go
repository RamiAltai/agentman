package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity resolution for the CLI, in priority order:
//  1. AGENTMAN_AGENT env (explicit override; wins, e.g. for parallel agents in one dir)
//  2. a per-working-directory file written by `am init` (survives across the
//     fresh shells the agent harness spawns, since env does not)
//
// This lets an agent run `am init <tasktype>` once at the start of a session and
// then use `am` normally — no need to repeat its identity on every command.

func identityFile() string {
	if f := os.Getenv("AGENTMAN_AGENT_FILE"); f != "" {
		return f
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "default"
	}
	sum := sha1.Sum([]byte(wd))
	return filepath.Join(home, ".agentman", "agents", hex.EncodeToString(sum[:])[:12])
}

func resolveAgent() string {
	if a := strings.TrimSpace(os.Getenv("AGENTMAN_AGENT")); a != "" {
		return a
	}
	if b, err := os.ReadFile(identityFile()); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// newIdentity builds a human-readable id like "bugfix_050626_4821".
func newIdentity(taskType string) string {
	t := sanitizeType(taskType)
	if t == "" {
		t = "agent"
	}
	return fmt.Sprintf("%s_%s_%04d", t, time.Now().Format("020106"), rand.Intn(10000))
}

func sanitizeType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if (r == ' ' || r == '-' || r == '_') && b.Len() > 0 {
			b.WriteByte('-')
		}
		if b.Len() >= 20 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// cmdInit generates an identity for this working directory and persists it.
func cmdInit(a Args) {
	id := newIdentity(a.at(0))
	f := identityFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		fail(1, "init: %v", err)
	}
	if err := os.WriteFile(f, []byte(id), 0o644); err != nil {
		fail(1, "init: %v", err)
	}
	fmt.Println(id) // print only the id, so `id=$(am init bugfix)` works
}

func cmdWhoami() {
	a := resolveAgent()
	if a == "" {
		fail(5, "no identity yet — run: am init <tasktype>   (e.g. am init bugfix)")
	}
	fmt.Println(a)
}
