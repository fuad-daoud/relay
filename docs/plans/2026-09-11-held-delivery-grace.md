# A held payload is delivered on an empty input or a quiet screen (#69)

**Design spec:** `docs/specs/2026-09-11-held-delivery-grace-design.md`
**Issue:** #69

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

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

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- **No test may execute a `cmd/relay` subcommand that reaches herdr.** CI
  runners have no `herdr` binary. Every test in this plan is in
  `internal/relay` against `fakeHerdr`. The `--held-grace` flag is not
  tested in `cmd/relay`; its effect is tested through `Runtime.HeldGrace`.
- The not-focused delivery path must behave exactly as today. Every
  existing test in `deliver_test.go` that does not set `Focused` must pass
  with only the return-shape change.
- `PlannerScreen != ""` if and only if the last `DeliverPending` for the
  binding returned `Held` via the fingerprint path (spec §3.1). Every
  non-hold return clears it.
- A failed screen read neither injects nor touches the fingerprint (spec
  §7.4).
- Read the planner with source `"visible"` and `heldScreenLines` lines,
  addressed by `planner.PaneID` (the agent `FindAgent` located, the same
  target `promptWithRetry` uses).
- Every new exported symbol gets a doc comment explaining the rationale, in
  the house style; the unexported ones in `held.go` too -- the spec gives
  the text.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: replace the focus-only hold with a two-signal hold

**Files:**
- Modify: `internal/store/store.go` (the `Binding` struct)
- Modify: `internal/relay/herdr.go` (the `Runtime` struct)
- Create: `internal/relay/held.go`, `internal/relay/held_test.go`
- Modify: `internal/relay/reconcile.go` (`screenFingerprint`, `deliverAndSettle`)
- Modify: `internal/relay/deliver.go`, `internal/relay/deliver_test.go`
- Modify: `cmd/relay/main.go` (`cmdDaemon`)
- Modify: `README.md`, `docs/design.md`

**Interfaces produced** (spec §3, §4):
- `store.Binding.PlannerScreen string`, `store.Binding.PlannerScreenAt time.Time`
- `relay.Runtime.HeldGrace time.Duration`
- `relay.DefaultHeldGrace = 60 * time.Second`; `heldScreenLines = 40`
- `inputEmpty(kind, screen string) (empty, known bool)`
- `fingerprint(text string) string`
- `plannerHold(ctx, rt, b store.Binding, planner herdr.Agent) (store.Binding, bool, string)`
- `clearPlannerScreen(b store.Binding) store.Binding`
- `DeliverPending(...) (store.Binding, Delivery, error)` -- **signature change**

- [ ] **Step 1: state and config fields** (spec §3.1, §3.2)

  In `internal/store/store.go`, directly after `BuilderScreenAt`, add
  `PlannerScreen string` (`json:"planner_screen,omitempty"`) and
  `PlannerScreenAt time.Time` (`json:"planner_screen_at,omitempty"`) with the
  comment from spec §3.1 verbatim.

  In `internal/relay/herdr.go`, add `HeldGrace time.Duration` to `Runtime`
  after `Hooks`, with the comment from spec §3.2.

  Verify: `go build ./...` clean. No test changes.

- [ ] **Step 2: the detector and the fingerprint, test first** (spec §4.1, §4.2, §8 items 1-2)

  Create `internal/relay/held_test.go` with:

  - `TestInputEmpty`, a table over `(kind, screen) → (empty, known)`.
    Screens are multi-line strings; write them the way herdr renders a
    claude pane, roughly:

    ```
    some transcript
    > a quoted line in the transcript
    ────────────────
    ❯
    ────────────────
      ? for shortcuts
    ```

    Cases, exactly as spec §8 item 1 lists them: bare `❯` → `(true,true)`;
    `❯ draft` → `(false,true)`; the quoted-`>` transcript line above a bare
    `❯` → `(true,true)` (this is the one that pins "scan from the bottom");
    older `>` marker bare → `(true,true)`; no marker → `(false,false)`;
    kinds `agy`, `opencode`, `""` with a bare `❯` → `known == false`.
  - `TestFingerprintIsStable`: `fingerprint("a") == fingerprint("a")` and
    `!= fingerprint("b")`.

  Run: `go test ./internal/relay/ -run 'InputEmpty|Fingerprint'`. Expected:
  compile failure (symbols undefined).

  Create `internal/relay/held.go` with `DefaultHeldGrace`, `heldScreenLines`,
  `inputEmpty`, `fingerprint` (sha256 hex of the text -- move the two hashing
  lines out of `screenFingerprint` in `reconcile.go` and have it call
  `fingerprint(text)`), and `clearPlannerScreen`. Comments per spec §3.4,
  §4.1, §4.2, §4.4.

  `inputEmpty` for `claude`: `strings.Split(screen, "\n")`, iterate from the
  last index down, `strings.TrimLeft(line, " \t")`, check
  `strings.HasPrefix` for `"❯"` then `">"`; on the first hit return
  `strings.TrimSpace(rest) == "", true`. No hit → `false, false`. Any other
  kind → `false, false` before scanning.

  Run the two tests. Expected: PASS. Run `go test ./internal/relay/` to
  confirm the `screenFingerprint` refactor broke nothing.

