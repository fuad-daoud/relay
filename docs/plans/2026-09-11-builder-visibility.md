# The builder-side decisions are visible: log nudge, scrape, halt and blocked; show the nudge clock in status (#77)

**Issue:** #77, the builder-side mirror of #75 (merged in #76).

There is no separate spec. The rule is written out in full here; this plan is
self-contained. `docs/plans/2026-09-11-delivery-visibility.md` is the plan
this one mirrors, and `internal/relay/reconcile_log_test.go` is the test file
it extends.

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

`handleIdleBuilder` (`internal/relay/reconcile.go`) decides, every tick a
builder reads idle, between waiting for the report, nudging once, watching
the terminal for `nudgeGrace`, scraping the terminal, or doing nothing because
the screen could not be read. `haltBinding` decides a round is over for the
human. `handleBlockedBuilder` decides the builder needs an answer. None of it
reaches the daemon log, and the quiescence clock that already lives in
`BuilderScreenAt` is not in `relay status`. This plan makes those decisions
visible. **It changes nothing about what the daemon decides.**

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand or reach herdr.** CI runners
  have no `herdr` binary. Every test in this plan is in `internal/relay`
  against `fakeHerdr`.
- Nothing in `handleIdleBuilder`, `builderQuiescent`, `nudgeBuilder`,
  `scrapeReport`, `haltBinding` or `handleBlockedBuilder` changes which
  branch is taken or what is written. Every existing test in
  `reconcile_test.go` and `reconcile_blocked_test.go` must pass unchanged.
- Each log line fires at the point that already dedupes the decision. The
  one exception, `builder screen unreadable`, is per tick on purpose and at
  `Warn`.
- Every new exported symbol gets a doc comment explaining the rationale, in
  the house style (read the comments on `HoldInfo` and `LastEvent` in
  `internal/relay/status.go` before writing yours).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: surface the builder-side decisions in the log and in status

**Files:**
- Modify: `internal/relay/reconcile.go` (`handleIdleBuilder`, `nudgeBuilder`, `haltBinding`, `handleBlockedBuilder`)
- Modify: `internal/relay/reconcile_log_test.go` (four new tests; `captureLog` already exists there)
- Modify: `internal/relay/status.go` (`LastEvent`, `BindingStatus`, `statusRow`, `RenderStatus`, new `NudgeInfo`, new `NudgeText`)
- Modify: `internal/relay/status_test.go` (two new tests)
- Modify: `README.md`

**Interfaces produced:**
- `relay.LastEvent.Note string` (`json:"note,omitempty"`)
- `relay.NudgeInfo{ At time.Time; QuietMS int; GraceMS int }` on `BindingStatus.Nudge *NudgeInfo` (`json:"nudge,omitempty"`)
- `relay.NudgeText(n NudgeInfo) string`

