package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInitWritesAndResolves(t *testing.T) {
	// Redirect the identity file to a temp location.
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "") // ensure env override is not active

	out := captureStdout(t, func() {
		cmdInit(parse([]string{"bugfix"}))
	})
	id := strings.TrimSpace(out)

	// File must exist.
	if _, err := os.Stat(idFile); err != nil {
		t.Fatalf("identity file not written: %v", err)
	}

	// File contents must match the expected pattern.
	data, err := os.ReadFile(idFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	contents := strings.TrimSpace(string(data))
	matched, err := regexp.MatchString(`^bugfix_\d{6}_\d{4}$`, contents)
	if err != nil || !matched {
		t.Fatalf("identity file contents %q does not match ^bugfix_\\d{6}_\\d{4}$", contents)
	}

	// The printed id must match the file.
	if id != contents {
		t.Fatalf("printed id %q != file contents %q", id, contents)
	}

	// resolveAgent must return the same id.
	if got := resolveAgent(); got != contents {
		t.Fatalf("resolveAgent() = %q, want %q", got, contents)
	}
}

func TestAgentEnvOverrides(t *testing.T) {
	t.Setenv("AGENTMAN_AGENT", "override-1")
	if got := resolveAgent(); got != "override-1" {
		t.Fatalf("resolveAgent() = %q, want override-1", got)
	}
}

func TestSanitizeType(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Bug Fix!", "bug-fix"},
		{"", ""},
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"a--b", "a--b"},     // consecutive dashes in middle are kept (no collapse)
		{"--start", "start"}, // leading dashes trimmed
		{"end--", "end"},     // trailing dashes trimmed
		{"123abc", "123abc"}, // digits allowed
	}

	// A long input (>20 chars) must be truncated.
	long := "abcdefghijklmnopqrstuvwxyz" // 26 chars
	got := sanitizeType(long)
	if len([]rune(got)) > 20 {
		t.Errorf("sanitizeType(long) len=%d > 20", len(got))
	}

	for _, c := range cases {
		out := sanitizeType(c.input)
		if out != c.want {
			t.Errorf("sanitizeType(%q) = %q, want %q", c.input, out, c.want)
		}
	}
}

func TestNewIdentityFormat(t *testing.T) {
	id := newIdentity("deploy")
	matched, err := regexp.MatchString(`^deploy_\d{6}_\d{4}$`, id)
	if err != nil || !matched {
		t.Fatalf("newIdentity(deploy) = %q, want ^deploy_\\d{6}_\\d{4}$", id)
	}

	// Empty type falls back to "agent" prefix.
	id2 := newIdentity("")
	if !strings.HasPrefix(id2, "agent_") {
		t.Fatalf("newIdentity(\"\") = %q, want agent_ prefix", id2)
	}
	matched2, err := regexp.MatchString(`^agent_\d{6}_\d{4}$`, id2)
	if err != nil || !matched2 {
		t.Fatalf("newIdentity(\"\") = %q, want ^agent_\\d{6}_\\d{4}$", id2)
	}
}

// ---------- Phase Q: scoped identity ----------

func TestInitScopedWritesJSON(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "")
	t.Setenv("AGENTMAN_SCOPE", "")

	out := captureStdout(t, func() {
		cmdInit(parse([]string{"bugfix", "-c", "personal"}))
	})
	id := strings.TrimSpace(out)
	// Stdout stays just the id, so `id=$(am init bugfix -c personal)` works.
	matched, err := regexp.MatchString(`^bugfix_\d{6}_\d{4}$`, id)
	if err != nil || !matched {
		t.Fatalf("printed id %q does not match ^bugfix_\\d{6}_\\d{4}$", id)
	}

	data, err := os.ReadFile(idFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var rec identityRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("identity file is not JSON: %v\n%s", err, data)
	}
	if rec.Agent != id || rec.Scope != "personal" {
		t.Fatalf("identity record = %+v, want agent %s scope personal", rec, id)
	}
	if got := resolveAgent(); got != id {
		t.Fatalf("resolveAgent() = %q, want %q", got, id)
	}
	if got := resolveScope(); got != "personal" {
		t.Fatalf("resolveScope() = %q, want personal", got)
	}
}

func TestInitScopedCategoryProject(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "")
	t.Setenv("AGENTMAN_SCOPE", "")

	captureStdout(t, func() {
		cmdInit(parse([]string{"bugfix", "-c", "Work", "-p", "API"}))
	})
	// parseScope lowercases like slugs.
	if got := resolveScope(); got != "work/api" {
		t.Fatalf("resolveScope() = %q, want work/api", got)
	}
}

