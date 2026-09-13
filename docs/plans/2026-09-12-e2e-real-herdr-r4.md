# E2E on real herdr, round 4: fresh binding under the lock, status-aware waitEcho, then Tasks 4-5 (#114 step 2)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-e2e-real-herdr-design.md`.
**Earlier plans:** `docs/plans/2026-09-12-e2e-real-herdr.md` (round 1),
`…-r2.md`, `…-r3.md` -- all in your worktree. Tasks 4 and 5 of this round are
executed from the round-1 file with round 3's `waitEcho` insertions.
**Issue:** #114, recommended step 2.

## Where you are

The worktree holds:

```
ce66280 test(e2e): shim flashes a working state so relay's --wait prompts land; real round closes on the marker (#114 step 2)
5c3ca8c test(e2e): detached herdr session fixture with scripted agents; smoke (#114 step 2)
```

plus **uncommitted** edits to `internal/relay/e2e_test.go`: round 1's Task 3
Step 1 (the two nudge subtests) and round 3's Task C Steps 1-2 (`waitEcho`,
the last-line constants, `prompt_last_lines`, and the inserted waits). All
correct as far as they go. Do not revert them.

Round 3 halted at Task C Step 3 with two root causes, both diagnosed in the
round-3 report and both confirmed by a diagnostic run that passed all five
subtests. The planner adopts that diagnosis unchanged:

1. **`reconcileAt` reconciled a stale binding.** `Send` stamps
   `RoundStartedAt` into the store, but the subtest's local `b` came from
   `bindBuilder` (before `Send`) with a zero `RoundStartedAt`, and
   `tx.Save(out)` wrote that zero back over the store. `handleIdleBuilder`
   then refused to nudge ("elapsed time is unknowable"). `daemon.go` loads
   the binding fresh under the lock before `Reconcile`; the helper must do
   the same.
2. **herdr's agent status lags the screen.** The shim's last echo line is
   visible ~600ms before `agent list` stops reporting `working`. `waitEcho`
   returned on the screen alone, so the next tick saw `working` and took the
   timeout branch. `waitEcho` must wait for both, which is what its doc
   comment already promises.

Task D applies exactly the two changes from the round-3 report's "Verified
Solution". Nothing else in the file changes.

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/e2e-herdr` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/e2e-herdr`. |
| `~/.local/state/relay/e2e-herdr` | relay's drop directory. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands, herdr use, global constraints

As in rounds 1 and 3: `make check` and `go vet -tags e2e ./internal/relay`
at the end of every task; `go test -tags e2e -count=1 -run TestE2E
./internal/relay -v` (and `make e2e` once Task 5 adds it); herdr only through
the test; no `relay-e2e-*` session left behind; no production code changes;
one commit per task. Real-time bounds: 5s `waitScreen`, 15s `waitEcho`.

---

### Task D: `reconcileAt` loads fresh; `waitEcho` waits for status

**Files:**
- Modify: `internal/relay/e2e_test.go` (`reconcileAt`, `waitEcho`)

**Interfaces:**
- Consumes: `(*store.Tx).Load(name string) (store.Binding, error)` -- as `daemon.go` uses it; `herdr.StatusWorking`.
- Produces: the same two helpers with the semantics below.

- [ ] **Step 1: `reconcileAt` reconciles the stored binding**

Replace the body of `reconcileAt` so that, inside the lock, the binding is
loaded fresh before `Reconcile`; the caller's `b` only supplies the name:

```go
// reconcileAt sets the clock to baseTime+at and runs one Reconcile the way
// the daemon does: under the lock, on the binding as stored (Send and earlier
// ticks have written to it; the caller's copy may be stale), with a fresh
// real agent list (spec §3.5).
func reconcileAt(t *testing.T, s *e2eSession, rt Runtime, clock *fakeClock, at time.Duration, b store.Binding) store.Binding {
	t.Helper()
	clock.now = baseTime.Add(at)
	agents := s.agents(t)
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		fresh, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		out, err = Reconcile(context.Background(), rt, tx, fresh, agents)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("Reconcile at T+%s: %v", at, err)
	}
	return out
}
```

