package relay

import "testing"

// These pin the exact bytes the CLI prints today (cmd/relay/main.go before
// this change), because the picker's result screen and the terminal must
// never say different things (spec §5).

func TestDoneText(t *testing.T) {
	got := DoneText("webshop")
	want := "webshop marked done; relaying stopped (relay gc archives it when you are finished with it)"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestAnswerText(t *testing.T) {
	if got, want := AnswerText("webshop"), "answered webshop's builder"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUnbindText(t *testing.T) {
	cases := []struct {
		name string
		res  UnbindResult
		want string
	}{
		{"deleted, no worktree", UnbindResult{},
			"unbound webshop (panes left untouched)"},
		{"archived", UnbindResult{ArchivedTo: "/a/webshop.tar.gz"},
			"archived webshop to /a/webshop.tar.gz (panes left untouched)"},
		{"worktree removed", UnbindResult{WorktreeRemoved: "/w/webshop"},
			"unbound webshop (panes left untouched)\nremoved worktree /w/webshop"},
		{"worktree kept", UnbindResult{WorktreeKept: "/w/webshop", KeptReason: "uncommitted changes"},
			"unbound webshop (panes left untouched)\nkept worktree /w/webshop (uncommitted changes)\n  remove by hand: git -C /w/webshop worktree remove /w/webshop"},
		{"worktree gone", UnbindResult{WorktreeGone: "/w/webshop"},
			"unbound webshop (panes left untouched)\nworktree /w/webshop was already gone"},
	}
	for _, c := range cases {
		if got := UnbindText("webshop", c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestUnbindTextProcessLines(t *testing.T) {
	got := UnbindText("x", UnbindResult{ProcessStopped: 4242})
	if got != "unbound x (panes left untouched)\nstopped builder process 4242" {
		t.Errorf("stopped: %q", got)
	}
	got = UnbindText("x", UnbindResult{ArchivedTo: "/a/x.tgz", ProcessErr: "pid 4242: SIGTERM: operation not permitted"})
	if got != "archived x to /a/x.tgz (panes left untouched)\ncould not stop builder process (pid 4242: SIGTERM: operation not permitted); check for it yourself" {
		t.Errorf("failed: %q", got)
	}
	// No process, no line: existing output is unchanged.
	if got := UnbindText("x", UnbindResult{}); got != "unbound x (panes left untouched)" {
		t.Errorf("plain: %q", got)
	}
}
