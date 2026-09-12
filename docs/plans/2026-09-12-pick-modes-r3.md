# Pick modes -- Go side, round 3: `Now` in the fake runtime, `go.sum` for textinput, then Task 4 (#15)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Round 2 outcome:** Task 2 landed (`bcd7c05`). Task 3 halted at Step 5 on two
plan errors, and the halt was right both times. `internal/pick/answer.go`,
`answer_test.go` and the `model.go` edits from Task 3 Steps 1-4 are in the
worktree, uncommitted, as the plan gave them.

**What was wrong, and the ruling on each:**

1. **`go.sum`.** The plan said `bubbles/textinput` would need no module
   change because `bubbles` is already required. False: `textinput` imports
   `github.com/atotto/clipboard`, which nothing else in relay pulls, so it is
   absent from `go.sum` and the package cannot build. The constraint is
   withdrawn. `go mod tidy` is the fix and its whole delta is three lines --
   one `// indirect` requirement in `go.mod`, two checksum lines in `go.sum`.
   The planner ran it in a scratch copy of this branch; Step 3 below says
   exactly what to expect.
2. **`Runtime.Now`.** `testRuntime` in `internal/pick/fake_test.go` built a
   `relay.Runtime` without `Now`. `relay.Answer` stamps its log entry with
   `rt.Now().UTC()`, so `TestAnswerEnterSendsTheParsedAnswer` panics on a nil
   func. The fake sets `Now: time.Now`. This is a fixture fix; `relay.Answer`
   does not change.

With both applied, every test in `internal/pick` passes (verified by the
planner in the scratch copy). This round applies them, finishes Task 3, then
does Task 4 as written -- with its `go mod tidy` expectation corrected.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/pick-go` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/pick-go`: `fa17e17`, `bcd7c05`, plus uncommitted Task 3 files. |
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

- **Changed from rounds 1-2:** `go.mod` and `go.sum` change once, in Task 3
  Step 3, by exactly the delta shown there. After that commit, `go mod tidy`
  is a no-op again and `make check`'s tidy check enforces it.
- The no-`--pick` paths of `done`, `unbind` and `answer` stay byte-for-byte
  unchanged in behaviour and output.
- `internal/pick` must not import `internal/ui`.
- relay parses nothing out of the dialog text.
- No test in `cmd/relay` may reach `newRuntime`.
- Nothing new is written to `log.jsonl`.
- One commit per task, on the worktree's branch.

---

### Task 3 (resumed): fixture `Now`, module checksums, finish Task 3

**Files:**
- Modify: `internal/pick/fake_test.go` (`testRuntime` and its imports)
- Modify: `go.mod`, `go.sum` (by `go mod tidy` only -- no hand edits)
- Everything else from Task 3 Steps 1-4 stays as round 2 wrote it.

**Interfaces:** unchanged from round 1's Task 3.

- [ ] **Step 1: Confirm the worktree is where round 2 left it**

Run:

```bash
git log --oneline main..HEAD | head -3
git status --short
```

Expected: `bcd7c05` and `fa17e17` among the commits (there may be docs-only
deletions relative to `main` in `git diff --stat`; `main` moved after this
branch was cut and that is not your concern), and exactly:

```
 M internal/pick/model.go
?? internal/pick/answer.go
?? internal/pick/answer_test.go
```

Anything else: stop and say so.

- [ ] **Step 2: Give the fake runtime a clock**

In `internal/pick/fake_test.go`, add `"time"` to the imports and change the
return in `testRuntime` to:

```go
	return relay.Runtime{Store: st, Herdr: fh, Now: time.Now}
```

Update the doc comment on `testRuntime` to:

```go
// testRuntime seeds a store with the given bindings and returns a runtime
// over it and the fake herdr. Now is set because relay.Answer stamps its log
// entry with it; a nil clock panics.
```

- [ ] **Step 3: Add the module checksums textinput needs**

Run:

```bash
cp go.mod /tmp/gm.before; cp go.sum /tmp/gs.before
go mod tidy
diff /tmp/gm.before go.mod; diff /tmp/gs.before go.sum
```

Expected output, and nothing else:

```
12a13
> 	github.com/atotto/clipboard v0.1.4 // indirect
0a1,2
> github.com/atotto/clipboard v0.1.4 h1:EH0zSVneZPSuFR11BlR9YppQTVDbh5+16AmcJi4g1z4=
> github.com/atotto/clipboard v0.1.4/go.mod h1:ZY9tmq7sm5xIbd9bOK4onWV4S6X0u6GY7Vn0Yu86PYI=
```

(The `12a13` / `0a1,2` line numbers may differ by a line or two; the three
added lines must not.) If `go mod tidy` adds or removes anything else, stop
and report the full diff.

- [ ] **Step 4: Run the pick tests**

Run: `go test ./internal/pick`
Expected: PASS -- every test, including all of `answer_test.go`. If
`TestAnswerEnterSendsTheParsedAnswer` fails inside `relay.Answer` with
`ErrBuilderGone`, the fake's agent does not match the binding's endpoint by
`AgentName` -- stop and report; do not touch `relay.Answer`.

- [ ] **Step 5: Verify and commit (round 1's Task 3 Step 6)**

Run: `make check`
Expected: green, including the tidy check (tidy is now a no-op).

```bash
git add internal/pick go.mod go.sum
git commit -m "feat(pick): answer screen -- live dialog above, one typed line below (#15 task 3)"
```

---

### Task 4: `pick.Run` and the `--pick` flag

Exactly as **Task 4** in `docs/plans/2026-09-12-pick-modes.md`, Steps 1-9,
with one correction to **Step 9**: the sentence "`go mod tidy` must leave
`go.mod`/`go.sum` untouched (`textinput` and `viewport` live in the
already-required bubbles module)" is replaced by "`go mod tidy` is a no-op
after Task 3's commit; `make check` enforces it". Commit message as given
there.

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), the exact `go.mod`/`go.sum` delta, and
`git diff --stat main..HEAD`. If any step was impossible as written, say which
and why -- do not work around it.
