package main

import "testing"

func TestEscapeModule(t *testing.T) {
	got := escapeModule("github.com/RamiAltai/agentman")
	want := "github.com/!rami!altai/agentman"
	if got != want {
		t.Fatalf("escapeModule = %q, want %q", got, want)
	}
}

func TestPseudoStamp(t *testing.T) {
	if s := pseudoStamp("v0.0.0-20260605203447-0327a4ce5320"); s != "20260605203447" {
		t.Fatalf("pseudoStamp pseudo = %q", s)
	}
	if s := pseudoStamp("v0.2.0"); s != "" {
		t.Fatalf("pseudoStamp tag = %q, want empty", s)
	}
}

func TestUpdateAvailable(t *testing.T) {
	cases := []struct {
		latest, cur string
		want        bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.2.0", "v0.2.1", false},
		{"v0.2.0", "v0.0.0-20260605203447-0327a4ce5320", true},       // tag beats pseudo
		{"v0.0.0-20260606000000-aaaa", "v0.0.0-20260605203447-bbbb", true},  // newer pseudo
		{"v0.0.0-20260604000000-aaaa", "v0.0.0-20260605203447-bbbb", false}, // older pseudo
		{"", "v0.1.0", false},
		{"v0.2.0", "devel", false},
	}
	for _, c := range cases {
		if got := updateAvailable(c.latest, c.cur); got != c.want {
			t.Errorf("updateAvailable(%q, %q) = %v, want %v", c.latest, c.cur, got, c.want)
		}
	}
}
