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
func TestLandRebasesGatesPushesLogs(t *testing.T) {
	rt := newRuntime(t, &fakePanes{})
	fg := &fakeGit{}
	rt.Git = fg
	b := landBinding(t, rt, nil)
	ex := &landExecStub{codes: []int{0}, outs: []string{"all good\n"}}

	res, err := Land(context.Background(), rt, "webshop", LandOptions{Exec: ex.run, LookPath: noGh})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}

	// The calls ran in land's order, against the binding's worktree.
	if len(fg.fetchCalls) != 1 {
		t.Fatalf("fetchCalls = %+v, want 1", fg.fetchCalls)
	}
	if want := (fetchCall{Dir: b.Worktree, Remote: "origin", Ref: "main"}); fg.fetchCalls[0] != want {
		t.Errorf("Fetch = %+v, want %+v", fg.fetchCalls[0], want)
	}
	if len(fg.rebaseCalls) != 1 {
		t.Fatalf("rebaseCalls = %+v, want 1", fg.rebaseCalls)
	}
	if want := (rebaseCall{Dir: b.Worktree, Onto: "origin/main"}); fg.rebaseCalls[0] != want {
		t.Errorf("Rebase = %+v, want %+v", fg.rebaseCalls[0], want)
	}
	if len(ex.calls) != 1 {
		t.Fatalf("exec calls = %+v, want only the gate", ex.calls)
	}
	gate := ex.calls[0]
	if gate.Dir != b.Worktree {
		t.Errorf("gate dir = %q, want %q", gate.Dir, b.Worktree)
	}
	if got, want := strings.Join(gate.Argv, " "), "sh -c make check 2>&1"; got != want {
		t.Errorf("gate argv = %q, want %q", got, want)
	}
	if want := filepath.Join(rt.Store.Dir("webshop"), "land-gate.log"); gate.LogPath != want {
		t.Errorf("gate log path = %q, want %q", gate.LogPath, want)
	}
	if len(fg.remoteBranchExistsCalls) != 1 {
		t.Fatalf("remoteBranchExistsCalls = %+v, want 1", fg.remoteBranchExistsCalls)
	}
	if want := (remoteBranchExistsCall{Dir: b.Worktree, Remote: "origin", Branch: "relay/x"}); fg.remoteBranchExistsCalls[0] != want {
		t.Errorf("RemoteBranchExists = %+v, want %+v", fg.remoteBranchExistsCalls[0], want)
	}
	if len(fg.pushCalls) != 1 {
		t.Fatalf("pushCalls = %+v, want 1", fg.pushCalls)
	}
	if want := (pushCall{Dir: b.Worktree, Remote: "origin", Branch: "relay/x", ForceWithLease: false}); fg.pushCalls[0] != want {
		t.Errorf("Push = %+v, want %+v", fg.pushCalls[0], want)
	}

	if res.GateResult != "pass" {
		t.Errorf("GateResult = %q, want pass", res.GateResult)
	}
	if !res.Rebased || !res.Pushed {
		t.Errorf("Rebased/Pushed = %v/%v, want true/true", res.Rebased, res.Pushed)
	}
	if res.PRURL != "" || res.PRCommand != "gh pr create --head relay/x --base main --fill" {
		t.Errorf("PRURL/PRCommand = (%q, %q), want (\"\", the gh command)", res.PRURL, res.PRCommand)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindLand {
		t.Errorf("last log kind = %q, want %q", last.Kind, store.KindLand)
	}
	if last.Note != "landed relay/x -> main" {
		t.Errorf("land note = %q, want %q", last.Note, "landed relay/x -> main")
	}
	if !last.Confirmed {
		t.Error("the land entry must be Confirmed: it is not a payload for anyone")
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LandedAt.Equal(baseTime) {
		t.Errorf("LandedAt = %v, want %v", stored.LandedAt, baseTime)
	}
	if stored.LandedPR != "" {
		t.Errorf("LandedPR = %q, want \"\"", stored.LandedPR)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d rows, want 1", len(rep.Bindings))
	}
	if rep.Bindings[0].Landed != "landed" {
		t.Errorf("status Landed = %q, want %q", rep.Bindings[0].Landed, "landed")
	}
}

// TestLandDirtyRefusesBeforeAnyGit: a dirty worktree is refused before a
// single git command runs, and before the gate.
func TestLandDirtyRefusesBeforeAnyGit(t *testing.T) {
	rt := newRuntime(t, &fakePanes{})
	fg := &fakeGit{dirtyResult: true}
	rt.Git = fg
	b := landBinding(t, rt, nil)
	ex := &landExecStub{codes: []int{0}}

	_, err := Land(context.Background(), rt, "webshop", LandOptions{Exec: ex.run, LookPath: noGh})
	if !errors.Is(err, ErrLandDirty) {
		t.Fatalf("Land err = %v, want ErrLandDirty", err)
	}
	if !strings.Contains(err.Error(), b.Worktree) {
		t.Errorf("refusal must name the tree it refused: %v", err)
	}
	if len(fg.fetchCalls)+len(fg.rebaseCalls)+len(fg.mergeCalls)+len(fg.pushCalls)+len(ex.calls) != 0 {
		t.Errorf("nothing may run after a dirty refusal: fetch %+v, rebase %+v, merge %+v, push %+v, exec %+v",
			fg.fetchCalls, fg.rebaseCalls, fg.mergeCalls, fg.pushCalls, ex.calls)
	}

	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LandedAt.IsZero() {
		t.Error("a refused land must not stamp LandedAt")
	}
}

// TestLandConflictAbortsExit3: a conflicting rebase names every path, stops
// before the gate and the push, and writes nothing.
func TestLandConflictAbortsExit3(t *testing.T) {
	rt := newRuntime(t, &fakePanes{})
	fg := &fakeGit{rebaseConflicts: []string{"a.go", "b.go"}}
	rt.Git = fg
	b := landBinding(t, rt, nil)
	ex := &landExecStub{codes: []int{0}}

	_, err := Land(context.Background(), rt, "webshop", LandOptions{Exec: ex.run, LookPath: noGh})
	if !errors.Is(err, ErrLandConflict) {
		t.Fatalf("Land err = %v, want ErrLandConflict", err)
	}
	for _, want := range []string{"a.go", "b.go", b.Worktree} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error must name %q: %v", want, err)
		}
	}
	if len(fg.pushCalls) != 0 {
		t.Errorf("a conflict must push nothing, got %+v", fg.pushCalls)
	}
	if len(ex.calls) != 0 {
		t.Errorf("a conflict must not reach the gate, got %+v", ex.calls)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindLand {
			t.Errorf("a conflicted land must log nothing, got %+v", e)
		}
	}
}

