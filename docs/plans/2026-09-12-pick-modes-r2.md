# Pick modes -- Go side, round 2: corrected fixture, then Tasks 2-4 (#15)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Round 1 outcome:** Task 1 landed (`fa17e17`) and is correct. Task 2 halted
at Step 10 because the plan's own test fixture was wrong, and the halt was the
right call. `internal/pick/` is present in the worktree, uncommitted, with
every file from Task 2 Steps 1-9 written as the plan gave them.

**What was wrong:** `TestRowsForFiltersPerVerb` (Task 2 Step 2) built a DONE
row as `{Display: "DONE"}` with no `State`. `relay.HideDone` filters on
`b.State == string(store.StateDone)` -- `Display` is *derived* from `State` in
a real report (`internal/relay/status.go`, `HideDone` and `displayState`), so a
row with `Display: "DONE"` and an empty `State` cannot exist in practice. The
fixture must set `State`. `rowsFor` in `verb.go` is right as written; it does
not change.

**This round:** fix that one fixture, finish Task 2, then do Tasks 3 and 4
exactly as `docs/plans/2026-09-12-pick-modes.md` states them. That file is in
this worktree; the tasks are not repeated here, only referenced.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/pick-go` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/pick-go`, one commit (`fa17e17`) ahead of `main`, plus the uncommitted `internal/pick/`. |
| `~/.local/state/relay/pick-go` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

Do **not** run `herdr` yourself.

## Global constraints

Unchanged from round 1:

- The no-`--pick` paths of `done`, `unbind` and `answer` stay byte-for-byte
  unchanged in behaviour and output.
- `internal/pick` must not import `internal/ui`.
- relay parses nothing out of the dialog text.
- No test in `cmd/relay` may reach `newRuntime`.
- Nothing new is written to `log.jsonl`.
- One commit per task, on the worktree's branch.

---

### Task 2 (resumed): correct the fixture, finish Task 2

**Files:**
- Modify: `internal/pick/verb_test.go` (the fixture in `TestRowsForFiltersPerVerb` and its imports)
- Everything else in `internal/pick/` stays exactly as round 1 wrote it.

**Interfaces:** unchanged from round 1's Task 2.

- [ ] **Step 1: Confirm the worktree is where round 1 left it**

Run:

```bash
git log --oneline main..HEAD
git status --short
```

Expected: one commit `fa17e17 refactor(relay): ParseAnswer, ...` and
`?? internal/pick/`. If `internal/pick/` is missing or `git status` shows
anything else, stop and say so.

- [ ] **Step 2: Correct the fixture**

In `internal/pick/verb_test.go`, add `"github.com/fuad-daoud/relay/internal/store"`
to the imports, and replace the `rep := relay.Report{...}` literal in
`TestRowsForFiltersPerVerb` with:

```go
	// State is what HideDone keys on; Display is derived from it in a real
	// report. A row with Display "DONE" and no State cannot occur, so the
	// fixture sets both -- round 1 set only Display and the test could not
	// pass against the real HideDone.
	rep := relay.Report{Bindings: []relay.BindingStatus{
		{Name: "active", State: string(store.StateActive), Display: "ACTIVE", BuilderStatus: herdr.StatusWorking},
		{Name: "blocked", State: string(store.StateNeedsYou), Display: "NEEDS YOU", BuilderStatus: herdr.StatusBlocked},
		{Name: "finished", State: string(store.StateDone), Display: "DONE", BuilderStatus: herdr.StatusIdle},
	}}
```

Nothing else in the test changes: the expected rows per verb are still
`done → [active blocked]`, `unbind → [active blocked finished]`,
`answer → [blocked]`.

- [ ] **Step 3: Run the pick tests**

Run: `go test ./internal/pick`
Expected: PASS -- every test in `verb_test.go`, `list_test.go`,
`model_test.go`. If any test other than `TestRowsForFiltersPerVerb` fails,
stop and report it; round 1 reported all of them passing.

- [ ] **Step 4: Verify and commit (round 1's Task 2 Step 11)**

Run: `make check`
Expected: green. Confirm `grep -rn '"github.com/fuad-daoud/relay/internal/ui"' internal/pick` prints nothing.

```bash
git add internal/pick
git commit -m "feat(pick): binding list and result screens; done and unbind run from the list (#15 task 2)"
```

---

### Task 3: the answer screen

Exactly as **Task 3** in `docs/plans/2026-09-12-pick-modes.md`, Steps 1-6,
unchanged. Commit message as given there.

---

### Task 4: `pick.Run` and the `--pick` flag

Exactly as **Task 4** in `docs/plans/2026-09-12-pick-modes.md`, Steps 1-9,
unchanged. Commit message as given there.

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. If any step was
impossible as written, say which and why -- do not work around it.