- [ ] **Step 3: `plannerHold`** (spec §4.3, §5)

  Add to `held.go`:

  ```
  func plannerHold(ctx context.Context, rt Runtime, b store.Binding, planner herdr.Agent) (store.Binding, bool, string)
  ```

  Body follows spec §5 `plannerHold` line for line. `grace := rt.HeldGrace;
  if grace <= 0 { grace = DefaultHeldGrace }`. The five reason strings are
  in spec §3.3; build the two parameterised ones with `fmt.Sprintf`, the
  quiet one as `"planner pane is focused; quiet %s of %s"` with
  `quiet.Truncate(time.Second)` and `grace`. The known-limitation note from
  spec §7.5 goes in the comment above the fingerprint step.

  No test of its own: it is exercised through `DeliverPending` in step 5.

  Verify: `go build ./...`.

- [ ] **Step 4: the signature change** (spec §4.5, §4.6)

  Change `DeliverPending` to return `(store.Binding, Delivery, error)`.
  Every existing `return Delivery{...}, nil` becomes
  `return clearPlannerScreen(b), Delivery{...}, nil`; every
  `return Delivery{}, err` becomes `return b, Delivery{}, err`.

  Update `deliverAndSettle` in `reconcile.go`:

  ```
  next, got, err := DeliverPending(ctx, rt, tx, b, agents)
  if err != nil {
      return b, err
  }
  b = next
  ```

  then the existing `switch` unchanged.

  Update the `deliverPending` helper in `deliver_test.go` to return
  `(store.Binding, Delivery, error)` and fix every caller in that file to
  take three values (existing tests discard the binding with `_` for now).

  Run: `go test ./internal/relay/`. Expected: PASS -- behaviour is
  unchanged so far, only the shape.

- [ ] **Step 5: the two-signal branch, test first** (spec §4.5, §8 items 3-12)

  In `deliver_test.go`, first add two screen fixtures next to `plannerWith`:

  ```
  const claudeIdleScreen = "transcript\n────\n❯\n────\n  ? for shortcuts\n"
  const claudeDraftScreen = "transcript\n────\n❯ half a thought\n────\n  ? for shortcuts\n"
  ```

  and a clock helper that replaces `rt.Now` with a closure over a `now`
  variable the test advances (`now := baseTime; rt.Now = func() time.Time { return now }`).

  Every multi-tick test passes the binding returned by the previous tick
  into the next one, with `State = store.StateHeld` set after the first
  hold -- that is exactly what the daemon persists between ticks, and a
  test that re-uses the original binding would restart the grace clock
  every tick and prove nothing.

  Fix the two existing focused tests: `TestDeliverHoldsWhilePlannerPaneFocused`
  and `TestDeliverNotifiesOnlyOnceWhileHeld` set `f.readOut = claudeDraftScreen`
  before their first tick. Leave their assertions alone.

  Add, in this order, each a separate `Test…` function named as written:

  - `TestHeldDeliversWhenPlannerInputEmpty` (item 3): focused idle claude
    planner, `f.readOut = claudeIdleScreen` → `Delivered`,
    `Reason == "planner focused, input empty"`, one prompt, and the last
    entry of `f.reads` has `Source == "visible"` and `Lines == heldScreenLines`.
  - `TestHeldFingerprintsADraft` (item 4): `claudeDraftScreen` → `Held`,
    returned binding has `PlannerScreen == fingerprint(claudeDraftScreen)`
    and `PlannerScreenAt.Equal(now)`, no prompts, one notice.
  - `TestHeldDeliversAfterQuietGrace` (item 5): tick once with the draft
    (held), pass the returned binding with `State = store.StateHeld` back,
    advance `now` by `DefaultHeldGrace`, tick again → `Delivered`,
    `Reason == "planner focused, quiet for 1m0s"`, returned binding has
    `PlannerScreen == ""` and `PlannerScreenAt.IsZero()`.
  - `TestHeldResetsWhenScreenMoves` (item 6): tick with the draft; advance
    `now` by 30s; set `f.readOut` to the draft with one more word; tick →
    `Held`, `PlannerScreenAt.Equal(now)` (the later instant),
    `PlannerScreen == fingerprint(<new screen>)`; advance another 30s with
    the same screen → still `Held` (only 30s quiet since the change).
  - `TestHeldUnknownKindUsesGraceOnly` (item 7): planner `Kind = "agy"`,
    `f.readOut = claudeIdleScreen` (bare `❯`, which a known kind would
    deliver on) → first tick `Held`; advance by `DefaultHeldGrace` →
    `Delivered` with the quiet reason.
  - `TestHeldReadFailureHoldsWithoutEvidence` (item 8): seed the input
    binding with `PlannerScreen = "keep"`, `PlannerScreenAt = baseTime`;
    `f.readErr = errors.New("boom")` → `err == nil`, `Held`, returned
    binding still has `PlannerScreen == "keep"` and the same
    `PlannerScreenAt`; `Reason == "planner pane is focused; screen unreadable"`.
  - `TestHeldNotifiesOnceAcrossThreeTicks` (item 9): draft screen, three
    ticks 10s apart, passing the returned binding (with `State = Held`)
    forward each time → `len(f.notices) == 1`.
  - `TestEmptyClearsPlannerScreen` (item 10): use `seedBound` (nothing
    queued), binding seeded with `PlannerScreen = "stale"`, planner focused
    idle → `Empty`, both fields zero on the returned binding.
  - `TestUnfocusedDeliveryClearsPlannerScreen` (item 11): queued binding
    seeded with `PlannerScreen = "stale"`, planner idle and **not** focused
    → `Delivered`, both fields zero, and `len(f.reads) == 0` (the unfocused
    path never reads the screen).
  - `TestHeldGraceZeroMeansDefault` (item 12): `rt.HeldGrace = 0`; draft
    screen; tick; advance 59s, tick → `Held`; advance 1s more, tick →
    `Delivered`. Then a second run with `rt.HeldGrace = 5 * time.Second`
    delivering after 5s, so the field is proven to be read.

  Run: `go test ./internal/relay/ -run 'Held|ClearsPlannerScreen'`.
  Expected: FAIL (the focused branch still holds unconditionally).

  Now replace the `if planner.Focused { … }` block in `DeliverPending`
  with spec §5's `DeliverPending` pseudocode: call `plannerHold`, hold
  (with the once-per-transition notify, unchanged) when it says hold,
  otherwise fall through to the prompt with its reason. The final
  `Delivered` return is `clearPlannerScreen(b), Delivery{Delivered: true, Reason: reason}, nil`
  where `reason` is `""` on the unfocused path. Rewrite the function's
  doc comment: the "Focus is the only proxy available" sentence is no
  longer true; say what the two signals are and point at `plannerHold`.

  Run: `go test ./internal/relay/`. Expected: PASS, all of it.

