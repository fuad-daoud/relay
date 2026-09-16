# Doctor first-run pass, round 2: finish Task 3, then Tasks 4-9 (#165, #166)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** none; the design is the "Design" section of the round 1 plan.
**Round 1 plan:** `docs/plans/2026-09-16-doctor-first-run.md` -- in your
worktree. Tasks 4-9 of this round are executed from that file, as written,
with the two amendments below.
**Issues:** #165, #166.

## Where you are

The worktree already holds round 1's work:

```
6619680 feat(doctor): WithDefinitions scopes role rows to what a candidate would load (#166)
502f9ff feat(harness): a role names every definition it needs installed (#166)
bb20d7a docs: doctor first-run plan (#165, #166)
```

plus **uncommitted** edits to `internal/doctor/doctor.go` and
`internal/doctor/doctor_test.go`: round 1's Task 3 Steps 1-3, complete and
correct. Do not revert them. Round 1 halted at Task 3 Step 4 because a
second existing test, `TestDoctorAgyMissingRoleHasAFix`
(`internal/doctor/doctor_test.go`, around line 786), pins the old
missing-file fix string exactly, and the plan allowed only one assertion
to change. The halt was right: the plan was wrong, not the test. That test
pins the same behaviour Task 3 changes on purpose (a missing role file's
fix must create its directory), so its expected string moves with it. That
is Task 3b below. Tasks 4-9 then run to the end from the round 1 file.

Do not touch the *drift* assertion further down the same file (the
subtest "one extra line warns with the print fix", around line 837): that
pins the fix for a file that exists and differs, which Task 3 leaves alone
by design.

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/doctor-first-run` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/doctor-first-run`. |
| `~/.local/state/relay/doctor-first-run` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and
do not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
sh scripts/check-plugin-version.sh
command -v shellcheck >/dev/null && shellcheck scripts/*.sh
for t in scripts/*_test.sh; do echo "==> $t"; sh "$t"; done
```

Do **not** run `herdr` or `make e2e` yourself. The round 1 plan's rules on
where tests may live apply unchanged.

## Amendments to the round 1 plan

1. **Global constraints**, the bullet "existing `internal/doctor` tests are
   unchanged except the one fix-string assertion named in Task 3": read
   "except the two fix-string assertions named in Task 3 and Task 3b".
2. **Task 9 Step 2**, the scope list: add
   `docs/plans/2026-09-16-doctor-first-run-r2.md` (this file, committed by
   the planner before the round started).

Everything else in the round 1 file stands as written.

---

### Task 3b: finish Task 3 -- the second pinned fix string moves with the behaviour

**Files:**
- Modify: `internal/doctor/doctor_test.go` (`TestDoctorAgyMissingRoleHasAFix`)
- Commit: the uncommitted round 1 Task 3 edits to `internal/doctor/doctor.go`
  and `internal/doctor/doctor_test.go`, together with this change

**Interfaces:** none new. The missing-file fix is exactly
`mkdir -p <dir> && relay agent print --kind <kind> --role <role> > <homeRel>`
as round 1 Task 3 implemented it.

- [ ] **Step 1: Confirm the halt state.** `git status --short` shows exactly
`M internal/doctor/doctor.go` and `M internal/doctor/doctor_test.go`.
`go test -count=1 ./internal/doctor -run TestDoctorAgyMissingRoleHasAFix`
FAILS with the row's `Fix` starting `mkdir -p ~/.gemini/config/agents &&`.
If either is not so, stop and say what you found.

- [ ] **Step 2: Move the assertion.** In `TestDoctorAgyMissingRoleHasAFix`
replace the expected fix string:

```go
		c.Fix != "mkdir -p ~/.gemini/config/agents && relay agent print --kind agy --role reviewer > ~/.gemini/config/agents/reviewer.md" {
```

Nothing else in that test changes.

- [ ] **Step 3: Run** `go test -race -count=1 ./internal/doctor ./cmd/relay`.
Expected: PASS -- this is round 1's Task 3 Step 4, now green.

- [ ] **Step 4: Commit** (round 1's Task 3 Step 5, with this file included)

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "fix(doctor): the missing-role fix creates the agents directory first (#166)"
```

---

### Tasks 4-9

Execute Tasks 4, 5, 6, 7, 8 and 9 from
`docs/plans/2026-09-16-doctor-first-run.md`, in order, exactly as written
there, with the two amendments above. Task 9's report goes to the drop
directory as relay's prompt named it, followed by the completion marker.
