# E2E on real herdr, round 3: wait for the shim to finish echoing before ticking, then Tasks 3-5 (#114 step 2)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-e2e-real-herdr-design.md`.
**Earlier plans:** `docs/plans/2026-09-12-e2e-real-herdr.md` (round 1) and
`…-r2.md` (round 2) -- both in your worktree. Tasks 4 and 5 of this round are
executed from the round-1 file, with the two insertions Task C defines.
**Issue:** #114, recommended step 2.

## Where you are

The worktree holds:

```
ce66280 test(e2e): shim flashes a working state so relay's --wait prompts land; real round closes on the marker (#114 step 2)
5c3ca8c test(e2e): detached herdr session fixture with scripted agents; smoke (#114 step 2)
```

plus **uncommitted** edits to `internal/relay/e2e_test.go`: round 1's Task 3
Step 1 -- the `idle_without_marker_nudges_once` and
`still_screen_closes_unmarked` subtests, added exactly as written. Do not
revert them; Task C edits them.

Round 2 halted at Task 3 Step 2. Root cause (the builder's analysis is
correct): `builderPrompt` is six lines; the shim reads stdin one line at a
time and flashes its spinner for one real second per line, so for ~6s after
`Send` herdr reports the builder `working`. `waitScreen(builder, "Round 1
from the planner")` returns on the prompt's *first* line, ~50ms in, so the
`startGrace+5s` tick runs while the builder is still `working`, takes the
`checkRoundTimeout` branch, and never nudges. The same holds for the nudge
prompt (three lines, ~3s) before any later tick.

The fix is in the test only: before any tick that depends on the builder
being idle, wait for the shim's echo of the prompt's **last** line. That is
a real-time wait of up to ~6s, longer than `waitScreen`'s 5s bound, so it
gets its own helper with a 15s bound. The shim is unchanged; the fake clock is
unchanged; no sleeps are added.

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/e2e-herdr` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/e2e-herdr`. |
| `~/.local/state/relay/e2e-herdr` | relay's drop directory. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands, herdr use, global constraints

Exactly as in round 1's plan, with one amendment: the bound on a real-time
wait is 5s for `waitScreen` and **15s for `waitEcho`** (below). Everything
else stands: `make check` and `go vet -tags e2e ./internal/relay` at the end
of every task; herdr only through the test; no `relay-e2e-*` session left
behind; no production code changes; one commit per task.

---

### Task C: `waitEcho`, and the two subtests wait for it

**Files:**
- Modify: `internal/relay/e2e_test.go`

**Interfaces:**
- Consumes: `builderPrompt`, `nudgePrompt` (`send.go`, `reconcile.go`) -- read, not changed.
- Produces:
  - `const e2eEchoTimeout = 15 * time.Second`
  - `func (s *e2eSession) waitEcho(t *testing.T, target, lastLine string)` -- waits until the screen contains `"you said: " + lastLine`.
  - `const promptLastLine = "Reply here with only the report path."` -- the last line of `builderPrompt`.
  - `const nudgeLastLine = "your last action, and reply with only the report path."` -- the last line of `nudgePrompt`.

- [ ] **Step 1: Add the helper and the constants**

Next to the other `e2e*` constants:

```go
	// e2eEchoTimeout bounds waitEcho: the shim spends one real second per
	// prompt line, and builderPrompt is six lines.
	e2eEchoTimeout = 15 * time.Second
```

After `waitScreen`:

```go
// The shim echoes every line it is given, one real second apart, and herdr
// reports it working until the last one. A tick that expects an idle builder
// must first wait for the echo of the prompt's last line.
const (
	promptLastLine = "Reply here with only the report path."
	nudgeLastLine  = "your last action, and reply with only the report path."
)

// waitEcho waits until target has echoed lastLine, i.e. the shim has consumed
// the whole prompt and herdr will report it done on the next agent list.
func (s *e2eSession) waitEcho(t *testing.T, target, lastLine string) {
	t.Helper()
	want := "you said: " + lastLine
	deadline := time.Now().Add(e2eEchoTimeout)
	var last string
	for time.Now().Before(deadline) {
		last = s.screen(t, target)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(e2ePoll)
	}
	t.Fatalf("screen of %s never contained %q within %s; last screen:\n%s", target, want, e2eEchoTimeout, last)
}
```

