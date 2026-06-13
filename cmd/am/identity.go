package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity resolution for the CLI, in priority order:
//  1. AGENTMAN_AGENT / AGENTMAN_SCOPE env (explicit overrides; win, e.g. for
//     parallel agents in one dir)
//  2. a per-working-directory file written by `am init` (survives across the
//     fresh shells the agent harness spawns, since env does not)
//
// This lets an agent run `am init <tasktype>` once at the start of a session and
// then use `am` normally — no need to repeat its identity on every command.
//
// File format: a scoped init writes JSON {"agent":"...","scope":"cat[/proj]"};
// an unscoped init keeps the legacy bare-id plain text, and any pre-Phase-Q
// plain-text file is read as an unscoped identity (R8 compatibility).

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

// identityRecord is the JSON identity-file shape used when a scope or token is
// set. A token (Phase S) is the bearer credential the server binds to a scope;
// it is stored here so an agent that ran `am token new` keeps sending it on
// every command without re-supplying it.
type identityRecord struct {
	Agent string `json:"agent"`
	Scope string `json:"scope,omitempty"`
	Token string `json:"token,omitempty"`
}

// resolveIdentity returns the agent id, scope, and token ("" when unset), each
// independently overridable by env (AGENTMAN_AGENT / AGENTMAN_SCOPE /
// AGENTMAN_TOKEN).
func resolveIdentity() (agent, scope, token string) {
	if b, err := os.ReadFile(identityFile()); err == nil {
		var rec identityRecord
		if json.Unmarshal(b, &rec) == nil && rec.Agent != "" {
			agent, scope, token = rec.Agent, rec.Scope, rec.Token
		} else {
			agent = strings.TrimSpace(string(b)) // legacy plain text = unscoped
		}
	}
	if a := strings.TrimSpace(os.Getenv("AGENTMAN_AGENT")); a != "" {
		agent = a
	}
	if s := strings.TrimSpace(os.Getenv("AGENTMAN_SCOPE")); s != "" {
		scope = s
	}
	if t := strings.TrimSpace(os.Getenv("AGENTMAN_TOKEN")); t != "" {
		token = t
	}
	return agent, scope, token
}

func resolveAgent() string {
	a, _, _ := resolveIdentity()
	return a
}

func resolveScope() string {
	_, s, _ := resolveIdentity()
	return s
}

func resolveToken() string {
	_, _, t := resolveIdentity()
	return t
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

// cmdInit generates an identity for this working directory and persists it,
// optionally scoped: `am init <tasktype> [-c CAT [-p PROJ]]`. Scope is
// validated locally (parseScope) — no server round-trip; an unknown slug just
// yields an empty world plus 403s on mutations.
func cmdInit(a Args) {
	cat, proj := a.flag("category"), a.flag("project")
	if proj != "" && cat == "" {
		fail(5, "init: -p requires -c (scope is category[/project])")
	}
	id := newIdentity(a.at(0))
	data := []byte(id) // unscoped: legacy bare id
	if cat != "" {
		raw := cat
		if proj != "" {
			raw += "/" + proj
		}
		sc, err := parseScope(raw)
		if err != nil {
			fail(5, "init: bad scope %q (want category[/project], no spaces)", raw)
		}
		data, err = json.Marshal(identityRecord{Agent: id, Scope: sc.String()})
		if err != nil {
			fail(1, "init: %v", err)
		}
	}
	f := identityFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		fail(1, "init: %v", err)
	}
	if err := os.WriteFile(f, data, 0o644); err != nil {
		fail(1, "init: %v", err)
	}
	fmt.Println(id) // print only the id, so `id=$(am init bugfix)` works
}

func cmdWhoami() {
	a, scope, token := resolveIdentity()
	if a == "" {
		fail(5, "no identity yet — run: am init <tasktype>   (e.g. am init bugfix)")
	}
	fmt.Println(a) // id stays line 1, so `am whoami | head -1` keeps working
	if scope != "" {
		fmt.Println("scope: " + scope)
	}
	if token != "" {
		// Never print the token value — its presence is all the human needs.
		fmt.Println("token: set")
	}
}