If `Load` on a `*store.Tx` is spelled differently, use the call `daemon.go`
makes right before `Reconcile`; do not add a fallback that swallows the error.

- [ ] **Step 2: `waitEcho` waits until herdr no longer says `working`**

Replace the body of `waitEcho`:

```go
// waitEcho waits until target has echoed lastLine -- the shim has consumed
// the whole prompt -- AND herdr's agent list no longer reports it working.
// herdr's status lags the screen by a few hundred milliseconds; a tick taken
// in that window sees `working` and never reaches the idle path.
func (s *e2eSession) waitEcho(t *testing.T, target, lastLine string) {
	t.Helper()
	want := "you said: " + lastLine
	deadline := time.Now().Add(e2eEchoTimeout)
	var last, status string
	for time.Now().Before(deadline) {
		last = s.screen(t, target)
		if strings.Contains(last, want) {
			status = ""
			for _, a := range s.agents(t) {
				if a.PaneID == target {
					status = a.Status
				}
			}
			if status != "" && status != herdr.StatusWorking {
				return
			}
		}
		time.Sleep(e2ePoll)
	}
	t.Fatalf("target %s: echo of %q seen=%v, last status %q, within %s; last screen:\n%s",
		target, want, strings.Contains(last, want), status, e2eEchoTimeout, last)
}
```

- [ ] **Step 3: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: `smoke`, `prompt_last_lines`, `marker_closes_round`,
`idle_without_marker_nudges_once`, `still_screen_closes_unmarked` all PASS
(the round-3 diagnostic run took ~31s). No `relay-e2e-*` session left.

- [ ] **Step 4: Mutations (round 3's Task C Step 4, plus one for each fix)**

1. `idle_without_marker_nudges_once`: nudge tick at `20*time.Second` (adjust the `BuilderScreenAt` expectation to match). Expected: nudge never appears. Revert.
2. `still_screen_closes_unmarked`: final tick at `startGrace+6*time.Second+30*time.Second`. Expected: `round = 1, want 2`. Revert.
3. In `reconcileAt`, pass `b` instead of `fresh` to `Reconcile`. Expected: `idle_without_marker_nudges_once` fails -- the nudge never appears (zero `RoundStartedAt` written back). Revert.
4. In `waitEcho`, delete the `status` check so it returns on the screen alone. Expected: `idle_without_marker_nudges_once` fails at least intermittently -- run it three times and report how many failed. Revert.

- [ ] **Step 5: `make check`, then commit**

```bash
git add internal/relay/e2e_test.go
git commit -m "test(e2e): reconcile the stored binding; wait for herdr status; nudge once, then unmarked on a still real screen (#114 step 2)"
```

---

### Task 4: Scrape, and a moving screen resets the grace

Execute **Task 4 of `docs/plans/2026-09-12-e2e-real-herdr.md`**, Steps 1-4,
with round 3's insertions in both subtests as you add them:

- after each `s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")`:
  `s.waitEcho(t, b.Builder.PaneID, promptLastLine)`
- after each `s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")`:
  `s.waitEcho(t, b.Builder.PaneID, nudgeLastLine)`
- in `screen_movement_resets_grace`, replace
  `s.waitScreen(t, b.Builder.PaneID, "you said: keep talking")` with
  `s.waitEcho(t, b.Builder.PaneID, "keep talking")` -- same wait, plus the
  status condition, so the `moved` tick sees a builder that is no longer
  `working`.

- [ ] Task 4 Steps 1-4 done; commit
  `test(e2e): a still real screen scrapes; a moving one resets the nudge grace (#114 step 2)` exists.

---

### Task 5: `make e2e`, docs

Execute **Task 5 of `docs/plans/2026-09-12-e2e-real-herdr.md`** exactly as
written, Steps 1-4.

- [ ] Task 5 Steps 1-4 done; `make check && make e2e` green; no `relay-e2e-*` session left.

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD`, the tail of `make check` and of `make e2e`
(per-subtest PASS lines and total time), the observed result of each mutation
(including the count for mutation 4), and anything you stopped on. Confirm
`herdr session list` shows no `relay-e2e-*` session. Then create the empty
`NNN-done` file the round prompt names, and reply with only the report path.
