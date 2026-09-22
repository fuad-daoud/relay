package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// landExecCall is one recorded LandOptions.Exec invocation.
type landExecCall struct {
	Dir, LogPath string
	Argv         []string
}

// landExecStub scripts LandOptions.Exec: one code and output per call, the
// last repeating, and records every argv with its dir and log path so a test
// can assert the gate's and gh's exact command lines.
type landExecStub struct {
	calls []landExecCall
	codes []int
	outs  []string
	errs  []error
}

func (s *landExecStub) run(_ context.Context, dir, logPath string, argv ...string) (int, string, error) {
	s.calls = append(s.calls, landExecCall{Dir: dir, LogPath: logPath, Argv: argv})
	if len(s.codes) == 0 {
		return 0, "", nil
	}
	i := len(s.calls) - 1
	if i >= len(s.codes) {
		i = len(s.codes) - 1
	}
	out := ""
	if i < len(s.outs) {
		out = s.outs[i]
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.codes[i], out, err
}

// noGh and hasGh are the two LookPath stubs land's PR branch is decided by.
func noGh(string) (string, error) {
	return "", errors.New("executable file not found in $PATH")
}

func hasGh(string) (string, error) { return "/usr/bin/gh", nil }

// landBinding saves a landable binding -- worktree, branch, base ref, gate,
// round closed -- and returns it. mutate adjusts the case under test.
func landBinding(t *testing.T, rt Runtime, mutate func(*store.Binding)) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:     "webshop",
		CWD:      "/repo",
		Worktree: "/wt/webshop",
		Branch:   "relay/x",
		BaseRef:  "main",
		Gate:     "make check",
		State:    store.StateActive,
		Round:    2,
	}
	if mutate != nil {
		mutate(&b)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestLandRebasesGatesPushesLogs is the whole land sequence in one place:
// fetch, rebase onto origin/<base>, the gate on the rebased tree, the remote
// branch check, the push, and the log entry -- with the PR only printed,
// since no gh is on PATH.
// TestLandDirtyRefusesBeforeAnyGit: a dirty worktree is refused before a
// single git command runs, and before the gate.
// TestLandConflictAbortsExit3: a conflicting rebase names every path, stops
// before the gate and the push, and writes nothing.
// TestLandGateFailNothingPushed is the ordering rule: the gate runs on the
// rebased tree, so a failing gate leaves origin untouched.
// TestLandPRWithGh: with gh on PATH and --pr, the PR is created and its URL
// recorded; the gate still ran first.
// TestLandForceWithLeaseOnReland: a branch already on origin was rewritten by
// the rebase, so its push needs the lease; --merge does not rewrite it and
// does not.
// noExec is a seam that would fail if Land ever reached it: the sub-tests
// here assert on git calls only.
func noExec(context.Context, string, string, ...string) (int, string, error) {
	return 0, "", errors.New("unexpected exec")
}

// noGate is the landBinding mutation for the sub-tests that assert on git
// calls only: a binding with no gate never reaches the exec seam.
func noGate(b *store.Binding) { b.Gate = "" }

// TestLandRefusals covers every state land refuses outright, and the two
// escapes that turn a refusal into a land: --onto for a binding that recorded
// no base branch, and --force for a binding with a round still open.
// TestLandNoGateSaysSo pins the two no-gate words: a binding with no gate
// reports "none", and --no-gate reports "skipped", so a skipped gate never
// reads as a passing one.
// TestLandTextIsHonestAboutWhatItDid: the printed line never calls a merge a
// rebase, and always says how the gate went.
func TestLandTextIsHonestAboutWhatItDid(t *testing.T) {
	rebased := LandResult{Branch: "relay/x", Base: "main", Rebased: true, GateResult: "pass", Pushed: true, PRCommand: "gh pr create"}
	if got, want := LandText(rebased), "landed relay/x -> main (rebased; gate pass; pushed)\n  open the PR: gh pr create"; got != want {
		t.Errorf("LandText = %q, want %q", got, want)
	}

	merged := LandResult{Branch: "relay/x", Base: "main", GateResult: "skipped", Pushed: true, PRURL: "https://github.com/o/r/pull/7"}
	if got, want := LandText(merged), "landed relay/x -> main (merged; gate skipped; pushed)\n  pr: https://github.com/o/r/pull/7"; got != want {
		t.Errorf("LandText = %q, want %q", got, want)
	}
}

// TestRealExecCapturesAndLogs pins the default seam's contract: the combined
// output comes back, a non-zero exit is a code rather than an error, and a
// named log path receives the same bytes.
func TestRealExecCapturesAndLogs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "land-gate.log")

	code, out, err := realExec(context.Background(), dir, logPath, "sh", "-c", "echo hello; echo oops >&2; exit 3")
	if err != nil {
		t.Fatalf("realExec: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "oops") {
		t.Errorf("combined output = %q, want both streams", out)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(logged) != out {
		t.Errorf("log = %q, want the captured output %q", string(logged), out)
	}
}
