package main

import (
	"strings"
	"testing"
)

// TestAskHeadlessFlagIsNoOp is a rule test: every consult is headless since
// #303, so `relay ask --headless` is accepted and ignored, and the note says
// so. It is a pure-function test, so it never runs a subcommand that reaches
// harness (CI has none).
func TestAskHeadlessFlagIsNoOp(t *testing.T) {
	if got := askHeadlessNoOpLines(false); got != nil {
		t.Errorf("askHeadlessNoOpLines(false) = %v, want nil", got)
	}
	got := askHeadlessNoOpLines(true)
	if len(got) != 1 || got[0] != askHeadlessFlagNote {
		t.Fatalf("askHeadlessNoOpLines(true) = %v, want [%q]", got, askHeadlessFlagNote)
	}
	if !strings.Contains(askHeadlessFlagNote, "--headless is the default and only consult mode") {
		t.Errorf("note = %q", askHeadlessFlagNote)
	}
}
