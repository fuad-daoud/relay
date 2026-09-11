# A delivery's reason is visible: log held-path outcomes, show hold progress in status (#75)

**Issue:** #75, a follow-up to #69 (merged in #73).

There is no separate spec. The rule is written out in full here; this plan is
self-contained.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Why this exists

`DeliverPending` (`internal/relay/deliver.go`) decides between four outcomes on
a focused planner and records which one in `Delivery.Reason`. Nothing in
production reads it: the daemon logs only its start line and two error lines,
`log.jsonl` records `delivered_at` but not why, and `relay status` shows
`HELD` with no clock. Verifying #69 live meant polling `herdr agent list` and
`bind.json` by hand to reconstruct a decision the daemon had already made and
thrown away. This plan makes the decision visible. **It changes nothing about
what the daemon decides.**

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand or reach herdr.** CI runners
  have no `herdr` binary. Every test in this plan is in `internal/relay` or
  `internal/ui` against the existing fakes.
- Nothing in `DeliverPending` or `plannerHold` changes which branch is taken.
  Every existing test in `deliver_test.go` and `held_test.go` must pass with
  only the additive field changes below.
- The unfocused delivery (`Delivery.Reason == ""`) stays silent in the log.
  It is the ordinary path and has always been.
- The `payload held` log line fires on the **transition into** `Held` only.
  A held tick whose reason differs from the last tick's is not logged: the
  reason string carries the quiet clock and would change every tick.
- Every new exported symbol gets a doc comment explaining the rationale, in
  the house style (read the comments on `Delivery`, `PendingInfo`, and
  `Binding.PlannerScreen` before writing yours).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: surface the held-path decision in the log and in status

**Files:**
- Modify: `internal/relay/deliver.go` (the `Delivery` struct, two returns)
- Modify: `internal/relay/held.go` (`plannerHold`, `clearPlannerScreen`)
- Modify: `internal/store/types.go` (the `Binding` struct)
- Modify: `internal/relay/reconcile.go` (`deliverAndSettle`)
- Create: `internal/relay/reconcile_log_test.go`
- Modify: `internal/relay/deliver_test.go` (one new test)
- Modify: `internal/relay/status.go` (`PendingInfo`, `statusRow`, `RenderStatus`, new `HoldInfo`, new `HoldText`)
- Modify: `internal/relay/status_test.go` (four new tests)
- Modify: `internal/ui/list.go` (`renderListRow`)
- Modify: `internal/ui/list_test.go` (one new test)
- Modify: `README.md`, `docs/design.md`

**Interfaces produced:**
- `relay.Delivery.Round int` -- the pending entry's round, set on `Held` and `Delivered`
- `store.Binding.HeldGrace time.Duration` (`json:"held_grace,omitempty"`)
- `relay.HoldInfo{ QuietMS int; GraceMS int }` -- on `PendingInfo.Hold *HoldInfo` (`json:"hold,omitempty"`)
- `relay.HoldText(b BindingStatus) string`

