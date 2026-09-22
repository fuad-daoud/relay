package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// seedBound binds webshop on the agy test candidate, headless (#303): the
// runtime gets a fakeRunner, and no herdr agent is added for the builder --
// there is none.
func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// TestSendArmsSessionCursorForPaneBuilder pins Send's pane branch (#184):
// after the plan lands, the builder's cursor is cut at the located session
// record's current size, so the round's log holds only what the builder
// writes to its own record from here on.
// The role is selected with --agent at launch (#85); the plan prompt is
// the plan prompt, on round 1 as on every other.
// TestPromptRetryReportsANonStallFailureAsItself keeps the second failure
// honest: the retry can fail for an unrelated reason -- the builder became
// blocked between the two attempts, say -- and calling that a stall sends the
// human looking at the wrong thing.
// TestPromptRetryReportsASecondStallAsAStall is the other branch: two genuine
// stalls stay labelled as such, and relay never fires a third time.
// A binding that appears only after the pre-lock load must never be addressed
// with the zero agent's empty pane id. See round 3, Task 1.
// TestSendRefusesPaused: a paused binding has no builder to address and its
// worktree is gone; the human resumes it first. No plan is staged.
// TestSendDriftEntryPinsConfirmedDoesNotShadowPendingReport asserts that
// an unconsumed pending report is still returned by Pull after a Send with drift.
// An unconfirmed drift entry would shadow the report in pendingForPlanner.
// TestComposePromptNamesPlanReportAndMarkerInOrder pins the handoff contract:
// the builder is told the plan, the report and the completion marker, in that
// order, and told the marker is its last action (spec §3.3).
func TestComposePromptNamesPlanReportAndMarkerInOrder(t *testing.T) {
	b := store.Binding{Name: "webshop", CWD: "/repo/webshop", Round: 3}
	got := composePrompt(b, "/s/003-plan.md", "/s/003-report.md", "/s/003-done")

	wantOrigin := OriginLine("webshop", 3, store.DirToBuilder, store.KindPlan)
	firstLine := strings.SplitN(got, "\n", 2)[0]
	if firstLine != wantOrigin {
		t.Errorf("first line = %q, want origin line %q", firstLine, wantOrigin)
	}
	if !strings.Contains(got, "Your working tree is: /repo/webshop") {
		t.Errorf("prompt must name the working tree (#192), got:\n%s", got)
	}
	if !strings.Contains(got, "create the done marker,\nand do nothing else.") {
		t.Errorf("prompt must state the git-status halt rule, got:\n%s", got)
	}

	plan := strings.Index(got, "/s/003-plan.md")
	report := strings.Index(got, "/s/003-report.md")
	done := strings.Index(got, "/s/003-done")
	if plan < 0 || report < 0 || done < 0 || !(plan < report && report < done) {
		t.Fatalf("paths must appear plan < report < marker, got:\n%s", got)
	}
	if !strings.Contains(got, "as the very last thing you do") {
		t.Errorf("prompt must say the marker is the last action, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Reply here with only the report path.") {
		t.Errorf("prompt must end with the reply instruction, got:\n%s", got)
	}
}

// TestSendDryRunPaneMakesNoWrites pins the dry run's contract for a pane
// binding: it describes the round Send would open and writes nothing -- no
// staged plan, no log entry, no prompt, no change to the binding (#149).
// endProcess scripts the binding's recorded process as exited, so a second
// Send into the same binding is allowed: one process per round (#99 §5.2).
func endProcess(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runtime has no fakeRunner")
	}
	if b.Builder.PID != 0 {
		fr.script(b.Builder.PID, false)
	}
}

// TestSendDryRunHeadlessShowsArgv pins that a dry run of a headless binding
// reports the launch it would use -- the harness binary first -- without
// starting anything.
// TestSendDryRunGateNote pins the advisory gate note: a rate-limited candidate
// still dry-runs, but the note says the daemon would switch after the start.
// TestSendDryRunErrorsMatchSend pins §6: for every precondition, the dry run
// returns the identical error Send would, exit-1 text included, and writes
// nothing on the way.
// TestRenderDryRunShape pins the exact rendered shape of a dry run: the seven
// labelled lines, in order, with the 1024-based size.
func TestRenderDryRunShape(t *testing.T) {
	d := DryRun{
		Name:       "api-auth",
		Round:      5,
		Mode:       "headless",
		Candidate:  "agy/google/gemini-3.8-flash-high",
		Where:      "/usr/bin/agy -p",
		PlanPath:   "/home/p/.local/state/relay/api-auth/005-plan.md",
		PlanFrom:   "./plan.md",
		PlanBytes:  4198,
		ReportPath: "/home/p/.local/state/relay/api-auth/005-report.md",
		DonePath:   "/home/p/.local/state/relay/api-auth/005-done",
		Tier:       "yolo",
		PromptHead: []string{
			`relay: round 5 · to builder "api-auth" · from the planner (not the human)`,
			"Your working tree is: /home/p/.worktrees/api-auth",
		},
	}
	want := `would send round 5 to api-auth
  builder   headless agy/google/gemini-3.8-flash-high
  where     /usr/bin/agy -p
  tier      yolo
  plan      /home/p/.local/state/relay/api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)
  report    /home/p/.local/state/relay/api-auth/005-report.md
  marker    /home/p/.local/state/relay/api-auth/005-done
  prompt    relay: round 5 · to builder "api-auth" · from the planner (not the human)
            Your working tree is: /home/p/.worktrees/api-auth
`
	got := RenderDryRun(d)
	if got != want {
		t.Errorf("RenderDryRun:\n%s\nwant:\n%s", got, want)
	}

	// The seven labelled lines appear in this order.
	at := -1
	for _, label := range []string{"builder", "where", "tier", "plan", "report", "marker", "prompt"} {
		i := strings.Index(got, "  "+label+" ")
		if i < 0 {
			t.Fatalf("no %q line in:\n%s", label, got)
		}
		if i < at {
			t.Errorf("label %q is out of order", label)
		}
		at = i
	}

	// A gated candidate's note rides on the builder line.
	d.GateNote = "rate-limited until 00:26; the daemon would switch after start"
	if !strings.Contains(RenderDryRun(d), "(rate-limited until 00:26; the daemon would switch after start)") {
		t.Errorf("the gate note must render on the builder line:\n%s", RenderDryRun(d))
	}
}

// TestVerifyPolicyDefault pins #144's trigger: an explicit SendOptions.Verify
// wins, and a plain Send takes policy.json verify.default.