func TestInitProjectRequiresCategory(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)

	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdInit(parse([]string{"bugfix", "-p", "api"}))
		})
	})
	if code != 5 {
		t.Fatalf("init -p without -c exit = %d, want 5", code)
	}
	if !strings.Contains(msg, "-p requires -c") {
		t.Fatalf("stderr = %q, want '-p requires -c' hint", msg)
	}
	if _, err := os.Stat(idFile); err == nil {
		t.Fatal("identity file written despite usage error")
	}

	// A malformed scope is rejected before writing, too.
	code = captureExit(t, func() {
		captureStderr(t, func() {
			cmdInit(parse([]string{"bugfix", "-c", "has space"}))
		})
	})
	if code != 5 {
		t.Fatalf("init with bad category exit = %d, want 5", code)
	}
}

func TestLegacyPlainIdentityUnscoped(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "")
	t.Setenv("AGENTMAN_SCOPE", "")

	// Pre-Phase-Q file: bare id, trailing newline.
	if err := os.WriteFile(idFile, []byte("bugfix_010101_0001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgent(); got != "bugfix_010101_0001" {
		t.Fatalf("resolveAgent() = %q, want bugfix_010101_0001", got)
	}
	if got := resolveScope(); got != "" {
		t.Fatalf("resolveScope() = %q, want empty (legacy file is unscoped)", got)
	}
}

func TestScopeEnvOverride(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "")
	t.Setenv("AGENTMAN_SCOPE", "")

	captureStdout(t, func() {
		cmdInit(parse([]string{"bugfix", "-c", "personal"}))
	})
	t.Setenv("AGENTMAN_SCOPE", "work/api")
	if got := resolveScope(); got != "work/api" {
		t.Fatalf("resolveScope() with env override = %q, want work/api", got)
	}
	// The env also scopes an otherwise-unscoped (legacy) identity.
	if err := os.WriteFile(idFile, []byte("plain_010101_0001"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveScope(); got != "work/api" {
		t.Fatalf("resolveScope() legacy+env = %q, want work/api", got)
	}
}

func TestWhoamiPrintsScope(t *testing.T) {
	idFile := filepath.Join(t.TempDir(), "id")
	t.Setenv("AGENTMAN_AGENT_FILE", idFile)
	t.Setenv("AGENTMAN_AGENT", "")
	t.Setenv("AGENTMAN_SCOPE", "")

	var id string
	captureStdout(t, func() {
		cmdInit(parse([]string{"bugfix", "-c", "work", "-p", "api"}))
	})
	id = resolveAgent()

	out := captureStdout(t, func() { cmdWhoami() })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != id || lines[1] != "scope: work/api" {
		t.Fatalf("whoami output = %q, want id line then 'scope: work/api'", out)
	}

	// Unscoped identity: id only, no scope line (output shape unchanged).
	if err := os.WriteFile(idFile, []byte("plain_010101_0001"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { cmdWhoami() })
	if strings.TrimSpace(out) != "plain_010101_0001" {
		t.Fatalf("unscoped whoami output = %q, want bare id", out)
	}
}

func TestParseScope(t *testing.T) {
	cases := []struct {
		in      string
		want    Scope
		wantErr bool
	}{
		{"work", Scope{Category: "work"}, false},
		{"work/api", Scope{Category: "work", Project: "api"}, false},
		{"  Work/API  ", Scope{Category: "work", Project: "api"}, false}, // trim + lowercase
		{"", Scope{}, true},
		{"   ", Scope{}, true},
		{"/", Scope{}, true},
		{"work/", Scope{}, true},
		{"/api", Scope{}, true},
		{"a/b/c", Scope{}, true},
		{"has space", Scope{}, true},
		{"work/has space", Scope{}, true},
	}
	for _, c := range cases {
		got, err := parseScope(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseScope(%q) = %+v, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseScope(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	// String round-trips both forms.
	if (Scope{Category: "work"}).String() != "work" {
		t.Error("Scope{work}.String() != work")
	}
	if (Scope{Category: "work", Project: "api"}).String() != "work/api" {
		t.Error("Scope{work,api}.String() != work/api")
	}
	if !(Scope{}).IsZero() || (Scope{Category: "work"}).IsZero() {
		t.Error("IsZero misclassifies")
	}
}