- [ ] **Step 1: carry the round and the grace in state, test first**

  In `internal/relay/deliver_test.go`, add after `TestHeldDeliversAfterQuietGrace`:

  ```go
  func TestHeldRecordsRoundAndGraceInState(t *testing.T) {
  	f := &fakeHerdr{}
  	rt, b := queuedBinding(t, f)
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
  	f.readOut = claudeDraftScreen
  	f.prompts = nil
  	rt.HeldGrace = 5 * time.Second
  	now, rt := heldClock(rt)

  	next, got := heldTick(t, rt, b, f)
  	if !got.Held {
  		t.Fatalf("first tick: want held, got %+v", got)
  	}
  	if got.Round != 1 {
  		t.Errorf("held Round = %d, want 1", got.Round)
  	}
  	if next.HeldGrace != 5*time.Second {
  		t.Errorf("HeldGrace = %v, want 5s: status cannot show a fraction without it", next.HeldGrace)
  	}
  	next.State = store.StateHeld

  	// A read failure holds with everything untouched, the grace included.
  	f.readErr = errors.New("boom")
  	*now = now.Add(time.Second)
  	next, got = heldTick(t, rt, next, f)
  	if !got.Held || next.HeldGrace != 5*time.Second {
  		t.Fatalf("read failure must keep HeldGrace, got %+v / %+v", got, next)
  	}
  	f.readErr = nil

  	*now = now.Add(5 * time.Second)
  	next, got = heldTick(t, rt, next, f)
  	if !got.Delivered {
  		t.Fatalf("want delivered, got %+v", got)
  	}
  	if got.Round != 1 {
  		t.Errorf("delivered Round = %d, want 1", got.Round)
  	}
  	if next.HeldGrace != 0 {
  		t.Errorf("a delivery must clear HeldGrace with the fingerprint, got %v", next.HeldGrace)
  	}
  }
  ```

  Run: `go test ./internal/relay/ -run TestHeldRecordsRoundAndGraceInState`.
  Expected: compile failure (`got.Round`, `next.HeldGrace` undefined).

  In `internal/store/types.go`, directly after `PlannerScreenAt`, add:

  ```go
  	// HeldGrace is the grace the daemon was running with when it started the
  	// PlannerScreen clock. It exists so `relay status`, which runs in another
  	// process and never sees `--held-grace`, can print "quiet 23s of 1m0s"
  	// rather than a bare duration. Written with PlannerScreen, cleared with it.
  	HeldGrace time.Duration `json:"held_grace,omitempty"`
  ```

  In `internal/relay/deliver.go`, add to `Delivery` after `Reason`:

  ```go
  	// Round is the pending entry's round, set on Held and Delivered so the
  	// caller can log the outcome without re-reading the queue.
  	Round int
  ```

  Set it on the two returns that know `pending`: the hold return becomes
  `Delivery{Held: true, Reason: reason, Round: pending.Round}` and the final
  return becomes
  `Delivery{Delivered: true, Reason: reason, Round: pending.Round}`.

  In `internal/relay/held.go`:
  - `clearPlannerScreen` also sets `b.HeldGrace = 0`. Extend its comment:
    "HeldGrace goes with them: it describes the clock, and there is no clock."
  - In `plannerHold`, in the branch that (re)starts the clock
    (`b.PlannerScreen == "" || fp != b.PlannerScreen`), set
    `b.HeldGrace = grace` alongside `b.PlannerScreen, b.PlannerScreenAt = fp, now`.
    The read-failure return and the steady branches do not touch it.

  Run: `go test ./internal/relay/`. Expected: PASS, all of it. The existing
  `TestHeldDeliversAfterQuietGrace` still asserts `PlannerScreen == ""` after
  delivery; `store.SameBinding` compares JSON, so `omitempty` keeps a
  zero `HeldGrace` invisible to it.

