# Plan: #382 round 2: `--role` on bind and add, doctor names a binding whose role vanished, docs

## 1. System overview

Spec: `docs/specs/2026-09-24-writer-roles-design.md`, committed in round 1. Read
§4 and §5.4.

Round 1 (on this branch) made `roles.json` writer rows valid and threaded a
binding's role through `internal/relevo`. The API this round uses:

- `AddOptions.Role` and `BindOptions.Role`. `""` means builder.
- `relevo.BindingRole(b store.Binding) string` returns the role name, `builder`
  for `""`.
- `relevo.CandidateKindFor(rt, token, role string) string`.
- `relevo.ErrNotAWriterRole` and `relevo.ErrUnknownRole`.
- `BindingStatus.Role`.

Read `internal/relevo/writer_role.go` first. It holds everything round 1 added.

This round adds the CLI surface and the doctor row, and documents both:

1. `relevo bind --role <r>` and `relevo add --role <r>`.
2. The pick note printed on stdout names the role.
3. `relevo ask --role` help text.
4. A doctor row for a binding whose role is no longer in `roles.json`.
5. A regression test proving doctor already checks a custom writer's definitions
   on disk. `assembleRoleDefinitions` (`cmd/relevo/doctor.go:96`) and
   `rolesMissingGates` (`internal/relevo/ledger.go:244`) iterate every registry
   role, so no code change should be needed there. If the test shows otherwise,
   stop and report.
6. The README.

**Not in this round:**

- remote servers (round 3). `Add` still refuses `--server` with a custom role,
  and that message stays;
- `relevo fork` (it inherits the role, with no flag);
- `relevo send` (there is no `--role`).

