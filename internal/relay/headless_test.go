package relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/store"
)

// seedHeadless binds webshop headless on the agy test candidate, with fr as
// the runtime's Runner. No herdr agent is added for the builder: there is
// none.
func TestHandleOfConvertsUnixSeconds(t *testing.T) {
	h := handleOf(store.Endpoint{PID: 42, StartedAt: 1_789_000_000})
	if h.PID != 42 || !h.StartedAt.Equal(time.Unix(1_789_000_000, 0)) {
		t.Errorf("handleOf = %+v", h)
	}
	if z := handleOf(store.Endpoint{}); z.PID != 0 || !z.StartedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("zero endpoint: %+v", z)
	}
}

func TestRoundBudgetIsTheBindingsRoundTimeout(t *testing.T) {
	if got := roundBudget(store.Binding{RoundTimeoutMS: 90 * 60 * 1000}); got != 90*time.Minute {
		t.Errorf("roundBudget = %s, want 1h30m", got)
	}
	// Store.Save fills RoundTimeoutMS, so zero is only ever a binding that was
	// never saved; the default matches the store's 24h.
	if got := roundBudget(store.Binding{}); got != 24*time.Hour {
		t.Errorf("roundBudget(zero) = %s, want 24h", got)
	}
}

func TestHeadlessLaunchPerKind(t *testing.T) {
	role, _ := harness.RoleByName("builder")
	set := candidateSet(t, testCandidatesJSON)
	lookup := func(token string) candidate.Candidate {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			t.Fatal(err)
		}
		c, err := set.Lookup(ref)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		token string
		want  []string
	}{
		{testAgyRef, []string{"agy", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor",
			"--output-format", "stream-json", "--print-timeout", "2h0m0s", "--add-dir", "/repo", "--dangerously-skip-permissions"}},
		{testClaudeRef, []string{"claude", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}},
		{testOpencodeRef, []string{"opencode", "run", "PROMPT", "-m", "test/m", "--agent", "plan-executor", "--format", "json", "--standalone"}},
	}
	for _, c := range cases {
		got, err := headlessLaunch(lookup(c.token), role, harness.TierHarness, 2*time.Hour, "PROMPT", "/repo", "/state/dir")
		if err != nil {
			t.Fatalf("%s: %v", c.token, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.token, got, c.want)
		}
	}
	if _, err := headlessLaunch(candidate.Candidate{Harness: "nope"}, role, harness.TierHarness, time.Hour, "x", "/repo", "/state/dir"); err == nil {
		t.Error("unknown harness kind must be an error, not a panic or an empty argv")
	}
}

// containsArg reports whether argv has flag immediately followed by value.
func containsArg(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// TestStartRoundSetsScope: rt.Scope set fills ProcSpec.Scope with the
// per-round unit name and the template's slice/weight/limits (#244, #216).
func TestScopeUnitNameSafe(t *testing.T) {
	if got, want := scopeUnitName(store.Binding{Name: "webshop", Round: 3}), "relay-round-local-webshop-3"; got != want {
		t.Errorf("scopeUnitName(no owner) = %q, want %q", got, want)
	}
	if got, want := scopeUnitName(store.Binding{Name: "web/shop no", Round: 1}), "relay-round-local-web-shop-no-1"; got != want {
		t.Errorf("scopeUnitName(unsafe name) = %q, want %q", got, want)
	}
}

// TestSendHeadlessWithoutRunnerStagesNothing pins #149's behaviour fix: the
// missing runner is a send precondition, checked before anything is staged.
// Before the fix Send wrote the plan first and only startRound discovered the
// runner was gone, leaving a staged plan and a NEEDS YOU binding behind a
// process that could never start.
// TestSendClearsAStaleHalt pins Send's success path: a binding that was
// halted for an earlier round must not carry that halt into a round it just
// successfully handed over.
func TestLogTailReturnsTheLastLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "003-builder.log")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 2); got != "c\nd" {
		t.Errorf("logTail(2) = %q, want \"c\\nd\"", got)
	}
	if got := logTail(p, 10); got != "a\nb\nc\nd" {
		t.Errorf("logTail(10) = %q, want the whole file without the trailing newline", got)
	}
	if got := logTail(filepath.Join(dir, "absent.log"), 3); got != "" {
		t.Errorf("logTail(absent) = %q, want empty", got)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 3); got != "" {
		t.Errorf("logTail(empty) = %q, want empty", got)
	}
}

func TestClearProcessKeepsIdentity(t *testing.T) {
	e := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 7, StartedAt: 9, LogPath: "/l", StreamRound: 3, StreamOffset: 99}
	got := clearProcess(e)
	want := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 3, StreamOffset: 99}
	if got != want {
		t.Errorf("clearProcess = %+v, want %+v", got, want)
	}
}