- [ ] **Step 2: log the transition into held and the focused-path delivery, test first**

  Create `internal/relay/reconcile_log_test.go`:

  ```go
  package relay

  import (
  	"bytes"
  	"context"
  	"log/slog"
  	"strings"
  	"testing"
  	"time"

  	"github.com/fuad-daoud/relay/internal/herdr"
  	"github.com/fuad-daoud/relay/internal/store"
  )

  // captureLog routes slog's default logger into a buffer for one test. The
  // daemon logs through the default logger, so this is the only seam.
  func captureLog(t *testing.T) *bytes.Buffer {
  	t.Helper()
  	var buf bytes.Buffer
  	prev := slog.Default()
  	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
  	t.Cleanup(func() { slog.SetDefault(prev) })
  	return &buf
  }

  // settleTick is one daemon tick's deliverAndSettle over a binding, under the
  // state lock the daemon would hold, returning the binding to persist.
  func settleTick(t *testing.T, rt Runtime, b store.Binding, f *fakeHerdr) store.Binding {
  	t.Helper()
  	var next store.Binding
  	err := rt.Store.WithLock(func(tx *store.Tx) error {
  		var err error
  		next, err = deliverAndSettle(context.Background(), rt, tx, b, f.agents)
  		return err
  	})
  	if err != nil {
  		t.Fatalf("deliverAndSettle: %v", err)
  	}
  	return next
  }

  func TestSettleLogsHeldOnceAndDeliveredWithReason(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{}
  	rt, b := queuedBinding(t, f)
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true)}
  	f.readOut = claudeDraftScreen
  	f.prompts = nil
  	now, rt := heldClock(rt)

  	next := settleTick(t, rt, b, f)
  	if next.State != store.StateHeld {
  		t.Fatalf("first tick: State = %q, want held", next.State)
  	}
  	for i := 0; i < 2; i++ {
  		*now = now.Add(10 * time.Second)
  		next = settleTick(t, rt, next, f)
  		if next.State != store.StateHeld {
  			t.Fatalf("tick %d: State = %q, want held", i+2, next.State)
  		}
  	}

  	if n := strings.Count(buf.String(), `msg="payload held"`); n != 1 {
  		t.Fatalf("payload held logged %d times across three held ticks, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), "binding=webshop") || !strings.Contains(buf.String(), "round=1") {
  		t.Errorf("held line must name the binding and round:\n%s", buf.String())
  	}
  	if strings.Contains(buf.String(), `msg="payload delivered"`) {
  		t.Fatalf("nothing was delivered yet:\n%s", buf.String())
  	}

  	*now = now.Add(DefaultHeldGrace)
  	next = settleTick(t, rt, next, f)
  	if next.State != store.StateActive {
  		t.Fatalf("after grace: State = %q, want active", next.State)
  	}
  	if n := strings.Count(buf.String(), `msg="payload delivered"`); n != 1 {
  		t.Fatalf("payload delivered logged %d times, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), `reason="planner focused, quiet for 1m0s"`) {
  		t.Errorf("delivered line must carry the grace reason:\n%s", buf.String())
  	}
  }

  func TestSettleStaysSilentOnTheUnfocusedPath(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{}
  	rt, b := queuedBinding(t, f)
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
  	f.prompts = nil

  	settleTick(t, rt, b, f)
  	if len(f.prompts) != 1 {
  		t.Fatalf("prompts = %+v, want one", f.prompts)
  	}
  	if strings.Contains(buf.String(), "payload") {
  		t.Errorf("the unfocused delivery is the ordinary path and must not log:\n%s", buf.String())
  	}
  }
  ```

  Run: `go test ./internal/relay/ -run TestSettle`. Expected: FAIL on the
  held count (0, want 1).

  In `internal/relay/reconcile.go`, rewrite `deliverAndSettle`:

  ```go
  // deliverAndSettle attempts any pending delivery and folds the result into the
  // binding's state. It is also where a held-path decision becomes visible:
  // DeliverPending records why it held or injected, and nothing else in
  // production reads that reason (#75).
  func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
  	prev := b.State
  	next, got, err := DeliverPending(ctx, rt, tx, b, agents)
  	if err != nil {
  		return b, err
  	}
  	b = next

  	switch {
  	case got.Held && prev != store.StateHeld:
  		// Log the transition only. A held tick's reason carries the quiet
  		// clock ("quiet 23s of 1m0s") and would change every tick.
  		slog.Info("payload held", "binding", b.Name, "round", got.Round, "reason", got.Reason)
  	case got.Delivered && got.Reason != "":
  		// The two focused-path injects. The unfocused delivery has an empty
  		// reason and stays silent: it is the ordinary path.
  		slog.Info("payload delivered", "binding", b.Name, "round", got.Round, "reason", got.Reason)
  	}

  	switch {
  	case got.PlannerGone:
  		b.State = store.StateOrphaned
  	case got.Held:
  		b.State = store.StateHeld
  	case got.Delivered && b.State == store.StateHeld:
  		b.State = store.StateActive
  	case got.Empty && b.State == store.StateHeld:
  		// Nothing is waiting any more, so the hold is over. This is the path a
  		// `relay pull` leaves behind: it claims the payload without delivering
  		// it, so nothing else ever clears Held.
  		b.State = store.StateActive
  	}

  	return b, nil
  }
  ```

  Add `"log/slog"` to the file's imports.

  Run: `go test ./internal/relay/`. Expected: PASS.