// TestLandGateFailNothingPushed is the ordering rule: the gate runs on the
// rebased tree, so a failing gate leaves origin untouched.
func TestLandGateFailNothingPushed(t *testing.T) {
	rt := newRuntime(t, &fakePanes{})
	fg := &fakeGit{}
	rt.Git = fg
	landBinding(t, rt, nil)
	ex := &landExecStub{codes: []int{2}, outs: []string{"boom\n"}}

	_, err := Land(context.Background(), rt, "webshop", LandOptions{Exec: ex.run, LookPath: noGh})
	if !errors.Is(err, ErrLandGate) {
		t.Fatalf("Land err = %v, want ErrLandGate", err)
	}
	if !strings.Contains(err.Error(), "exit 2") {
		t.Errorf("the gate failure must name its exit code: %v", err)
	}
	if len(fg.pushCalls) != 0 {
		t.Errorf("nothing may be pushed on a failed gate, got %+v", fg.pushCalls)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed gate must leave the log unchanged, got %+v", entries)
	}
}

// TestLandPRWithGh: with gh on PATH and --pr, the PR is created and its URL
// recorded; the gate still ran first.
func TestLandPRWithGh(t *testing.T) {
	rt := newRuntime(t, &fakePanes{})
	fg := &fakeGit{}
	rt.Git = fg
	landBinding(t, rt, nil)
	const url = "https://github.com/o/r/pull/7"
	ex := &landExecStub{
		codes: []int{0, 0},
		outs:  []string{"all good\n", "warning: something\n" + url + "\n"},
	}

	res, err := Land(context.Background(), rt, "webshop", LandOptions{PR: true, Exec: ex.run, LookPath: hasGh})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if res.PRURL != url {
		t.Errorf("PRURL = %q, want %q", res.PRURL, url)
	}
	if res.PRCommand != "" {
		t.Errorf("PRCommand = %q, want \"\" when the PR was created", res.PRCommand)
	}
	if len(ex.calls) != 2 {
		t.Fatalf("exec calls = %+v, want the gate and gh", ex.calls)
	}
	gh := ex.calls[1]
	if got, want := strings.Join(gh.Argv, " "), "gh pr create --head relay/x --base main --fill"; got != want {
		t.Errorf("gh argv = %q, want %q", got, want)
	}
	if gh.Dir != "/wt/webshop" || gh.LogPath != "" {
		t.Errorf("gh must run in the worktree with no log file, got %+v", gh)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entries[len(entries)-1].Note, "landed relay/x -> main pr "+url; got != want {
		t.Errorf("land note = %q, want %q", got, want)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if stored.LandedPR != url {
		t.Errorf("LandedPR = %q, want %q", stored.LandedPR, url)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got, want := rep.Bindings[0].Landed, "landed pr "+url; got != want {
		t.Errorf("status Landed = %q, want %q", got, want)
	}
}

// TestLandForceWithLeaseOnReland: a branch already on origin was rewritten by
// the rebase, so its push needs the lease; --merge does not rewrite it and
// does not.
func TestLandForceWithLeaseOnReland(t *testing.T) {
	t.Run("a rebase relands with the lease", func(t *testing.T) {
		rt := newRuntime(t, &fakePanes{})
		fg := &fakeGit{remoteBranchExists: true}
		rt.Git = fg
		b := landBinding(t, rt, noGate)

		res, err := Land(context.Background(), rt, "webshop", LandOptions{Exec: noExec, LookPath: noGh})
		if err != nil {
			t.Fatalf("Land: %v", err)
		}
		if len(fg.pushCalls) != 1 {
			t.Fatalf("pushCalls = %+v, want 1", fg.pushCalls)
		}
		want := pushCall{Dir: b.Worktree, Remote: "origin", Branch: "relay/x", ForceWithLease: true}
		if fg.pushCalls[0] != want {
			t.Errorf("Push = %+v, want %+v", fg.pushCalls[0], want)
		}
		if !res.Rebased {
			t.Error("Rebased = false, want true")
		}
	})

	t.Run("--merge is not rewritten, so no lease", func(t *testing.T) {
		rt := newRuntime(t, &fakePanes{})
		fg := &fakeGit{remoteBranchExists: true}
		rt.Git = fg
		b := landBinding(t, rt, noGate)

		res, err := Land(context.Background(), rt, "webshop", LandOptions{Merge: true, Exec: noExec, LookPath: noGh})
		if err != nil {
			t.Fatalf("Land: %v", err)
		}
		if len(fg.rebaseCalls) != 0 {
			t.Errorf("--merge must not rebase, got %+v", fg.rebaseCalls)
		}
		if len(fg.mergeCalls) != 1 {
			t.Fatalf("mergeCalls = %+v, want 1", fg.mergeCalls)
		}
		if want := (mergeCall{Dir: b.Worktree, Ref: "origin/main"}); fg.mergeCalls[0] != want {
			t.Errorf("Merge = %+v, want %+v", fg.mergeCalls[0], want)
		}
		if len(fg.pushCalls) != 1 {
			t.Fatalf("pushCalls = %+v, want 1", fg.pushCalls)
		}
		want := pushCall{Dir: b.Worktree, Remote: "origin", Branch: "relay/x", ForceWithLease: false}
		if fg.pushCalls[0] != want {
			t.Errorf("Push = %+v, want %+v", fg.pushCalls[0], want)
		}
		if res.Rebased {
			t.Error("Rebased = true on a --merge land")
		}
	})
}

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
func TestLandRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*store.Binding)
		opts   LandOptions
		want   string
	}{
		{
			"no recorded base ref and no --onto",
			func(b *store.Binding) { b.BaseRef = "" },
			LandOptions{},
			"pass --onto",
		},
		{
			"open round without --force",
			func(b *store.Binding) { b.RoundStartedAt = baseTime },
			LandOptions{},
			"round is open",
		},
		{
			"a --cwd binding has no tree to land",
			func(b *store.Binding) { b.Worktree = "" },
			LandOptions{},
			"nothing to land",
		},
		{
			"paused",
			func(b *store.Binding) { b.State = store.StatePaused },
			LandOptions{},
			"paused",
		},
		{
			"done",
			func(b *store.Binding) { b.State = store.StateDone },
			LandOptions{},
			"done",
		},
		{
			"remote",
			func(b *store.Binding) { b.Builder.Mode = store.ModeRemote },
			LandOptions{},
			"remote bindings land on the server side",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := newRuntime(t, &fakePanes{})
			fg := &fakeGit{}
			rt.Git = fg
			landBinding(t, rt, c.mutate)

			_, err := Land(context.Background(), rt, "webshop", c.opts)
			if err == nil {
				t.Fatalf("Land must refuse %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to mention %q", err, c.want)
			}
			if len(fg.fetchCalls)+len(fg.rebaseCalls)+len(fg.mergeCalls)+len(fg.pushCalls) != 0 {
				t.Errorf("a refused land must run no git: %+v", fg)
			}
		})
	}

	t.Run("--onto unblocks a binding with no base ref", func(t *testing.T) {
		rt := newRuntime(t, &fakePanes{})
		fg := &fakeGit{}
		rt.Git = fg
		landBinding(t, rt, func(b *store.Binding) { b.BaseRef = ""; b.Gate = "" })

		res, err := Land(context.Background(), rt, "webshop", LandOptions{Onto: "trunk", Exec: noExec, LookPath: noGh})
		if err != nil {
			t.Fatalf("Land --onto: %v", err)
		}
		if res.Base != "trunk" {
			t.Errorf("Base = %q, want trunk", res.Base)
		}
		if want := (rebaseCall{Dir: "/wt/webshop", Onto: "origin/trunk"}); fg.rebaseCalls[0] != want {
			t.Errorf("Rebase = %+v, want %+v", fg.rebaseCalls[0], want)
		}
	})

	t.Run("--force unblocks an open round", func(t *testing.T) {
		rt := newRuntime(t, &fakePanes{})
		fg := &fakeGit{}
		rt.Git = fg
		landBinding(t, rt, func(b *store.Binding) { b.RoundStartedAt = baseTime; b.Gate = "" })

		if _, err := Land(context.Background(), rt, "webshop", LandOptions{Force: true, Exec: noExec, LookPath: noGh}); err != nil {
			t.Fatalf("Land --force: %v", err)
		}
		if len(fg.pushCalls) != 1 {
			t.Errorf("pushCalls = %+v, want 1", fg.pushCalls)
		}
	})
}

// TestLandNoGateSaysSo pins the two no-gate words: a binding with no gate
// reports "none", and --no-gate reports "skipped", so a skipped gate never
// reads as a passing one.
func TestLandNoGateSaysSo(t *testing.T) {
	cases := []struct {
		name string
		gate string
		opts LandOptions
		want string
	}{
		{"a binding with no gate", "", LandOptions{}, "none"},
		{"--no-gate skips the binding's gate", "make check", LandOptions{NoGate: true}, "skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := newRuntime(t, &fakePanes{})
			rt.Git = &fakeGit{}
			landBinding(t, rt, func(b *store.Binding) { b.Gate = c.gate })
			ex := &landExecStub{codes: []int{0}}

			res, err := Land(context.Background(), rt, "webshop", LandOptions{
				NoGate: c.opts.NoGate, Exec: ex.run, LookPath: noGh,
			})
			if err != nil {
				t.Fatalf("Land: %v", err)
			}
			if res.GateResult != c.want {
				t.Errorf("GateResult = %q, want %q", res.GateResult, c.want)
			}
			if len(ex.calls) != 0 {
				t.Errorf("no gate should have run, got %+v", ex.calls)
			}
		})
	}
}

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
