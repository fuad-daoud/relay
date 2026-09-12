# Completion marker: the builder says when the round is over (#114 step 4)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-completion-marker-design.md`. Section numbers
below (§) refer to it.
**Issue:** #114, recommended step 4. Does not close #114.
**Depends on:** nothing open.

**Goal:** A round closes when the builder creates `NNN-done`, checked every
tick in both the pane and headless paths. Without the marker, relay falls back
to today's nudge → quiescence → scrape, but closes with the report that exists
(note `unmarked`) rather than scraping over it.

**Architecture:** One new function, `closeOnMarker` (`reconcile.go`), is the
only place that reads `Store.DonePath`. `Reconcile` (pane) and
`reconcileHeadless` call it at the top of an open round, before any status or
liveness reasoning. `handleIdleBuilder` loses its `os.Stat(reportPath)` gate
and, after quiescence, closes with the report if there is one (`unmarked`) or
scrapes if there is not. `reconcileHeadless` swaps its report gate for
`closeOnMarker` and, on exit, closes `unmarked` when a report exists. The
builder prompt and nudge name the marker path. `queueReport`, diff capture,
drift, delivery and switching are untouched.

**Tech stack:** Go 1.22. Verification is `make check` (runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/completion-marker` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/completion-marker`, cut from `main`. |
| `~/.local/state/relay/completion-marker` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. The live verification is the planner's, after
merge. No test in `cmd/relay` is added by this plan: no subcommand changes.

## Global constraints

- `NNN-done` is **never opened or written by relay**. Only `os.Stat`. Only
  `err == nil` counts as present; every other error is "absent this tick".
- The report entry `Note` vocabulary is exactly: `""`, `scraped`, `unmarked`,
  `noreport` (§3.2). No other strings.
- Prompt texts are the spec's, verbatim (§3.3). Do not reword.
- `queueReport`, `CaptureRoundDiff`, `CaptureDrift`, `deliverAndSettle`,
  `switchBuilder`, `builderQuiescent`, `scrapeLines`, `startGrace`,
  `nudgeGrace` are not modified.
- No new binding `State`. An `unmarked` or `noreport` close is a normal close.
- One commit per task, on the worktree's branch.

---

### Task 1: `Store.DonePath`

**Files:**
- Modify: `internal/store/store.go:135-137` (add after `ReportPath`)
- Test: `internal/store/store_test.go:128-136` (`TestPathsAreZeroPaddedUnderBindingDir`)

**Interfaces:**
- Consumes: `func (s *Store) roundFile(name string, round int, kind, ext string) string` (existing, private).
- Produces: `func (s *Store) DonePath(name string, round int) string` → `<dir>/<name>/NNN-done`.

- [ ] **Step 1: Extend the path test so it fails**

In `internal/store/store_test.go`, inside `TestPathsAreZeroPaddedUnderBindingDir`, after the `ReportPath` check add:

```go
	if got, want := s.DonePath("webshop", 7), filepath.Join("/state", "webshop", "007-done"); got != want {
		t.Errorf("DonePath = %q, want %q", got, want)
	}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/store -run TestPathsAreZeroPaddedUnderBindingDir`
Expected: compile error, `s.DonePath undefined`.

- [ ] **Step 3: Add the method**

In `internal/store/store.go`, directly after `ReportPath`:

```go
// DonePath is the builder's completion marker for a round: an empty file it
// creates as its last action (spec 2026-09-12-completion-marker §1). relay
// only ever stats it.
func (s *Store) DonePath(name string, round int) string {
	return s.roundFile(name, round, "done", "")
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/store`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): DonePath names the round's completion marker (#114)"
```

---

### Task 2: Prompts name the marker

**Files:**
- Modify: `internal/relay/send.go:27-33` (`builderPrompt`), `:131-136` (call site), `:224-229` (`composePrompt`)
- Modify: `internal/relay/switch.go:138`
- Modify: `internal/relay/reconcile.go:37-38` (`nudgePrompt`)
- Modify: `internal/relay/headless_test.go:196-198`
- Test: `internal/relay/send_test.go` (new test appended)

**Interfaces:**
- Consumes: `Store.DonePath` (Task 1).
- Produces: `func composePrompt(b store.Binding, planPath, reportPath, donePath string) string`. `nudgePrompt` now takes two `%s`: report path, then marker path (used by Task 5).

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/send_test.go`:

