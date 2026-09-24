package relevo

import "testing"

// These pin the exact bytes the CLI prints today (cmd/relevo/main.go before
// this change), because the picker's result screen and the terminal must
// never say different things (spec §5).

func TestDoneText(t *testing.T) {
	base := "webshop marked done; relaying stopped (relevo unbind --done archives it when you are finished with it)"
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
			base + "\nkept worktree /w (uncommitted changes); relevo unbind --done retries when it is clean"},
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
		{"archived", UnbindResult{Archived: true},
			"archived webshop"},
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
	got = UnbindText("x", UnbindResult{Archived: true, ProcessErr: "pid 4242: SIGTERM: operation not permitted"})
	if got != "archived x\ncould not stop builder process (pid 4242: SIGTERM: operation not permitted); check for it yourself" {
		t.Errorf("failed: %q", got)
	}
	// No process, no line: existing output is unchanged.
	if got := UnbindText("x", UnbindResult{}); got != "unbound x" {
		t.Errorf("plain: %q", got)
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
