package main

import (
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