```go
// TestComposePromptNamesPlanReportAndMarkerInOrder pins the handoff contract:
// the builder is told the plan, the report and the completion marker, in that
// order, and told the marker is its last action (spec §3.3).
func TestComposePromptNamesPlanReportAndMarkerInOrder(t *testing.T) {
	b := store.Binding{Name: "webshop", Round: 3}
	got := composePrompt(b, "/s/003-plan.md", "/s/003-report.md", "/s/003-done")

	plan := strings.Index(got, "/s/003-plan.md")
	report := strings.Index(got, "/s/003-report.md")
	done := strings.Index(got, "/s/003-done")
	if plan < 0 || report < 0 || done < 0 || !(plan < report && report < done) {
		t.Fatalf("paths must appear plan < report < marker, got:\n%s", got)
	}
	if !strings.HasPrefix(got, "Round 3 from the planner.") {
		t.Errorf("prompt must open with the round, got:\n%s", got)
	}
	if !strings.Contains(got, "as the very last thing you do") {
		t.Errorf("prompt must say the marker is the last action, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "Reply here with only the report path.") {
		t.Errorf("prompt must end with the reply instruction, got:\n%s", got)
	}
}
```

`store` and `strings` are already imported in `send_test.go`; if not, add them.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/relay -run TestComposePromptNamesPlanReportAndMarkerInOrder`
Expected: compile error, too many arguments to `composePrompt`.

- [ ] **Step 3: Change the prompt and its callers**

In `internal/relay/send.go`, replace the `builderPrompt` constant (keep its doc comment, add one sentence to it):

```go
// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relay reads off the terminal. The marker is the
// builder's own "the tree is final": relay closes the round on it, not on the
// report appearing (spec 2026-09-12-completion-marker §1).
const builderPrompt = `Round %d from the planner.
Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
Reply here with only the report path.`
```

Replace `composePrompt`:

```go
// composePrompt renders the builder prompt for this round. Nothing is
// interpolated except the round number and the three paths.
func composePrompt(b store.Binding, planPath, reportPath, donePath string) string {
	return fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath, donePath)
}
```

In `Send` (around line 131), after `reportPath := rt.Store.ReportPath(name, b.Round)` add
`donePath := rt.Store.DonePath(name, b.Round)` and change the call to
`composePrompt(b, planPath, reportPath, donePath)`.

In `internal/relay/switch.go:138`, change the call to:

```go
	text := composePrompt(b, rt.Store.PlanPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round))