- [ ] **Step 1: the five log lines, test first**

  Append to `internal/relay/reconcile_log_test.go`. `captureLog` is already
  defined at the top of that file; `sentBinding`, `reconcile`, `fakeClock`,
  `withClock`, `builderAgent`, `plannerWith`, `startGrace` and `nudgeGrace`
  come from the package's other test files and `reconcile.go`.

  ```go
  // nudgedBinding drives one binding through the nudge: plan sent, builder
  // idle past startGrace, no report on disk. It returns the binding after the
  // nudge tick and the clock the caller advances between later ticks.
  func nudgedBinding(t *testing.T, f *fakeHerdr) (Runtime, store.Binding, *fakeClock, []herdr.Agent) {
  	t.Helper()
  	rt, b := sentBinding(t, f)
  	clock := &fakeClock{now: baseTime}
  	rt = withClock(rt, clock)
  	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
  	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

  	b, err := reconcile(t, rt, b, agents)
  	if err != nil {
  		t.Fatalf("nudge Reconcile: %v", err)
  	}
  	return rt, b, clock, agents
  }

  func TestReconcileLogsNudgeOnceAndScrapeOnce(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{readOut: "half a screen of output"}
  	rt, b, clock, agents := nudgedBinding(t, f)

  	if n := strings.Count(buf.String(), `msg="builder nudged"`); n != 1 {
  		t.Fatalf("builder nudged logged %d times after the nudge tick, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), "binding=webshop") || !strings.Contains(buf.String(), "round=1") {
  		t.Errorf("nudge line must name the binding and round:\n%s", buf.String())
  	}

  	// Three ticks inside the grace: the quiescence clock runs, nothing is
  	// decided, nothing is logged.
  	before := buf.Len()
  	for i := 0; i < 3; i++ {
  		clock.Advance(10 * time.Second)
  		var err error
  		if b, err = reconcile(t, rt, b, agents); err != nil {
  			t.Fatalf("tick %d: %v", i+2, err)
  		}
  	}
  	if buf.Len() != before {
  		t.Fatalf("ticks inside the grace must not log:\n%s", buf.String()[before:])
  	}

  	clock.Advance(nudgeGrace)
  	got, err := reconcile(t, rt, b, agents)
  	if err != nil {
  		t.Fatalf("scrape Reconcile: %v", err)
  	}
  	if got.Round != 2 {
  		t.Fatalf("round = %d, want 2 after the scrape", got.Round)
  	}
  	if n := strings.Count(buf.String(), `msg="builder quiescent, scraping report"`); n != 1 {
  		t.Fatalf("scrape logged %d times, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), "quiet=1m30s") {
  		t.Errorf("scrape line must carry how long the screen was quiet:\n%s", buf.String())
  	}
  }

  func TestReconcileWarnsWhenBuilderScreenUnreadable(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{readOut: "terminal at nudge"}
  	rt, b, clock, agents := nudgedBinding(t, f)

  	clock.Advance(nudgeGrace + time.Second)
  	f.readErr = errors.New("simulated herdr read error")
  	got, err := reconcile(t, rt, b, agents)
  	if err != nil {
  		t.Fatalf("Reconcile must still swallow the read error: %v", err)
  	}
  	if got.Round != 1 {
  		t.Fatalf("round = %d, want 1: the behaviour must not change", got.Round)
  	}
  	if !strings.Contains(buf.String(), `level=WARN msg="builder screen unreadable"`) {
  		t.Errorf("a dropped read error must be logged at WARN:\n%s", buf.String())
  	}
  	if !strings.Contains(buf.String(), "simulated herdr read error") {
  		t.Errorf("the line must carry the error:\n%s", buf.String())
  	}
  }

  func TestReconcileLogsHaltOncePerRound(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{}
  	rt, b := sentBinding(t, f)
  	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
  	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
  	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false), builderAgent(herdr.StatusWorking)}

  	for i := 0; i < 3; i++ {
  		var err error
  		if b, err = reconcile(t, rt, b, agents); err != nil {
  			t.Fatalf("tick %d: %v", i+1, err)
  		}
  	}
  	if b.State != store.StateNeedsYou {
  		t.Fatalf("state = %s, want needs_you", b.State)
  	}
  	if n := strings.Count(buf.String(), `msg="binding halted"`); n != 1 {
  		t.Fatalf("binding halted logged %d times across three halted ticks, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), "has run past") {
  		t.Errorf("halt line must carry the reason:\n%s", buf.String())
  	}
  }

  func TestReconcileLogsBlockedOncePerRound(t *testing.T) {
  	buf := captureLog(t)
  	f := &fakeHerdr{readOut: "Allow edit to src/main.go?  1. Yes  2. No"}
  	rt, b := sentBinding(t, f)
  	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusBlocked)}

  	for i := 0; i < 2; i++ {
  		var err error
  		if b, err = reconcile(t, rt, b, agents); err != nil {
  			t.Fatalf("tick %d: %v", i+1, err)
  		}
  	}
  	if n := strings.Count(buf.String(), `msg="builder blocked"`); n != 1 {
  		t.Fatalf("builder blocked logged %d times across two blocked ticks, want 1:\n%s", n, buf.String())
  	}
  	if !strings.Contains(buf.String(), "question="+rt.Store.QuestionPath("webshop", 1)) {
  		t.Errorf("blocked line must point at the question file:\n%s", buf.String())
  	}
  }
  ```

  Add `"errors"` to the file's imports.

  Run: `go test ./internal/relay/ -run 'TestReconcileLogs|TestReconcileWarns'`.
  Expected: FAIL on every count (0, want 1).

  In `internal/relay/reconcile.go` (`"log/slog"` is already imported):

  - In `handleIdleBuilder`, the quiescence block becomes:

    ```go
    	next, quiescent, err := builderQuiescent(ctx, rt, b, nudgedAt)
    	if err != nil {
    		// The round stays open (a failed read is not evidence the builder
    		// stopped), but a read that keeps failing is a herdr problem the
    		// human should see. Per tick on purpose.
    		slog.Warn("builder screen unreadable", "binding", b.Name, "round", b.Round, "err", err)
    		return b, nil
    	}
    	if !quiescent {
    		return next, nil
    	}

    	// Once per round: the report scrapeReport queues ends this path.
    	slog.Info("builder quiescent, scraping report", "binding", next.Name, "round", next.Round,
    		"quiet", rt.Now().UTC().Sub(next.BuilderScreenAt).Truncate(time.Second))
    	return scrapeReport(ctx, rt, tx, next, entries, reportPath)
    ```

  - In `nudgeBuilder`, directly after the `tx.AppendLog` succeeds:

    ```go
    	// Once per round: nudgeTime gates the call.
    	slog.Info("builder nudged", "binding", b.Name, "round", b.Round)
    ```

  - In `haltBinding`, inside the `if b.HaltNotifiedRound != b.Round {` block,
    after the notify succeeds and before `b.HaltNotifiedRound = b.Round`:

    ```go
    		// Shares the notify's once-per-round dedup.
    		slog.Info("binding halted", "binding", b.Name, "round", b.Round, "reason", message)
    ```

  - In `handleBlockedBuilder`, directly after `Queue` succeeds:

    ```go
    	// Once per round: HasEntry on the question gates the call.
    	slog.Info("builder blocked", "binding", b.Name, "round", b.Round, "question", path)
    ```

  Run: `go test ./internal/relay/`. Expected: PASS, all of it.