Add a guard test so the constants cannot drift from the prompts (this is the
one assertion in the file that does not need herdr; keep it inside `TestE2E`
as the first subtest after `smoke` so it still runs only under the tag):

```go
	t.Run("prompt_last_lines", func(t *testing.T) {
		if !strings.HasSuffix(builderPrompt, promptLastLine) {
			t.Errorf("promptLastLine %q is not the last line of builderPrompt", promptLastLine)
		}
		if !strings.HasSuffix(nudgePrompt, nudgeLastLine) {
			t.Errorf("nudgeLastLine %q is not the last line of nudgePrompt", nudgeLastLine)
		}
	})
```

- [ ] **Step 2: Insert the waits into the existing subtests**

Three edits, each **directly after** the line named:

1. `marker_closes_round`: after `s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")` add
   `s.waitEcho(t, b.Builder.PaneID, promptLastLine)`. (Not required for the marker path, which ignores status; added so every case leaves the builder quiet before its first tick.)
2. `idle_without_marker_nudges_once`: after `s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")` add
   `s.waitEcho(t, b.Builder.PaneID, promptLastLine)`.
   And after `s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")` add
   `s.waitEcho(t, b.Builder.PaneID, nudgeLastLine)`.
3. `still_screen_closes_unmarked`: no change -- it starts from a builder that has finished echoing the nudge.

Leave every assertion as it is. In particular the
`b.BuilderScreenAt.Equal(baseTime.Add(startGrace+5*time.Second))` assertion
still holds: the fingerprint is taken inside the nudge tick, before the wait.

- [ ] **Step 3: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: `smoke`, `prompt_last_lines`, `marker_closes_round`,
`idle_without_marker_nudges_once`, `still_screen_closes_unmarked` all PASS.
No `relay-e2e-*` session left. Note the total time.

- [ ] **Step 4: Round 1's Task 3 Step 3 mutations**

1. In `idle_without_marker_nudges_once`, change the nudge tick's offset from `startGrace+5*time.Second` to `20*time.Second` (and the `BuilderScreenAt` expectation to match, so only the nudge assertion can fail). Expected: `waitScreen(... "You went idle without finishing")` fails -- start grace held. Revert both.
2. In `still_screen_closes_unmarked`, change the final tick's offset to `startGrace+6*time.Second+30*time.Second`. Expected: `round = 1, want 2` -- nudge grace held. Revert.

- [ ] **Step 5: `make check`, then commit**

```bash
git add internal/relay/e2e_test.go
git commit -m "test(e2e): wait for the shim to finish echoing; idle builder is nudged once, then closes unmarked on a still real screen (#114 step 2)"
```

---

### Task 4: Scrape, and a moving screen resets the grace

Execute **Task 4 of `docs/plans/2026-09-12-e2e-real-herdr.md`**, Steps 1-4,
with these insertions in both subtests as you add them:

- after each `s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")`:
  `s.waitEcho(t, b.Builder.PaneID, promptLastLine)`
- after each `s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")`:
  `s.waitEcho(t, b.Builder.PaneID, nudgeLastLine)`
- in `screen_movement_resets_grace`, the existing
  `s.waitScreen(t, b.Builder.PaneID, "you said: keep talking")` already waits
  for the echo of that one-line prompt; leave it.

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
(per-subtest PASS lines and total time), the observed failure text from each
mutation check, and anything you stopped on. Confirm `herdr session list`
shows no `relay-e2e-*` session. Then create the empty `NNN-done` file the
round prompt names, and reply with only the report path.