// streamWrite appends raw to webshop's round-1 stream file, creating it.
func streamWrite(t *testing.T, rt Runtime, raw string) {
	t.Helper()
	p := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// readLog is webshop's round-1 builder log, "" when absent.
func readLog(t *testing.T, rt Runtime) string {
	t.Helper()
	data, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		return ""
	}
	return string(data)
}

const (
	agyToolActive = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}` + "\n"
	agyToolDone   = `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command"}}` + "\n"
	agyResult     = `{"event":"result","result":{"status":"SUCCESS","response":"all done","denied_actions":[]}}` + "\n"
)

// sentHeadless is seedHeadless plus one Send: round 1 open, one process
// started on fr.
// TestReconcileHeadlessSkipsQueued pins #285: a queued round (Send(Defer)'s
// QueuedAt, PID still 0) has no process and no clocks, so Reconcile leaves
// it untouched -- no spawn, no halt -- until relay.Admit starts it.
//
// Mutation check: drop the `!b.QueuedAt.IsZero()` early return in
// reconcileHeadless and this fails on fr.specs no longer being 0 (the PID==0
// "spawn failed earlier" branch would otherwise treat it as NEEDS YOU-quiet,
// but a policy/candidate change could make it try to spawn).
// TestVerifyRoundStartsOnHeadlessClose pins #144's headless close path: the
// same reviewer a pane round's close starts must start when a headless round
// closes on its marker. The builder's own process is the first entry in
// fr.specs; the verify consult is the second, in the throwaway worktree.
//
// Mutation check (run and report): delete the wantVerify block from
// reconcileHeadless's close path and this fails on addDetachedWorktreeCalls.
// TestSendResetsRoundBudget pins #250 item 2 for the headless shape: a
// human's re-send is a fresh attempt, so it clears the round's switch
// bookkeeping along with Halt/HaltAt.
// switchHeadless runs switchBuilder on b inside the lock, the way Reconcile does.
func switchHeadless(t *testing.T, rt Runtime, b store.Binding, reason string, closeOld bool) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = switchBuilder(context.Background(), rt, tx, b, reason, closeOld, true)
		return err
	})
	return out, err
}

// exits returns the exit entries in webshop's log.
func exits(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindExit {
			out = append(out, e)
		}
	}
	return out
}