```

In `internal/relay/reconcile.go`, replace `nudgePrompt`:

```go
// nudgePrompt is the one reminder relay sends when a builder went idle without
// finishing. It names both files: the report and the completion marker.
const nudgePrompt = `You went idle without finishing.
Write your report to %s if you have not, then create the empty file %s as
your last action, and reply with only the report path.`
```

Do **not** change `nudgeBuilder` yet (Task 5 does); until then it still calls
`fmt.Sprintf(nudgePrompt, reportPath)` with one argument. `go vet` flags that
as a `printf` mismatch, so in this task change `nudgeBuilder`'s one line to
pass the marker too:

```go
	if err := promptWithRetry(ctx, rt, Target(b.Builder), fmt.Sprintf(nudgePrompt, reportPath, rt.Store.DonePath(b.Name, b.Round))); err != nil {
```

In `internal/relay/headless_test.go:196-198`, the existing expectation becomes:

```go
	reportPath := rt.Store.ReportPath("webshop", 1)
	donePath := rt.Store.DonePath("webshop", 1)
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(b, planPath, reportPath, donePath)
```

- [ ] **Step 4: Run the package tests**

Run: `go vet ./internal/relay && go test -count=1 ./internal/relay`
Expected: PASS, including `TestComposePromptNamesPlanReportAndMarkerInOrder` and `TestSendHeadlessStartsTheProcessInsteadOfPrompting`.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/send.go internal/relay/switch.go internal/relay/reconcile.go internal/relay/send_test.go internal/relay/headless_test.go
git commit -m "feat(relay): builder and nudge prompts name the completion marker (#114)"
```

---

### Task 3: `closeOnMarker`

**Files:**
- Create: nothing. Add to `internal/relay/reconcile.go`, directly above `handleIdleBuilder` (line 330).
- Test: `internal/relay/reconcile_test.go` (three new tests appended)

**Interfaces:**
- Consumes: `queueReport(ctx, rt, tx, b, entries, path, payload, note string) (store.Binding, error)` (existing), `Store.DonePath`, `Store.ReportPath`.
- Produces:
  ```go
  func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (next store.Binding, closed bool, err error)
  ```
  Precondition: the round is open. Postconditions per §4.1.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/reconcile_test.go`:

```go
// closeOnMarkerUnderLock calls closeOnMarker the way Reconcile does: inside
// the store lock, with the binding's current log.
func closeOnMarkerUnderLock(t *testing.T, rt Runtime, b store.Binding) (store.Binding, bool) {
	t.Helper()
	var out store.Binding
	var closed bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		out, closed, err = closeOnMarker(context.Background(), rt, tx, b, entries)
		return err
	})
	if err != nil {
		t.Fatalf("closeOnMarker: %v", err)
	}
	return out, closed
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

func TestCloseOnMarkerWithReportClosesNormally(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty on a marked close", pending.Note)
	}
	if !strings.Contains(pending.Payload, "Builder finished round 1. Report: "+rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	if len(f.reads) != 0 {
		t.Errorf("a marked close never reads the terminal: %+v", f.reads)
	}
}

