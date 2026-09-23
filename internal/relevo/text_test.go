package relevo

import "testing"

// These pin the exact bytes the CLI prints today (cmd/relevo/main.go before
// this change), because the picker's result screen and the terminal must
// never say different things (spec §5).

func TestDoneText(t *testing.T) {
	base := "webshop marked done; relaying stopped (relevo gc archives it when you are finished with it)"
	cases := []struct {
		name string
		res  DoneResult
		want string
	}{
		{"zero result", DoneResult{},
			base},
		{"removed with branch", DoneResult{WorktreeRemoved: "/w", Branch: "relevo/x"},
			base + "\nremoved worktree /w (branch relevo/x is free to check out)"},
		{"removed without branch", DoneResult{WorktreeRemoved: "/w"},
			base + "\nremoved worktree /w"},
		{"kept", DoneResult{WorktreeKept: "/w", KeptReason: "uncommitted changes"},
			base + "\nkept worktree /w (uncommitted changes); relevo gc retries when it is clean"},
		{"gone", DoneResult{WorktreeGone: "/w"},
			base + "\nworktree /w was already gone"},
	}
	for _, c := range cases {
		if got := DoneText("webshop", c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestUnbindText(t *testing.T) {
	cases := []struct {
		name string
		res  UnbindResult
		want string
	}{
		{"deleted, no worktree", UnbindResult{},
			"unbound webshop"},
		{"archived", UnbindResult{ArchivedTo: "/a/webshop.tar.gz"},
			"archived webshop to /a/webshop.tar.gz"},
		{"worktree removed", UnbindResult{WorktreeRemoved: "/w/webshop"},
			"unbound webshop\nremoved worktree /w/webshop"},
		{"worktree kept", UnbindResult{WorktreeKept: "/w/webshop", KeptReason: "uncommitted changes"},
			"unbound webshop\nkept worktree /w/webshop (uncommitted changes)\n  remove by hand: git -C /w/webshop worktree remove /w/webshop"},
		{"worktree gone", UnbindResult{WorktreeGone: "/w/webshop"},
			"unbound webshop\nworktree /w/webshop was already gone"},
	}
	for _, c := range cases {
		if got := UnbindText("webshop", c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestUnbindTextProcessLines(t *testing.T) {
	got := UnbindText("x", UnbindResult{ProcessStopped: 4242})
	if got != "unbound x\nstopped builder process 4242" {
		t.Errorf("stopped: %q", got)
	}
	got = UnbindText("x", UnbindResult{ArchivedTo: "/a/x.tgz", ProcessErr: "pid 4242: SIGTERM: operation not permitted"})
	if got != "archived x to /a/x.tgz\ncould not stop builder process (pid 4242: SIGTERM: operation not permitted); check for it yourself" {
		t.Errorf("failed: %q", got)
	}
	// No process, no line: existing output is unchanged.
	if got := UnbindText("x", UnbindResult{}); got != "unbound x" {
		t.Errorf("plain: %q", got)
	}
}

func TestPauseText(t *testing.T) {
	cases := []struct {
		name string
		res  PauseResult
		want string
	}{
		{"between rounds", PauseResult{Round: 1, Branch: "relevo/x", Worktree: "/w"},
			"webshop paused after round 1; worktree /w released, branch relevo/x kept\n  resume: relevo bind --resume --name webshop"},
		{"committed", PauseResult{Round: 2, Branch: "relevo/x", Worktree: "/w", Committed: "abcdef1234567890"},
			"webshop paused after round 2; worktree /w released, branch relevo/x kept\n  committed abcdef123456 ([relevo] webshop: paused after round 2)\n  resume: relevo bind --resume --name webshop"},
	}
	for _, c := range cases {
		if got := PauseText("webshop", c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestRestoreText(t *testing.T) {
	cases := []struct {
		name string
		res  Resolution
		want string
	}{
		{"zero result", Resolution{},
			""},
		{"restored", Resolution{RestoredWorktree: "/w", RestoredBranch: "relevo/x"},
			"restored worktree /w on relevo/x"},
	}
	for _, c := range cases {
		if got := RestoreText(c.res); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