// TestReconcileHeadlessStampsStallWhenStreamQuiet pins #252's core under
// #135's shared clock: a live process whose stream file has not moved for
// stall_after_ms is stamped StalledSince = the stream's last activity, one
// advisory notice is raised, and nothing else happens. The second case is the
// mutation target: with the stream only 5m quiet the comparison must not fire,
// so inverting it (or comparing `<` for `>=`) makes both cases fail.
// TestReconcileHeadlessStallClearsWhenStreamMoves pins the clear side of
// #252: a stream that moves again clears the stamp, and a process that exits
// with a report clears it as the round closes.
// TestStatusHeadlessStalledLabel pins #252's label: a live, stalled headless
// builder reads "stalled <age>", and a live, unstalled one reads "working".
// TestReconcileHeadlessLostToDaemonRestartRelaunches pins #244 half 1: a
// builder whose recorded start predates the daemon's own could not have
// died on its own -- the daemon itself must have taken it down (a systemd
// restart, a kill -9 of the process tree) before the supervisor could write
// the relay-exit: trailer. relay relaunches the same candidate on the same
// round instead of switching, and charges nothing.
//
// Mutation check: drop the `Before(rt.StartedAt)` condition in headless.go's
// `lost` computation (making it always false) and this test must fail.
// TestLostBuilderRequeuesOnServer pins #285: on a server (Owner set), a
// builder lost to a daemon restart is re-queued at the head of the queue
// instead of relaunched -- a box reboot must not relaunch every builder past
// the cap. TestReconcileHeadlessLostToDaemonRestartRelaunches above is the
// mirror for Owner == "" (the local daemon): it must keep relaunching
// exactly as it does today.
//
// Mutation check: drop the `b.Owner != ""` branch in headless.go's lost
// handling and this fails on fr.specs staying at 1.
// TestReconcileHeadlessUnknownExitBeforeDaemonStartStillSwitches pins the
// negative cases of #244: a builder that started after the daemon (so its
// death cannot be blamed on a restart) still switches and counts, exactly
// as before -- and so does one whose daemon start is unknown (zero
// rt.StartedAt), the control case for every other test in this file.
// TestReconcileHeadlessLostToDaemonRestartRelaunchFails pins the failure
// path: the daemon recognizes the loss but cannot relaunch (e.g. the
// binary vanished); the binding halts naming both facts, and nothing is
// charged.
// TestReconcileHeadlessAliveWithReportButNoMarkerWaits is the headless half
// of the fix: a running process that has written a report is still running.
// Mutation: stat the report instead of the marker -> round 2, PID cleared.
// TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked: exit is a
// hard fact, so the report is trusted with the omission noted (spec §4.4).
// TestReconcileHeadlessMarkerClosesAndClearsTheHandle: the marker closes the
// round the same way for a process as for a pane, and the handle goes with it.
// escapeFixture seeds webshop headless with a Repo and a fake Git configured
// so a round that leaves the worktree's tree unchanged while the repo is
// dirty is detected as a worktree escape (#192): fg.snapshotTreeID is
// captured as the round's baseline by Send and compared against again at
// close, so leaving it alone between the two is what makes treeUnchanged
// hold.
// TestHeadlessMarkerCloseEscapedNote: the tree is unchanged and the source
// repo is dirty, but the builder still produced a report and its marker --
// the round closes normally, annotated "escaped" rather than halted (#192).
// TestHeadlessExitNoReportEscapedHalts: the tree is unchanged, the source
// repo is dirty, and the process exited without a report at all -- nothing
// suggests the builder ever touched its own tree, so relay halts NEEDS YOU
// rather than dispatching a replacement into the same broken setup (#192).
// TestHeadlessExitNoReportNoRepoSwitches pins that a binding with no Repo
// (an old bind.json, a --cwd bind, or an adopted one) is untouched by #192:
// escapeCheck's precondition on b.Repo keeps the existing switch-on-exit
// behaviour exactly as it was.
// twoBuilderJSON serves "builder" from exactly two candidates, so excluding
// both is reachable in one round -- testCandidatesJSON's third (unlisted)
// candidate would otherwise still be pickable and no halt would ever fire.
const twoBuilderJSON = `[
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

// TestReconcileHeadlessRoundExclusionThenAllGatedHalts is #191's end-to-end
// pin: A exits without a report and is excluded in favour of B; B then also
// exits without a report, and with every candidate serving "builder" now
// excluded, relay halts instead of dispatching a third pick into the same
// broken round. A report that eventually appears still closes the round
// normally, and finishRound clears the exclusion with the switch count.
func TestAppendLogMarker(t *testing.T) {
	t.Run("appends in order", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "builder.log")
		if err := os.WriteFile(p, []byte("first line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t1 := time.Date(2026, 9, 14, 10, 15, 30, 0, time.UTC)
		t2 := time.Date(2026, 9, 14, 10, 16, 45, 0, time.UTC)
		appendLogMarker(p, t1, "stopped: done")
		appendLogMarker(p, t2, "switched to x/y/z (why)")

		want := fmt.Sprintf("first line\n--- relay %s: stopped: done ---\n--- relay %s: switched to x/y/z (why) ---\n",
			t1.Local().Format("15:04:05"),
			t2.Local().Format("15:04:05"),
		)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("creates the file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "builder.log")
		t1 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
		appendLogMarker(p, t1, "stopped: done")

		want := fmt.Sprintf("--- relay %s: stopped: done ---\n", t1.Local().Format("15:04:05"))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("empty path is a no-op and unwritable path does not panic", func(t *testing.T) {
		appendLogMarker("", time.Now(), "stopped: done")

		dir := t.TempDir()
		appendLogMarker(dir, time.Now(), "stopped: done")
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Errorf("%s is no longer a directory", dir)
		}
	})
}

// TestGateHeadlessCallSiteHolds pins #132: the headless marker path holds
// the round while a configured gate runs -- no "exited without a report"
// handling (no KindExit, no switch) -- exactly as the pane call site does.
// TestRegateHeadlessStartsRepairProcess pins #132 part 2's intended case: a
// headless binding whose gate fails gets round N+1 started as a fresh process,
// with the repair plan as its prompt -- the same hand-off Send performs.
// claudeStreamLines is internal/usage/testdata/claude-stream.jsonl's lines,
// without their trailing newlines: the shape a claude builder's stream has,
// and the fixture TestDrainStreamRecordsSessionIDOnce feeds it.
func claudeStreamLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "usage", "testdata", "claude-stream.jsonl"))
	if err != nil {
		t.Fatalf("read claude fixture: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// sentClaudeHeadless is sentHeadless with a claude builder, so its stream is
// the claude fixture's shape -- the harness whose id arrives as session_id.
// TestDrainStreamRecordsSessionIDOnce pins #147: the first drained line that
// names a session records it on the endpoint, and a later line naming another
// (a sub-agent's) leaves it alone.
// TestReportEntryCarriesHeadlessSession pins #147: the report entry of a
// closed headless round names the stream's session, and the id is cleared
// from the endpoint once the round has closed.
