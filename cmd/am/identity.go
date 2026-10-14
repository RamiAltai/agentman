package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity resolution for the CLI, in priority order (each of agent, scope
// and token resolves independently):
//  1. AGENTMAN_AGENT / AGENTMAN_SCOPE / AGENTMAN_TOKEN env (explicit
//     overrides; win, e.g. for parallel agents in one dir — an env token
//     silently supersedes the file's)
//  2. a per-working-directory file written by `am init` (survives across the
//     fresh shells the agent harness spawns, since env does not)
//
// This lets an agent run `am init <tasktype>` once at the start of a session and
// then use `am` normally — no need to repeat its identity on every command.
//
// File format: JSON {"agent","scope","token"} (scope and token omitted when
// empty). A scoped init writes agent+scope; `am token new` merges in token
// (and writes {"token":"..."} alone, with no agent, before any init). An
// unscoped init writes the bare agent id as plain text, and any plain-text
// file — including those from versions before scopes existed — is read as an
// unscoped identity.

// identityFile returns this working directory's identity file. Without a home
// directory or a working directory it errors rather than guess: a relative
// "./.agentman" would put a bearer token inside the project tree, and a shared
// fallback name would merge distinct directories' identities.
func identityFile() (string, error) {
	if f := os.Getenv("AGENTMAN_AGENT_FILE"); f != "" {
		return f, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory (%v); set AGENTMAN_AGENT_FILE or AGENTMAN_AGENT", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("no working directory (%v); set AGENTMAN_AGENT_FILE or AGENTMAN_AGENT", err)
	}
	sum := sha1.Sum([]byte(wd))
	return filepath.Join(home, ".agentman", "agents", hex.EncodeToString(sum[:])[:12]), nil
}

// identityRecord is the JSON identity-file shape, written by a scoped init
// and by storeToken (which may write it with an empty agent). The token is
// the bearer credential the server binds to a scope; it is stored here so an
// agent that ran `am token new` keeps sending it on every command without
// re-supplying it.
type identityRecord struct {
	Agent string `json:"agent"`
	Scope string `json:"scope,omitempty"`
	Token string `json:"token,omitempty"`
}

// readIdentityFile returns the identity file's own agent, scope and token,
// without env overrides. A JSON record is accepted when any field is set (a
// token minted before `am init` has no agent); anything else is the legacy
// plain-text bare agent id. No locatable file reads as no identity.
func readIdentityFile() (rec identityRecord) {
	f, err := identityFile()
	if err != nil {
		return rec
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return rec
	}
	if json.Unmarshal(b, &rec) == nil && rec != (identityRecord{}) {
		return rec
	}
	return identityRecord{Agent: strings.TrimSpace(string(b))} // legacy plain text = unscoped
}

// writeIdentityFile replaces the identity file atomically (temp file in the
// same directory, then rename), so an interrupted write never leaves a
// truncated identity. The temp file is created 0600: the record may hold a
// bearer token.
func writeIdentityFile(data []byte) error {
	f, err := identityFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ".identity-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}

// resolveIdentity returns the agent id, scope, and token ("" when unset), each
// independently overridable by env (AGENTMAN_AGENT / AGENTMAN_SCOPE /
// AGENTMAN_TOKEN).
func resolveIdentity() (agent, scope, token string) {
	rec := readIdentityFile()
	agent, scope, token = rec.Agent, rec.Scope, rec.Token
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

// newIdentity builds a human-readable id like "bugfix_050626_4f21a9". The
// suffix is 24 random bits from crypto/rand: the server treats a claim by the
// same agent string as idempotent, so two agents that drew the same id would
// silently share a task (a 4-digit suffix collided at ~n²/20000 per day).
func newIdentity(taskType string) string {
	t := sanitizeType(taskType)
	if t == "" {
		t = "agent"
	}
	var b [3]byte
	rand.Read(b[:]) // never fails (crypto/rand panics rather than return short)
	return fmt.Sprintf("%s_%s_%s", t, time.Now().Format("020106"), hex.EncodeToString(b[:]))
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
	if err := writeIdentityFile(data); err != nil {
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