// TestCloseOnMarkerWithoutReportIsNoreport pins §4.1: the builder said it was
// done, so relay closes on that and says the report is missing, instead of
// waiting for idle and scraping a worse artefact. Mutation: fall through to
// scrapeReport -> f.reads is non-empty and the note is "scraped".
func TestCloseOnMarkerWithoutReportIsNoreport(t *testing.T) {
	f := &fakeHerdr{readOut: "some terminal text"}
	rt, b := sentBinding(t, f)
	touch(t, rt.Store.DonePath("webshop", 1))

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("entry must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "noreport" {
		t.Errorf("note = %q, want noreport", pending.Note)
	}
	want := "Builder wrote its completion marker for round 1 but no report at " + rt.Store.ReportPath("webshop", 1) + "."
	if !strings.Contains(pending.Payload, want) {
		t.Errorf("payload = %q, want it to contain %q", pending.Payload, want)
	}
	if len(f.reads) != 0 {
		t.Errorf("no scrape on a marker-only close: %+v", f.reads)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err == nil {
		t.Error("relay must not write a report file of its own on a noreport close")
	}
}

func TestCloseOnMarkerAbsentDoesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if closed || got.Round != 1 {
		t.Fatalf("closed=%v round=%d, want untouched: a report alone is not a close", closed, got.Round)
	}
	after, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("log grew from %d to %d entries; no marker means no I/O", len(before), len(after))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestCloseOnMarker'`
Expected: compile error, `undefined: closeOnMarker`.

- [ ] **Step 3: Implement**

In `internal/relay/reconcile.go`, above `handleIdleBuilder`:

```go
// closeOnMarker closes an open round when the builder's completion marker
// (Store.DonePath) exists. It is the one place both the pane and the headless
// path decide "the builder says it is finished", so they cannot disagree.
//
// Preconditions: the round is open -- a plan was sent for b.Round and no
// report has been queued for it.
// Postconditions:
//   - marker absent: closed is false, b is returned unchanged, nothing written.
//   - marker and report present: the round closes normally (note "").
//   - marker present, report absent: the round closes with note "noreport" and
//     a payload saying so. The terminal is never read: the builder said it
//     was done, and a scrape would be a worse artefact than an honest gap.
//
// Errors are queueReport's, wrapped; the round stays open and the next tick
// retries, since the marker is still on disk.
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (store.Binding, bool, error) {
	if _, err := os.Stat(rt.Store.DonePath(b.Name, b.Round)); err != nil {
		return b, false, nil
	}
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath), "")
		if err != nil {
			return b, false, fmt.Errorf("close round on marker: %w", err)
		}
		return next, true, nil
	}
	slog.Warn("round closed by marker without a report", "binding", b.Name, "round", b.Round, "note", "noreport")
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("Builder wrote its completion marker for round %d but no report at %s.", b.Round, reportPath), "noreport")
	if err != nil {
		return b, false, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, nil
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go
git commit -m "feat(relay): closeOnMarker closes an open round on NNN-done (#114)"
```

---

### Task 4: Pane path checks the marker every tick

**Files:**
- Modify: `internal/relay/reconcile.go:212-222` (`Reconcile`, between `entries, err := tx.ReadLog(b.Name)` and `switch effectiveStatus(...)`)
- Modify: `internal/relay/reconcile_test.go:82-120` (`TestReconcileQueuesReportWhenBuilderIdleAndFileExists`), `:511-539` (`TestReconcileQueuesReportInsideStartGrace`)
- Test: `internal/relay/reconcile_test.go` (one new test appended)

**Interfaces:**
- Consumes: `closeOnMarker` (Task 3).
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/reconcile_test.go`:

```go
// TestReconcileClosesOnMarkerWhileBuilderStillWorking pins §4.2: the marker
// is checked every tick, before herdr's status is consulted. Mutation: move
// the closeOnMarker call inside the idle branch -> this fails because the
// builder is reported working.
func TestReconcileClosesOnMarkerWhileBuilderStillWorking(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusWorking)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: the marker closes the round regardless of status", got.Round)
	}
	if len(f.prompts) != 0 {
		t.Errorf("no nudge on a marked round: %+v", f.prompts)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/relay -run TestReconcileClosesOnMarkerWhileBuilderStillWorking`
Expected: FAIL with `round = 1, want 2` (a working builder goes to `checkRoundTimeout`, nothing closes).

- [ ] **Step 3: Hoist the check**

In `Reconcile` (`internal/relay/reconcile.go`), after

```go
	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
```

and before `var next store.Binding` / `switch effectiveStatus(...)`, insert:

```go
	// The builder's completion marker closes the round on any tick, whatever
	// herdr says the pane is doing: the marker is written last, so what is
	// left of the builder's turn is just its reply (spec §4.2). Idle status
	// matters only on the fallback path below, when there is no marker.
	if HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport) {
		next, closed, err := closeOnMarker(ctx, rt, tx, b, entries)
		if err != nil {
			return b, err
		}
		if closed {
			return deliverAndSettle(ctx, rt, tx, next, agents)
		}
	}
```

- [ ] **Step 4: Update the two tests that closed on the report alone**

Both tests below currently write only the report and expect a close. After
this task the marker is what closes a round; the report-only case gets its own
behaviour in Task 5. Add the marker to each so they pin the intended contract.

`TestReconcileQueuesReportWhenBuilderIdleAndFileExists` (line 82): rename to
`TestReconcileQueuesReportWhenBuilderIdleAndMarkerExists`, and after the
`os.WriteFile(rt.Store.ReportPath(...))` block add
`touch(t, rt.Store.DonePath("webshop", 1))`. Everything else stays.

`TestReconcileQueuesReportInsideStartGrace` (line 511): after the
`os.WriteFile(rt.Store.ReportPath(...))` block add
`touch(t, rt.Store.DonePath("webshop", 1))`. Change the message
`"expected zero prompts when report file is present, got %+v"` to
`"expected zero prompts when the marker is present, got %+v"`.

- [ ] **Step 5: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS. (`handleIdleBuilder` still has its own report gate until Task 5; that is fine here, both gates agree when marker and report are both present.)

- [ ] **Step 6: Commit**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go
git commit -m "feat(relay): pane path closes the round on the marker every tick (#114)"
```

---

### Task 5: Idle fallback closes `unmarked` or scrapes

