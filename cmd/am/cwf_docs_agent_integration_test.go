package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// cwfDocsCheatSheetLine matches a command line of the pasted CLAUDE.md block
// in docs/agent-integration.md: four-space indent, then `am <verb> [<sub>]`.
var cwfDocsCheatSheetLine = regexp.MustCompile(`(?m)^    am ([a-z]+)(?: ([a-z]+))?`)

// cwfDocsSubVerbs are the verbs whose second word is a subcommand.
var cwfDocsSubVerbs = map[string]bool{"dep": true, "project": true, "category": true, "token": true, "db": true}

// TestCwfDocsAgentCheatSheetVerbsInUsage keeps the agent cheat sheet a strict
// subset of usage(), the canonical command list: a verb removed or renamed in
// the CLI must not linger in the text agents paste into CLAUDE.md.
func TestCwfDocsAgentCheatSheetVerbsInUsage(t *testing.T) {
	doc, err := os.ReadFile("../../docs/agent-integration.md")
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, usage)
	matches := cwfDocsCheatSheetLine.FindAllStringSubmatch(string(doc), -1)
	if len(matches) < 10 {
		t.Fatalf("found only %d cheat-sheet lines; did the block's indentation change?", len(matches))
	}
	for _, m := range matches {
		cmd := "am " + m[1]
		if cwfDocsSubVerbs[m[1]] && m[2] != "" {
			cmd += " " + m[2]
		}
		if !strings.Contains(out, "  "+cmd+" ") {
			t.Errorf("docs/agent-integration.md lists %q, which usage() does not", cmd)
		}
	}
}
