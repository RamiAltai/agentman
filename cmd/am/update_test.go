package main

import "testing"

func TestEscapeModule(t *testing.T) {
	cases := []struct{ in, want string }{
		{"github.com/RamiAltai/agentman", "github.com/!rami!altai/agentman"},
		{"github.com/BurntSushi/TOML", "github.com/!burnt!sushi/!t!o!m!l"}, // consecutive capitals
		{"golang.org/x/net", "golang.org/x/net"},                           // no capitals
	}
	for _, c := range cases {
		if got := escapeModule(c.in); got != c.want {
			t.Errorf("escapeModule(%q) = %q, want %q", c.in, got, c.want)
		}
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
		{"v0.2.0", "v0.0.0-20260605203447-0327a4ce5320", true},              // higher release triple
		{"v0.0.0", "v0.0.0-20260605203447-0327a4ce5320", true},              // same triple: tag beats pseudo
		{"v0.0.0-20260605203447-0327a4ce5320", "v0.0.0", false},             // same triple: pseudo never beats tag
		{"v0.0.0-20260606000000-aaaa", "v0.0.0-20260605203447-bbbb", true},  // newer pseudo
		{"v0.0.0-20260604000000-aaaa", "v0.0.0-20260605203447-bbbb", false}, // older pseudo
		{"", "v0.1.0", false},
		{"v0.2.0", "devel", false},
		{"v0.5.0", "v0.5.0-rc1", true},       // stable beats prerelease
		{"v0.5.0-rc1", "v0.5.0", false},      // prerelease does not beat stable
		{"v0.5.0-rc2", "v0.5.0-rc1", true},   // newer prerelease
		{"v0.5.0-rc1", "v0.5.0-rc1", false},  // same prerelease
		{"v0.5.0-rc10", "v0.5.0-rc2", false}, // prereleases order lexically (documented): rc10 < rc2
		{"garbage", "v0.1.0", false},         // unparsable reads as 0.0.0
		{"v0.1.0", "garbage", true},          // ...on either side
		{"v1", "v0.9.9", true},               // short version: missing parts are 0
	}
	for _, c := range cases {
		if got := updateAvailable(c.latest, c.cur); got != c.want {
			t.Errorf("updateAvailable(%q, %q) = %v, want %v", c.latest, c.cur, got, c.want)
		}
	}
}