- [ ] **Step 2: the nudge clock and the last-event note in status, test first**

  In `internal/relay/status_test.go`, add after `TestStatusPendingLineUnchangedWhenNotHeld`:

  ```go
  func TestStatusShowsNudgeClock(t *testing.T) {
  	f := &fakeHerdr{readOut: "half a screen of output"}
  	rt, b := sentBinding(t, f)
  	clock := &fakeClock{now: baseTime}
  	rt = withClock(rt, clock)
  	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
  	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

  	// One tick nudges and takes the fingerprint at baseTime; the daemon
  	// would persist it, so Status must see the saved binding.
  	b, err := reconcile(t, rt, b, agents)
  	if err != nil {
  		t.Fatalf("nudge Reconcile: %v", err)
  	}
  	if err := rt.Store.Save(b); err != nil {
  		t.Fatalf("Save: %v", err)
  	}
  	f.agents = agents
  	clock.Advance(23 * time.Second)

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	row := rep.Bindings[0]
  	if row.Nudge == nil {
  		t.Fatalf("a nudged round must carry a Nudge, got %+v", row)
  	}
  	if !row.Nudge.At.Equal(baseTime) || row.Nudge.QuietMS != 23000 || row.Nudge.GraceMS != 60000 {
  		t.Fatalf("Nudge = %+v, want at baseTime, quiet 23000ms of 60000ms", row.Nudge)
  	}
  	if row.Last == nil || row.Last.Note != "nudge" {
  		t.Fatalf("Last = %+v, want the nudge entry with its note", row.Last)
  	}

  	text := RenderStatus(rep)
  	if !strings.Contains(text, "  nudge    "+baseTime.Local().Format("15:04:05")+"  quiet 23s of 1m0s\n") {
  		t.Errorf("rendered status = %q", text)
  	}
  	if !strings.Contains(text, "plan to_builder round 1 (nudge)\n") {
  		t.Errorf("the last line must say it was a nudge, got %q", text)
  	}
  }

  func TestStatusHasNoNudgeLineWhenNotNudged(t *testing.T) {
  	f := &fakeHerdr{}
  	rt, _ := sentBinding(t, f)
  	f.agents = []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

  	rep, err := Status(context.Background(), rt)
  	if err != nil {
  		t.Fatalf("Status: %v", err)
  	}
  	if rep.Bindings[0].Nudge != nil {
  		t.Fatalf("Nudge = %+v, want nil on an un-nudged round", rep.Bindings[0].Nudge)
  	}
  	text := RenderStatus(rep)
  	if strings.Contains(text, "  nudge") || strings.Contains(text, "(") {
  		t.Errorf("no nudge line and no note on an ordinary round, got %q", text)
  	}
  }
  ```

  `time` is already imported in that file since #76.

  Run: `go test ./internal/relay/ -run 'TestStatusShowsNudgeClock|TestStatusHasNoNudgeLine'`.
  Expected: compile failure (`Nudge`, `Note` undefined).

  In `internal/relay/status.go`:

  Add `Note` to `LastEvent`:

  ```go
  type LastEvent struct {
  	TS        time.Time       `json:"ts"`
  	Round     int             `json:"round"`
  	Direction store.Direction `json:"direction"`
  	Kind      store.Kind      `json:"kind"`
  	// Note is the entry's note, when it has one. A nudge is a plan entry to
  	// the builder and a scrape is a report entry to the planner, so without
  	// it the last line after either reads exactly like the ordinary case.
  	Note string `json:"note,omitempty"`
  }
  ```

  Add to `BindingStatus`, directly after `Pending`:

  ```go
  	// Nudge is set while the current round has been nudged and no report has
  	// arrived: when relay nudged, and how long the builder's terminal has been
  	// unchanged against the grace after which relay scrapes it. Nil otherwise.
  	Nudge *NudgeInfo `json:"nudge,omitempty"`
  ```

  Add `NudgeInfo` and `NudgeText` after `HoldInfo`:

  ```go
  // NudgeInfo is the quiescence clock carried as data, like HoldInfo. The
  // grace needs no state field: nudgeGrace is a constant in this package, so
  // status knows it without the daemon writing it down.
  type NudgeInfo struct {
  	At      time.Time `json:"at"`
  	QuietMS int       `json:"quiet_ms"`
  	GraceMS int       `json:"grace_ms"`
  }

  // NudgeText is the human form of the quiescence clock: "quiet 23s of 1m0s".
  func NudgeText(n NudgeInfo) string {
  	quiet := (time.Duration(n.QuietMS) * time.Millisecond).Truncate(time.Second)
  	return fmt.Sprintf("quiet %s of %s", quiet, time.Duration(n.GraceMS)*time.Millisecond)
  }
  ```

  In `statusRow`, the `Last` block copies the note, and the nudge is derived
  from the same entries:

  ```go
  	if n := len(entries); n > 0 {
  		last := entries[n-1]
  		row.Last = &LastEvent{
  			TS: last.TS, Round: last.Round,
  			Direction: last.Direction, Kind: last.Kind,
  			Note: last.Note,
  		}
  	}

  	// What relay acts on is what it shows: the clock starts where
  	// builderQuiescent starts it -- at the last fingerprint, falling back to
  	// the nudge itself when none has been taken yet.
  	if nudgedAt, ok := nudgeTime(entries, b.Round); ok {
  		since := b.BuilderScreenAt
  		if since.IsZero() {
  			since = nudgedAt
  		}
  		quiet := rt.Now().UTC().Sub(since)
  		if quiet < 0 {
  			quiet = 0
  		}
  		row.Nudge = &NudgeInfo{
  			At:      nudgedAt,
  			QuietMS: int(quiet / time.Millisecond),
  			GraceMS: int(nudgeGrace / time.Millisecond),
  		}
  	}
  ```

  In `RenderStatus`, after the `detail` line and before the `last` line:

  ```go
  		if b.Nudge != nil {
  			fmt.Fprintf(&sb, "  nudge    %s  %s\n",
  				b.Nudge.At.Local().Format("15:04:05"), NudgeText(*b.Nudge))
  		}
  ```

  and the `last` line becomes:

  ```go
  		if b.Last != nil {
  			fmt.Fprintf(&sb, "  last     %s %s %s round %d",
  				b.Last.TS.Local().Format("15:04:05"), b.Last.Kind, b.Last.Direction, b.Last.Round)
  			if b.Last.Note != "" {
  				fmt.Fprintf(&sb, " (%s)", b.Last.Note)
  			}
  			fmt.Fprint(&sb, "\n")
  		}
  ```

  Run: `go test ./internal/relay/`. Expected: PASS, including
  `TestStatusJSONCarriesStructuredFields` (`note` and `nudge` are additive
  and omitted when empty).