- [ ] **Step 3: the hold clock in status, test first**

  In `internal/relay/status_test.go`, add after `TestStatusSurfacesHeldPending`:

  ```go
  // heldStatusBinding saves a HELD binding whose planner clock started 23s
  // before the runtime's fixed clock, with the grace the test supplies.
  func heldStatusBinding(t *testing.T, f *fakeHerdr, screen string, grace time.Duration) Runtime {
  	t.Helper()
  	rt, b := queuedBinding(t, f)
  	b.State = store.StateHeld
  	b.PlannerScreen = screen
  	if screen != "" {
  		b.PlannerScreenAt = baseTime.Add(-23 * time.Second)
  	}
  	b.HeldGrace = grace
  	if err := rt.Store.Save(b); err != nil {
  		t.Fatalf("Save: %v", err)
  	}
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, true), builderAgent(herdr.StatusIdle)}
  	return rt
  }

  func TestStatusShowsHoldClockAgainstGrace(t *testing.T) {
  	f := &fakeHerdr{}
  	rt := heldStatusBinding(t, f, "fp", time.Minute)

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	hold := rep.Bindings[0].Pending.Hold
  	if hold == nil || hold.QuietMS != 23000 || hold.GraceMS != 60000 {
  		t.Fatalf("Hold = %+v, want quiet 23000ms of 60000ms", hold)
  	}
  	text := RenderStatus(rep)
  	if !strings.Contains(text, "pending  report round 1 -> planner, held: quiet 23s of 1m0s") {
  		t.Errorf("rendered status = %q", text)
  	}
  }

  func TestStatusShowsHoldClockWithoutGrace(t *testing.T) {
  	f := &fakeHerdr{}
  	rt := heldStatusBinding(t, f, "fp", 0)

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	hold := rep.Bindings[0].Pending.Hold
  	if hold == nil || hold.QuietMS != 23000 || hold.GraceMS != 0 {
  		t.Fatalf("Hold = %+v, want quiet 23000ms with no grace", hold)
  	}
  	text := RenderStatus(rep)
  	if !strings.Contains(text, "held: quiet 23s\n") {
  		t.Errorf("a binding held before HeldGrace existed shows the quiet time alone, got %q", text)
  	}
  }

  func TestStatusShowsHoldWaitingForScreen(t *testing.T) {
  	f := &fakeHerdr{}
  	rt := heldStatusBinding(t, f, "", time.Minute)

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	if rep.Bindings[0].Pending.Hold != nil {
  		t.Fatalf("Hold = %+v, want nil: the clock has not started", rep.Bindings[0].Pending.Hold)
  	}
  	text := RenderStatus(rep)
  	if !strings.Contains(text, "held: waiting for the planner's screen") {
  		t.Errorf("rendered status = %q", text)
  	}
  }

  func TestStatusPendingLineUnchangedWhenNotHeld(t *testing.T) {
  	f := &fakeHerdr{}
  	rt, _ := queuedBinding(t, f)
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	if rep.Bindings[0].Pending == nil || rep.Bindings[0].Pending.Hold != nil {
  		t.Fatalf("Pending = %+v, want a pending with no hold", rep.Bindings[0].Pending)
  	}
  	text := RenderStatus(rep)
  	if !strings.Contains(text, "pending  report round 1 -> planner\n") || strings.Contains(text, "held:") {
  		t.Errorf("an active binding's pending line must not mention a hold, got %q", text)
  	}
  }
  ```

  Add `"time"` to the test file's imports if it is not already there.

  Run: `go test ./internal/relay/ -run 'TestStatusShowsHold|TestStatusPendingLine'`.
  Expected: compile failure (`Hold` undefined).

  In `internal/relay/status.go`:

  Extend `PendingInfo` and add `HoldInfo` directly below it:

  ```go
  // PendingInfo describes a payload waiting on the planner.
  type PendingInfo struct {
  	Round int        `json:"round"`
  	Kind  store.Kind `json:"kind"`
  	// Hold is the daemon's quiet clock for a HELD binding: how long the
  	// planner's screen has been unchanged, against the grace it will be
  	// injected at. Nil when the binding is not held, and nil when it is held
  	// but the clock has not started -- a failed screen read, or a hold
  	// recorded before the daemon read the screen. The human should know the
  	// clock is not running.
  	Hold *HoldInfo `json:"hold,omitempty"`
  }

  // HoldInfo is the quiet clock carried as data, like LastEvent: a statusline
  // consumer reads the two numbers, and RenderStatus formats them. Milliseconds
  // as ints, the way RoundTimeoutMS is, not time.Duration's nanoseconds.
  type HoldInfo struct {
  	QuietMS int `json:"quiet_ms"`
  	// GraceMS is zero when the binding was held by a daemon that did not
  	// record its grace (state written before HeldGrace existed). status then
  	// shows the quiet time alone rather than guess a fraction.
  	GraceMS int `json:"grace_ms,omitempty"`
  }
  ```

  In `statusRow`, replace the `if found { … }` block:

  ```go
  	if found {
  		row.Pending = &PendingInfo{Round: pending.Round, Kind: pending.Kind}
  		if b.State == store.StateHeld && b.PlannerScreen != "" {
  			quiet := rt.Now().UTC().Sub(b.PlannerScreenAt)
  			if quiet < 0 {
  				quiet = 0
  			}
  			row.Pending.Hold = &HoldInfo{
  				QuietMS: int(quiet / time.Millisecond),
  				GraceMS: int(b.HeldGrace / time.Millisecond),
  			}
  		}
  	}
  ```

  Add `HoldText` after `displayState`:

  ```go
  // HoldText is the human form of a held binding's clock, shared by
  // RenderStatus and the TUI so the two never drift: "quiet 23s of 1m0s",
  // "quiet 23s" when the grace is unknown, or "waiting for the planner's
  // screen" when the clock has not started. Empty for any binding that is
  // not HELD with a pending payload.
  func HoldText(b BindingStatus) string {
  	if b.Display != "HELD" || b.Pending == nil {
  		return ""
  	}
  	h := b.Pending.Hold
  	if h == nil {
  		return "waiting for the planner's screen"
  	}
  	quiet := (time.Duration(h.QuietMS) * time.Millisecond).Truncate(time.Second)
  	if h.GraceMS == 0 {
  		return fmt.Sprintf("quiet %s", quiet)
  	}
  	return fmt.Sprintf("quiet %s of %s", quiet, time.Duration(h.GraceMS)*time.Millisecond)
  }
  ```

  In `RenderStatus`, replace the pending branch:

  ```go
  		if b.Pending != nil {
  			fmt.Fprintf(&sb, "  pending  %s round %d -> planner", b.Pending.Kind, b.Pending.Round)
  			if hold := HoldText(b); hold != "" {
  				fmt.Fprintf(&sb, ", held: %s", hold)
  			}
  			fmt.Fprint(&sb, "\n\n")
  		} else {
  			fmt.Fprint(&sb, "  pending  --\n\n")
  		}
  ```

  Run: `go test ./internal/relay/`. Expected: PASS, including the existing
  `TestStatusJSONCarriesStructuredFields` (a `hold` object is additive).