**Files:**
- Modify: `internal/relay/reconcile.go` — `handleIdleBuilder` (line ~330), `nudgeBuilder` (line ~433)
- Test: `internal/relay/reconcile_test.go` (three new tests appended; one existing assertion extended)

**Interfaces:**
- Consumes: `builderQuiescent`, `scrapeReport`, `queueReport`, `nudgeTime` (all existing), `nudgePrompt` (Task 2).
- Produces: `nudgeBuilder(ctx, rt, tx, b, reportPath, donePath string)`. `handleIdleBuilder` no longer closes on the report file; after quiescence it closes `unmarked` when a report exists, else scrapes.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/reconcile_test.go`:

```go
// TestReconcileReportWithoutMarkerIsNotAClose pins §4.3: a report on disk is
// not evidence the builder is finished. Mutation: gate on the report instead
// of the marker -> the round advances on the first tick.
func TestReconcileReportWithoutMarkerIsNotAClose(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("half-written"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	got, err := reconcile(t, rt, b, agents)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("round = %d, want 1: a report without a marker must not close the round", got.Round)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing may be queued before the marker or the fallback")
	}
	// Idle past the start grace with no marker: the one nudge, naming both files.
	if len(f.prompts) != 1 {
		t.Fatalf("want exactly one nudge, got %+v", f.prompts)
	}
	if !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) ||
		!strings.Contains(f.prompts[0].Text, rt.Store.DonePath("webshop", 1)) {
		t.Errorf("nudge must name the report and the marker, got %q", f.prompts[0].Text)
	}
}

// TestReconcileQuiescentWithReportClosesUnmarked pins the fallback: after the
// nudge and a still screen, the report that exists is delivered with the
// omission named, never scraped over. Mutation: drop the "unmarked" note ->
// fails; scrape instead of queueing the report -> the body check fails.
func TestReconcileQuiescentWithReportClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{readOut: "still screen"}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("the builder's own words"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	got, err := reconcile(t, rt, b, agents) // quiescent
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2 after the unmarked close", got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "never confirmed completion (no 001-done)") ||
		!strings.Contains(pending.Payload, "The diff may be premature.") ||
		!strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	body, err := os.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the builder's own words" {
		t.Errorf("the builder's report must be delivered as written, got %q", body)
	}
}