- [ ] **Step 3: docs**

  `README.md`, the `relay status` bullet in the command list (around line
  183) ends with "...the last relayed event, and anything pending." Extend
  that sentence: "...the last relayed event, anything pending, and for a
  nudged builder how long its terminal has been quiet against the grace
  after which relay scrapes it."

  No other doc changes.

- [ ] **Step 4: full check and mutations**

  `make check` (or constituents) green.

  Do each, run the named test, revert, record the result in your report:
  1. In `haltBinding`, move the `binding halted` log line outside the
     `HaltNotifiedRound` guard (after the `if` block). Expect
     `TestReconcileLogsHaltOncePerRound` to fail on count (3, want 1).
  2. In `statusRow`, delete the `if since.IsZero()` fallback **and** change
     `since := b.BuilderScreenAt` to `since := rt.Now().UTC()`. Expect
     `TestStatusShowsNudgeClock` to fail on `QuietMS`.
  3. In `handleIdleBuilder`, delete the `slog.Warn` line. Expect
     `TestReconcileWarnsWhenBuilderScreenUnreadable` to fail.
  4. In `statusRow`, drop `Note: last.Note`. Expect
     `TestStatusShowsNudgeClock` to fail on `Last`.

- [ ] **Step 5: commit**

  One commit. Subject:
  `feat(relay): log builder nudge, scrape, halt and blocked decisions, and show the nudge clock in status (#77)`.
  Body: the five log lines and where each is deduped, that the read-error
  warning is per tick on purpose, the `nudge` status line and why it needs
  no state field, and the `(note)` on the last line. Do not push.

## Report

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The mutation table, all four.
- Anything you stopped on, or any place the plan and the code disagreed.