- [ ] **Step 4: the TUI list row, test first**

  In `internal/ui/list_test.go`, add a test next to the existing
  `renderListRow` test (the one asserting `want1`, `want2`, `want3`):

  ```go
  func TestRenderListRowShowsHoldClock(t *testing.T) {
  	b := relay.BindingStatus{
  		Name: "relay-held", Workspace: "wM", Round: 3, Display: "HELD",
  		BuilderAlias: "agy", BuilderStatus: "idle",
  		Pending: &relay.PendingInfo{
  			Round: 3, Kind: store.KindReport,
  			Hold:  &relay.HoldInfo{QuietMS: 23000, GraceMS: 60000},
  		},
  	}
  	got := renderListRow(b, false)
  	if !strings.HasSuffix(got, "pending report r3, held: quiet 23s of 1m0s") {
  		t.Errorf("row = %q", got)
  	}

  	b.Pending.Hold = nil
  	got = renderListRow(b, false)
  	if !strings.HasSuffix(got, "pending report r3, held: waiting for the planner's screen") {
  		t.Errorf("row = %q", got)
  	}
  }
  ```

  Run: `go test ./internal/ui/ -run TestRenderListRowShowsHoldClock`.
  Expected: compile failure (`Hold` undefined) or FAIL on the suffix.

  In `internal/ui/list.go` `renderListRow`, after the `if b.Pending != nil { … }`
  block that builds `pending`, add:

  ```go
  	if hold := relay.HoldText(b); hold != "" {
  		pending += ", held: " + hold
  	}
  ```

  Run: `go test ./internal/ui/`. Expected: PASS. The three existing row
  fixtures have no hold and render exactly as before.

- [ ] **Step 5: docs**

  `README.md`, the `HELD` bullet under the state list (around line 488),
  currently:

  > **HELD** — a payload is ready for the planner, but the planner pane is
  > focused, so relay is holding it rather than typing into it.

  Append one sentence: "`relay status` shows the hold's clock on the
  `pending` line: how long the planner's screen has been quiet against
  `--held-grace`, or that the clock has not started because the screen
  could not be read."

  `docs/design.md`, the sample status block (around line 269) has:

  ```
    pending  report 007 -> planner, held 45s, delivers when you leave the pane
  ```

  Replace that one line with the real format:

  ```
    pending  report round 7 -> planner, held: quiet 45s of 1m0s
  ```

  No other doc changes.

- [ ] **Step 6: full check and mutations**

  `make check` (or constituents) green.

  Do each, run the named test, revert, record the result in your report:
  1. In `deliverAndSettle`, change `got.Held && prev != store.StateHeld` to
     `got.Held`. Expect `TestSettleLogsHeldOnceAndDeliveredWithReason` to
     fail on the held count (3, want 1).
  2. In `plannerHold`, delete the `b.HeldGrace = grace` assignment. Expect
     `TestHeldRecordsRoundAndGraceInState` to fail on `HeldGrace`.
  3. In `statusRow`, set `QuietMS: 0`. Expect
     `TestStatusShowsHoldClockAgainstGrace` to fail.
  4. In `HoldText`, return `""` when `h == nil`. Expect
     `TestStatusShowsHoldWaitingForScreen` to fail.

- [ ] **Step 7: commit**

  One commit. Subject:
  `feat(relay): log why a held payload was held or injected, and show the hold clock in status (#75)`.
  Body: the two log lines and when each fires (transition into held; the two
  focused-path injects), that the unfocused path stays silent, the
  `HeldGrace` field and why status needs it, and the `HoldInfo` shape on the
  JSON status. Do not push.

## Report

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The mutation table, all four.
- Anything you stopped on, or any place the plan and the code disagreed.