// TestReconcileQuiescentWithoutReportStillScrapes is the regression pin for
// the scrape path: no report and no marker after quiescence is exactly what
// it was before the marker existed.
func TestReconcileQuiescentWithoutReportStillScrapes(t *testing.T) {
	f := &fakeHerdr{readOut: "I did the thing but wrote nothing."}
	rt, b := sentBinding(t, f)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.RoundStartedAt = rt.Now().Add(-startGrace - time.Second)
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}

	b, err := reconcile(t, rt, b, agents) // nudge
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	clock.Advance(nudgeGrace + time.Second)
	got, err := reconcile(t, rt, b, agents) // scrape
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || got.Round != 2 {
		t.Fatalf("scrape must close the round: round=%d found=%v err=%v", got.Round, found, err)
	}
	if pending.Note != "scraped" {
		t.Errorf("note = %q, want scraped", pending.Note)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestReconcileReportWithoutMarkerIsNotAClose|TestReconcileQuiescentWithReportClosesUnmarked|TestReconcileQuiescentWithoutReportStillScrapes'`
Expected: the first two FAIL (`round = 2, want 1`; then the second's first tick closes on the report, so `note = "", want unmarked`). The third PASSES already; keep it as the pin.

- [ ] **Step 3: Rewrite the idle path**

In `internal/relay/reconcile.go`, `handleIdleBuilder`: delete the block

```go
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		payload := fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)
		return queueReport(ctx, rt, tx, b, entries, reportPath, payload, "")
	}
```

and replace it with:

```go
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	donePath := rt.Store.DonePath(b.Name, b.Round)
```

Change the nudge call in the same function from
`return nudgeBuilder(ctx, rt, tx, b, reportPath)` to
`return nudgeBuilder(ctx, rt, tx, b, reportPath, donePath)`.

Replace the tail of the function (from `// Once per round: the report scrapeReport queues ends this path.` to the closing `return scrapeReport(...)`) with:

```go
	// Once per round: whichever close runs below ends this path. The builder
	// never wrote its marker, so relay cannot know the tree is final; it
	// delivers the best artefact it has and names the omission (spec §4.3).
	quiet := rt.Now().UTC().Sub(next.BuilderScreenAt).Truncate(time.Second)
	if _, err := os.Stat(reportPath); err == nil {
		slog.Warn("builder quiescent with a report but no marker", "binding", next.Name, "round", next.Round, "quiet", quiet, "note", "unmarked")
		payload := fmt.Sprintf(
			"Builder finished round %d but never confirmed completion (no %s). Report: %s. The diff may be premature.",
			next.Round, filepath.Base(donePath), reportPath)
		return queueReport(ctx, rt, tx, next, entries, reportPath, payload, "unmarked")
	}
	slog.Info("builder quiescent, scraping report", "binding", next.Name, "round", next.Round, "quiet", quiet)
	return scrapeReport(ctx, rt, tx, next, entries, reportPath)
```

Add `"path/filepath"` to the file's imports if it is not there.

Change `nudgeBuilder`'s signature and its one prompt line:

```go
func nudgeBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reportPath, donePath string) (store.Binding, error) {
	if err := promptWithRetry(ctx, rt, Target(b.Builder), fmt.Sprintf(nudgePrompt, reportPath, donePath)); err != nil {
```

(The rest of `nudgeBuilder` is unchanged.) Update its doc comment, if it has
one, to say the nudge names the report and the marker.

- [ ] **Step 4: Extend one existing assertion**

`TestReconcileNudgesOnceWhenReportFileMissing` (line 122) checks the nudge
names the report path. Extend the condition so it also requires the marker:

```go
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0].Text, rt.Store.ReportPath("webshop", 1)) ||
		!strings.Contains(f.prompts[0].Text, rt.Store.DonePath("webshop", 1)) {
		t.Fatalf("expected one nudge naming the report and marker paths, got %+v", f.prompts)
	}
```

- [ ] **Step 5: Run the package tests**

Run: `go vet ./internal/relay && go test -count=1 ./internal/relay`
Expected: PASS. If `TestReconcileScrapesAfterNudgeFails` or the quiescence
tests fail, the scrape branch was altered: they must pass unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/reconcile.go internal/relay/reconcile_test.go
git commit -m "feat(relay): idle fallback closes unmarked with the report, scrapes without (#114)"
```

---

### Task 6: Headless path

**Files:**
- Modify: `internal/relay/headless.go:200-210` (report gate), `:252-268` (exit branch)
- Modify: `internal/relay/headless_test.go:487-532` (two existing tests)
- Test: `internal/relay/headless_test.go` (three new tests appended)

**Interfaces:**
- Consumes: `closeOnMarker` (Task 3), `clearProcess`, `exitEntry`, `haltBinding`, `switchBuilder` (existing).
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/headless_test.go`:

```go
// TestReconcileHeadlessAliveWithReportButNoMarkerWaits is the headless half
// of the fix: a running process that has written a report is still running.
// Mutation: stat the report instead of the marker -> round 2, PID cleared.
func TestReconcileHeadlessAliveWithReportButNoMarkerWaits(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || got.Builder.PID != b.Builder.PID {
		t.Errorf("round=%d pid=%d, want round 1 and the same pid: the process is still running", got.Round, got.Builder.PID)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing is queued while the process runs without a marker")
	}
	if len(fr.kills) != 0 || len(exits(t, rt)) != 0 {
		t.Errorf("kills=%d exits=%d, want none", len(fr.kills), len(exits(t, rt)))
	}
}

// TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked: exit is a
// hard fact, so the report is trusted with the omission noted (spec §4.4).
func TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "exited (code 0) after writing its report but never confirmed completion (no 001-done)") ||
		!strings.Contains(pending.Payload, rt.Store.ReportPath("webshop", 1)) {
		t.Errorf("payload = %q", pending.Payload)
	}
	if len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("exits=%d specs=%d, want no exit entry and no switch: the report is the record", len(exits(t, rt)), len(fr.specs))
	}
}

// TestReconcileHeadlessMarkerClosesAndClearsTheHandle: the marker closes the
// round the same way for a process as for a pane, and the handle goes with it.
func TestReconcileHeadlessMarkerClosesAndClearsTheHandle(t *testing.T) {
	f := &fakeHerdr{}
	fr := newFakeRunner()
	rt, b := sentHeadless(t, f, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b, []herdr.Agent{plannerAgent()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	if !got.Builder.Headless() || got.Builder.AgentName != "webshop-builder" {
		t.Errorf("identity must survive: %+v", got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("a builder that wrote its marker is never killed: %+v", fr.kills)
	}
}
```

`touch` is defined in `reconcile_test.go` (Task 3); both files are package `relay`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestReconcileHeadlessAliveWithReportButNoMarkerWaits|TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked'`
Expected: both FAIL — the first with `round=2`, the second with `note = "", want unmarked`. (`TestReconcileHeadlessMarkerClosesAndClearsTheHandle` passes already via the report gate; it becomes meaningful after Step 3.)

- [ ] **Step 3: Swap the gate and add the exit case**

In `internal/relay/headless.go`, `reconcileHeadless`, replace

```go
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		// The report is the contract (spec §1): the process is done with
		// whatever it exits as, and it exits on its own. Never Kill here.
		payload := fmt.Sprintf("Builder finished round %d. Report: %s", b.Round, reportPath)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, "")
		if err != nil {
			return b, err
		}
		next.Builder = clearProcess(next.Builder)
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}
```

with

```go
	// The marker is the contract (completion-marker spec §4.4): the process
	// is done with whatever it exits as, and it exits on its own. Never Kill
	// here. A report without a marker is a process still working.
	next, closed, err := closeOnMarker(ctx, rt, tx, b, entries)
	if err != nil {
		return b, err
	}
	if closed {
		next.Builder = clearProcess(next.Builder)
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}
```

Then, in the exit branch, replace the comment `// Exited without a report.`
and the two lines that follow it (`codeText := "unknown"` and the `ExitCode`
lookup) with:

```go
	// Exited. The exit code is read once for both outcomes below.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), b.Builder.LogPath); ok {
		codeText = strconv.Itoa(code)
	}

	// Exited after writing a report but without the marker: an exited
	// process cannot be mid-write, so the report is trusted and the
	// omission noted (spec §4.4).
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if _, err := os.Stat(reportPath); err == nil {
		slog.Warn("headless builder exited with a report but no marker", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText, "note", "unmarked")
		payload := fmt.Sprintf(
			"Builder exited (code %s) after writing its report but never confirmed completion (no %s). Report: %s.",
			codeText, filepath.Base(rt.Store.DonePath(b.Name, b.Round)), reportPath)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, "unmarked")
		if err != nil {
			return b, err
		}
		next.Builder = clearProcess(next.Builder)
		return deliverAndSettle(ctx, rt, tx, next, agents)
	}

	// Exited without a report.
```

The existing `now := rt.Now().UTC()` / `exitEntry` / halt-or-switch code
follows unchanged. Add `"path/filepath"` to the imports if absent.

- [ ] **Step 4: Update the two existing tests that closed on the report alone**

`TestReconcileHeadlessReportFinishesTheRoundAndClearsTheHandle` (line 487):
delete it. `TestReconcileHeadlessMarkerClosesAndClearsTheHandle` (Step 1) is
its replacement with the marker.

`TestReconcileHeadlessReportWinsEvenIfTheProcessExitedNonZero` (line 520):
the scenario (exited 1, report present, no marker) is now the `unmarked`
close. Keep the test; replace its final `if` with:

```go
	pending, found, perr := rt.Store.PendingForPlanner("webshop")
	if err != nil || got.Round != 2 || len(exits(t, rt)) != 0 || len(fr.specs) != 1 || perr != nil || !found {
		t.Fatalf("round=%d exits=%d specs=%d err=%v found=%v; want the report to finish the round with no exit entry and no switch", got.Round, len(exits(t, rt)), len(fr.specs), err, found)
	}
	if pending.Note != "unmarked" || !strings.Contains(pending.Payload, "exited (code 1)") {
		t.Errorf("note=%q payload=%q, want an unmarked close naming the exit code", pending.Note, pending.Payload)
	}
```

- [ ] **Step 5: Run the package tests**

Run: `go vet ./internal/relay && go test -count=1 ./internal/relay`
Expected: PASS, including `TestReconcileHeadlessExitWithoutReportLogsAndSwitches`, `TestReconcileHeadlessExitUnknownCode`, `TestReconcileHeadlessExitHaltsAfterMaxSwitches` unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/relay/headless.go internal/relay/headless_test.go
git commit -m "feat(relay): headless round closes on the marker; exit with a report closes unmarked (#114)"
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md:157-167` (Quick start), `:295-306` (Headless builders)
- Modify: `docs/design.md:190-215` (the two handoff blocks)
- Modify: `docs/specs/2026-09-12-completion-marker-design.md:1-11` (header status)

**Interfaces:** none.

- [ ] **Step 1: README**

In "Quick start", after the `relay send --file plan.md` line's comment `# hand it the plan; the builder starts working`, add a sentence below the code block (before "`relay bind` reads the planner's pane"):

```
The builder writes `NNN-report.md` when it has finished and then creates an
empty `NNN-done` as its last action; relay closes the round on that marker.
A builder that goes idle without the marker is nudged once, then its report
is delivered flagged `unmarked` (or its terminal scraped if there is no
report at all).
```

In "Headless builders", replace the sentence
`The report file is the whole contract: a process that wrote its report and then exited non-zero has done its job.`
with:

```
The completion marker is the contract: a process that wrote its report and
created `NNN-done`, then exited non-zero, has done its job. A process that
exits with a report but no marker closes the round too, flagged `unmarked`;
one that exits with neither is the "exited without a report" case below.
```

- [ ] **Step 2: design.md**

In the "Planner → builder" block (line ~192), replace the four-line prompt with the spec's:

```
         Round N from the planner.
         Read:  <state>/NNN-plan.md
         When you are done, write your report to: <state>/NNN-report.md
         Then, as the very last thing you do -- after every edit, test and
         commit -- create this empty file: <state>/NNN-done
         Reply here with only the report path.
```

Replace the "Builder → planner" block (from `relayd observes builder -> idle|done` to `deliver(payload) -> planner`) with:

```
relayd, every tick while the round is open (pane or headless):
  if <state>/NNN-done exists:
      if NNN-report.md exists: payload = "Builder finished round N. Report: <path>"
      else:                    payload = "...marker but no report at <path>."   note noreport
      close the round

relayd observes builder -> idle|done, no marker:
  nudge once: "You went idle without finishing. Write your report to <path>
               if you have not, then create the empty file <done> as your
               last action, and reply with only the report path."
  wait for the screen to stay still for the nudge grace
  if NNN-report.md exists:
      payload = "Builder finished round N but never confirmed completion
                 (no NNN-done). Report: <path>. The diff may be premature."  note unmarked
  else:
      scrape = herdr agent read --source recent-unwrapped --lines 200
      write to NNN-report.md marked SCRAPED (may be truncated)
      payload = report + explicit unreliability warning                       note scraped

headless: a process that exited with a report but no marker closes unmarked;
          with neither, "exited without a report" (see headless spec).

  deliver(payload) -> planner        # see delivery rule below
```

- [ ] **Step 3: Spec status**

In the spec header, after the `**Amends:**` paragraph, add a line:
`**Status:** implemented by \`docs/plans/2026-09-12-completion-marker.md\`.`

- [ ] **Step 4: Run the full check and commit**

Run: `make check` (or its constituents; see "Running commands").
Expected: green.

```bash
git add README.md docs/design.md docs/specs/2026-09-12-completion-marker-design.md
git commit -m "docs: the round closes on the builder's completion marker (#114)"
```

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD`, the `make check` output tail, and anything you
stopped on. Then create the empty `NNN-done` file the round prompt names, and
reply with only the report path.