**CLI tests must spawn nothing and reach no network.** Test a flag's effect by
parsing and error text only, or through the `internal/relevo` functions, never by
running a `bind`/`add` that starts a harness. The package's TestMain already
isolates HOME and the XDG dirs, and a test that needs a `roles.json` writes one
under a `t.TempDir()` it sets as `XDG_CONFIG_HOME` (#235).

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 2. Files touched

```
cmd/relevo/main.go          cmdBind (~952-1090), cmdAdd (~1180-1280), cmdAsk (~1489 help text)
cmd/relevo/doctor.go        wire bindingRoleChecks next to roleSourceChecks (~382)
internal/doctor/roles.go    NEW (or the file holding roleSourceChecks' doctor-side helpers): BindingRoleChecks
internal/doctor/roles_test.go  NEW
cmd/relevo/doctor_test.go   one regression test (custom writer definition is checked)
cmd/relevo/main_test.go     (or the file with bind/add flag tests) flag tests
README.md                   §"Roles" (~1451-1520), command surface (~262, ~354)
```

Find the file where `roleSourceChecks` is defined first (`grep -n 'func roleSourceChecks' cmd/relevo/*.go`),
and put the wiring beside it.

## 3. Data structures

None new. The doctor row is a `doctor.Check`:

- `Name: "binding role"`
- `Severity: doctor.SevFail`
- `Detail: fmt.Sprintf("binding %s runs role %q, which roles.json no longer defines", name, role)`
- `Fix: "restore the role in roles.json, or relevo done " + name`

## 4. Contracts

### `cmdBind` (`cmd/relevo/main.go` ~952)

- **New flag:** `role := fs.String("role", "", "writer role this binding runs: a roles.json writer row (default builder)")`.
- **`--role` with `--resume`:** if `*role != ""` and `*resume` is set, return
  `fmt.Errorf("relevo bind --resume keeps the binding's role; drop --role")`.
  Do this before any runtime work.
- `opts.Role = *role` in the `relevo.BindOptions` literal (~1002).
- **Preflight** (~1020-1045):
  1. Let `roleName := *role`. If it is `""`, use `"builder"`.
  2. The two `relevo.CandidateKind(rt, opts.Candidate)` calls become
     `relevo.CandidateKindFor(rt, opts.Candidate, roleName)`.
  3. `rt.RoleRegistry().Spec("builder", kind)` (~1043) becomes
     `Spec(specRole, kind)`. `specRole` is:
     - on the adopted path, `relevo.BindingRole(existing)`, using the binding
       already loaded there (widen its scope if needed);
     - otherwise, `roleName`.
- Both `notePick("builder", res)` calls (~1072, ~1083) become
  `notePick(roleName, res)`. On a resume, `roleName` must be the stored role,
  `relevo.BindingRole(b)` of the returned binding. These are stdout lines only.
  The `pickEntry` log entries inside `internal/relevo` stay `builder`, so do not
  touch them.

### `cmdAdd` (`cmd/relevo/main.go` ~1180)

- **New flag:** the same `--role`, with the same help text.
- `Role: *role` in the `relevo.AddOptions` literal (~1237).
- `notePick("builder", res.Resolution)` (~1277) becomes
  `notePick(roleOrBuilder(*role), res.Resolution)`.

Add a tiny helper, `func roleOrBuilder(r string) string`, in `main.go`, and use it
in both commands.

### `cmdAsk` help text (`cmd/relevo/main.go` ~1489)

`"consult role: reviewer, researcher"` becomes
`"reader role to consult: reviewer, researcher, or a reader row in roles.json"`.

### `doctor.BindingRoleChecks` (`internal/doctor/roles.go`)

```
// BindingRoleChecks reports one FAIL row per binding that is not DONE whose
// role (BindingRole(b); "builder" for "") the registry does not define. Pure.
func BindingRoleChecks(bindings []store.Binding, known func(role string) bool) []Check
```

- `known` is `func(r string) bool { _, ok := reg.Role(r); return ok }`, which
  keeps `internal/doctor` free of a `relevo` import.
- Compute the role name inline as `b.Role`, or `"builder"` when it is empty. Do
  **not** import `internal/relevo`.
- Rows are sorted by binding name.
- **Wiring in `cmd/relevo/doctor.go`:**
  1. List bindings with `rt.Store.List()`. On error, skip the rows silently,
     the same way `plannerCheckInput` treats a store error (~606-613).
  2. Append the rows beside `roleSourceChecks` (~382).

## 5. Pseudocode

```
relevo bind --role ui-builder:
  refuse --role + --resume
  kind  := CandidateKindFor(rt, opts.Candidate, "ui-builder")     // advisory
  defs  := Spec("ui-builder", kind).Definitions                  // preflight the custom files
  b     := relevo.Bind(opts{Role:"ui-builder"})                  // round 1's checks: unknown/reader refused
  notePick("ui-builder", res)                                    // "picked … for ui-builder: …"

relevo doctor:
  for b in store.List() where b.State != DONE:
    if !reg.Role(role(b)) -> FAIL "binding b runs role "x", which roles.json no longer defines"
```

## 6. Error handling

`Bind` and `Add` already return the round 1 errors (`ErrUnknownRole`,
`ErrNotAWriterRole`). The CLI prints them as they are. The one new CLI error is
`--role` combined with `--resume`. Doctor rows never fail the command; they are
data.

## 7. Tests

1. **`TestBindingRoleChecks`** (`internal/doctor/roles_test.go`, pure). Given:
   - binding `a` with `Role: ""`;
   - binding `b` with `Role: "ui-builder"`, where `known` says yes;
   - binding `c` with `Role: "gone"`, where `known` says no;
   - binding `d` with `Role: "gone"` in state DONE.

   Expect exactly one row, for `c`: Severity FAIL, and a Detail containing
   `binding c runs role "gone"`.
2. **`TestBindRoleWithResumeRefused`** (cmd/relevo). Run
   `run([]string{"bind", "--resume", "--role", "x", "--name", "n"})`. It returns
   the `drop --role` error, and nothing is spawned. The check happens before
   `newRuntime`, so the command never reaches a harness.
3. **`TestBindUnknownRoleRefused`** (cmd/relevo):
   - write a `roles.json` under a temp `XDG_CONFIG_HOME` with no `nope` row;
   - `run([]string{"bind", "--role", "nope", "--name", "n", "--builder", <a configured claude token>})`
     returns an error containing `unknown role "nope"`.

   **Before writing this test, confirm by reading `create` in
   `internal/relevo/bind.go`** that round 1's `checkWriterRole` runs before
   `resolveBuilder` and before anything spawns. If it doesn't, write this test
   against `relevo.Bind` in `internal/relevo` instead, and say so in the report.
4. **`TestDoctorChecksCustomWriterDefinition`** (cmd/relevo/doctor_test.go).
   Find the existing test that pins a custom *reader* or custom *builder*
   definition in `assembleRoleDefinitions`, or in the `roles_missing` doctor
   output (`grep -n 'assembleRoleDefinitions\|my-scout\|my-executor' cmd/relevo/*_test.go`).
   Add a sibling for a new writer row, `ui-builder` with the claude agent
   `my-ui`: `my-ui` appears in the definitions doctor checks for claude.
5. **`TestAskRoleHelpText`**: skip it if no existing test pins flag help text.
   Otherwise port that test to the new text.

**Mutations.** Apply each one, run the named test, confirm it fails, then revert.

- **M1:** `BindingRoleChecks` also reports DONE bindings. Test 1 fails.
- **M2:** `BindingRoleChecks` treats `""` as unknown. Test 1 fails.
- **M3:** drop the `--role`/`--resume` refusal. Test 2 fails.

## 8. Working efficiently

Each model step costs a full round trip, so:

- Read `internal/relevo/writer_role.go`, the `cmdBind`/`cmdAdd`/`cmdAsk` bodies,
  `cmd/relevo/doctor.go` around lines 300-400, and the README lines in §2 as
  parallel tool calls in one step.
- Make every change to one file in one edit call.
- **Focused loop:**
  - `go test -count=1 ./internal/doctor/`
  - `go test -count=1 -run 'Role|Doctor' ./cmd/relevo/`
  - `go build ./...`
- **Once at the end:** `make check` and `sh scripts/check-name.sh`.

## 9. Ordered steps

1. **Doctor row.** Deliverable: `BindingRoleChecks` and test 1. Verify:
   `go test -count=1 ./internal/doctor/`.
2. **CLI.** Deliverable: the `--role` flags, the resume refusal, the preflight,
   `notePick`, `roleOrBuilder`, the ask help text, and the doctor wiring. Verify:
   `go build ./...` and `go test -count=1 ./cmd/relevo/`.
3. **CLI tests 2–5.** Verify: `go test -count=1 -run 'Role|Doctor' ./cmd/relevo/`.
4. **Mutations M1–M3.**
5. **README.**
   - **Roles section** (~1480-1482): replace the `shape` bullet's "must be a
     `reader` for now: it runs with `relevo ask --role <name>`" with:
     - a new **reader** runs with `relevo ask --role <name>`;
     - a new **writer** runs as a binding's role: `relevo add --role <name>`
       or `relevo bind --role <name>`. Every round of that binding runs it, and
       `relevo fork` keeps it.
   - **The `gate` bullet:** true takes `policy.json`'s `gate.default`, false
     takes none. It defaults to true for every writer; an explicit `--gate`
     still wins.
   - **The JSON example** (~1462-1478): add a writer row,
     `"ui-builder": { "shape": "writer", "candidates": ["claude/anthropic/sonnet"], "definitions": { "claude": { "agent": "my-ui-builder" } } }`.
   - **"Seeing it"** paragraph: add that `relevo status` shows `role <r>` on a
     non-builder binding's builder line, and `status --json` has `role`.
   - **Command surface:** add `[--role R]` to the `relevo bind` line (~262) and
     the `relevo add` line (~354). Add half a sentence to each: the writer role
     the binding runs, default `builder`.

   Verify: `sh scripts/check-name.sh`.
6. **Two leftovers from round 1.**
   - In `internal/roles/file.go`, the `Row.Shape` doc comment (~lines 45-47)
     still says a new role "must be a reader (writer rows need `relevo send
     --role`, S2)". Rewrite it: a new role gives its shape, a new writer runs as
     a binding's role (`add`/`bind --role`), and a new reader runs through
     `relevo ask --role`.
   - `resolveGate` (`internal/relevo/bind.go` ~452) has no callers left. If
     `grep -rn 'resolveGate(' --include='*.go' .` shows only its definition and
     tests, keep it only if a test calls it. Otherwise delete it, and say which
     in the report.
7. **Full check and commit.** `make check` passes. Then make one commit:

       feat(roles): relevo bind/add --role, doctor names a binding whose role vanished (#382)

   Do not push. The report lists:
   - the files changed;
   - each mutation and the test that failed for it;
   - whether test 3 ran through the CLI or through `relevo.Bind`, and why;
   - the `make check` result.