- [ ] **Step 6: the daemon flag and the docs** (spec §4.7)

  In `cmd/relay/main.go` `cmdDaemon`, after `interval`:

  ```
  heldGrace := fs.Duration("held-grace", relay.DefaultHeldGrace,
      "how long a focused planner must be quiet before a held payload is injected anyway")
  ```

  and after `rt, err := newRuntime()` succeeds: `rt.HeldGrace = *heldGrace`.
  Add `"held_grace", *heldGrace` to the `relay daemon starting` slog line.

  `README.md`:
  - In the command list, `relay daemon [--interval D]` becomes
    `relay daemon [--interval D] [--held-grace D]` with a clause: "a held
    payload is injected into a focused planner once its input box is empty
    or its screen has been quiet for `--held-grace` (default 60s)".
  - Rewrite "The anti-clobber rule" section: keep the first two sentences
    (what `herdr agent prompt` does, why a focused pane is risky), then
    replace "As soon as the human's focus moves to another pane, the daemon
    delivers the held payload on its next tick." with the three ways out of
    a hold: focus leaves the pane; the planner's input box is seen empty
    (claude only today); the visible screen has not changed for
    `--held-grace`, in which case an abandoned draft gets the payload
    appended -- accepted on purpose. Keep the `relay pull` sentence.
  - The `--dangerously-skip-permissions` or other unrelated sections: do
    not touch.

  `docs/design.md`:
  - In the `deliver(payload)` pseudocode, the "two ways out of held" comment
    becomes four lines: focus leaves; input box empty; quiet for
    `held-grace`; `relay pull`.
  - The "Human camps in the planner pane" row: append "After `--held-grace`
    of screen quiet the daemon injects anyway."

  Verify: `go build ./... && relay daemon --help 2>&1 | grep held-grace`
  (the help text prints without herdr; that is not a herdr call).

- [ ] **Step 7: full check and mutations**

  `make check` (or constituents) green.

  Do each, run the named test, revert, record:
  1. In `plannerHold`, change `quiet >= grace` to `quiet > grace*2` (or
     delete the branch). Expect `TestHeldDeliversAfterQuietGrace` to fail.
  2. Delete the `known && empty` short circuit in `plannerHold`. Expect
     `TestHeldDeliversWhenPlannerInputEmpty` to fail on `Reason` or on
     `Delivered`.
  3. On the `Empty` return in `DeliverPending`, return `b` instead of
     `clearPlannerScreen(b)`. Expect `TestEmptyClearsPlannerScreen` to fail.
  4. In `inputEmpty`, scan from the top instead of the bottom. Expect the
     quoted-`>` case of `TestInputEmpty` to fail.

- [ ] **Step 8: commit**

  One commit. Subject:
  `feat(relay): deliver a held payload once the planner's input is empty or its screen is quiet (#69)`.
  Body: the two signals and their order, that the grace clock runs from the
  last screen change, that agy/opencode take the grace path only until
  their markers are captured, and the `--held-grace` flag. Do not push.

## Report

- `git diff --stat` against your base.
- The `make check` (or constituents) output tail.
- The mutation table, all four.
- Anything you stopped on, or any place the plan and the code disagreed.
