# Plan: #382 round 1: writer roles in the core (roles.json writer rows, `Binding.Role`, one role accessor)

## 1. System overview

Spec: `docs/specs/2026-09-24-writer-roles-design.md`. Read §2–§6 before starting.
It is in this worktree, uncommitted, and this round commits it.

Today a `roles.json` row may add a new **reader** role only. This round makes a
new **writer** row valid and makes a binding run one writer role:

- `store.Binding` gains `Role`. An empty value means `builder`.
- Every place `internal/relevo` names the *role* as the literal `"builder"` asks
  the binding's role instead.
- The binding's gate comes from its role's `gate`.

**This round has no CLI flag.** The role reaches `Add`, `Bind` and `Fork` through
their option structs only. Round 2 adds `--role`, doctor rows and docs. Round 3
adds remote servers. In this round a remote add with a non-builder role is
refused.

The rename stays deferred. These names keep `builder`, because they name the
binding's **writer slot**, not the role:

- the `Builder` endpoint;
- `BuilderCandidate`;
- `NNN-builder.*` files;
- `--builder`;
- `RunningProcRef.Kind`;
- `remote.FeatureBuilder`;
- `pickEntry(…, "builder", …)` log lines. **Do not change those.** They are what
  `internal/ingest/outcome.go:136` reads, and a pick for any other role name is
  classified as a consult.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report. A halt that surfaces a design error is worth more than
a green suite that bent a test to fit.

## 2. File structure

```
internal/roles/file.go          validate (~133-158): accept a new writer row
internal/roles/registry.go      buildFile (~158-185): shape and gate default of a new writer row
internal/roles/file_test.go     port the two refusal tests (lines ~130-133, ~291-300)
internal/roles/registry_test.go new tests for a new writer row
internal/store/types.go         Binding (~190-240): Role field
internal/store/format.go        BindingFormat 1 -> 2; new recordFormat
internal/store/store.go         save (~398-407): write recordFormat(b)
internal/store/format_test.go   (no edit) golden is regenerated with -update
internal/store/testdata/binding-shape.golden  regenerated
internal/store/store_test.go    new format tests (or a new format_role_test.go)
internal/relevo/writer_role.go  NEW: bindingRole, checkWriterRole, ErrNotAWriterRole, roleGateDefault
internal/relevo/add.go          AddOptions.Role; Add (~105-340)
internal/relevo/bind.go         BindOptions.Role; resume (~276-291), create (~512-540), resolveBuilder (~596-610), resolveGate (~452)
internal/relevo/fork.go         Fork (~170-200 and the Binding literal ~305-330)
internal/relevo/send.go         preflight (~236-248); ResolveSendBuilder call (~157)
internal/relevo/headless.go     two Spec calls (~203, ~292)
internal/relevo/switch.go       resolveRole (~101), resolveBuilder BindOptions (~126)
internal/relevo/builder_change.go  ResolveSendBuilder (~28), applyBuilder (~48)
internal/relevo/candidate.go    CandidateKind (~363)
internal/relevo/status.go       BindingStatus.Role; row build (~333); RenderStatus builder line (~715-728)
internal/serve/rounds.go        ResolveSendBuilder call (~153): pass the binding's role
internal/relevo/writer_role_test.go  NEW: all internal/relevo tests of this round
```

## 3. Data structures

### `store.Binding` (`internal/store/types.go`)

Add, directly after `BuilderCandidate` (line ~213):

```
// Role is the writer role this binding runs (#382). "" means builder, and a
// builder binding stores "", so its JSON is byte-identical to one written
// before the field existed.
Role string `json:"role,omitempty"`
```

**Invariant:** `Role` is never the literal `"builder"` on disk. Every writer of a
binding normalises `"builder"` to `""`.

### Format (`internal/store/format.go`)

- `const BindingFormat = 2`. Update its comment: format 2 adds `role`.
- New function `recordFormat(b Binding) int`. It returns 2 when `b.Role != ""`,
  and 1 otherwise. Its comment states the rule: **the lowest format that can hold
  the record**. An older relevo then refuses to save exactly the bindings it would
  get wrong, and keeps working on every other one.
- `save` (`internal/store/store.go` ~404-407):
  - keeps the `b.Format > BindingFormat` refusal as it is;
  - replaces `b.Format = storedFormat(BindingFormat)` with
    `b.Format = storedFormat(recordFormat(b))`.

### `relevo.AddOptions`, `relevo.BindOptions`

Each gets the field `Role string`. Its comment says: the writer role the new
binding runs; `""` means builder. `Bind` ignores it on resume, because the
binding keeps its stored role.

### `relevo.BindingStatus` (`internal/relevo/status.go` ~79-95)

Add `Role string` with tag `json:"role,omitempty"`. It is the binding's stored
`Role`, so it is empty for builder.

## 4. Interfaces and contracts

### `internal/roles`

**`validate`** (`file.go` ~133-152). For a **new** (non-built-in) row:

- `"shape": "writer"` is now accepted. Delete only the branch that returns
  `"a new writer role needs relevo send --role, not yet available"`.
- Set the local `shape` to `harness.ShapeBuilder` when the row's word is
  `"writer"`, so the gate check below it sees a writer.
- For a new row, `shape` stays `harness.ShapeConsult` when the word is
  `"reader"`.
- Built-in rows are unchanged.

**`buildFile`** (`registry.go` ~166-176). For a new row, the `base` Role is
built from `row.Shape`:

| row.Shape | Shape | Gate before the row's own `gate` |
|---|---|---|
| `"writer"` | `harness.ShapeBuilder` | `true` |
| `"reader"` | `harness.ShapeConsult` | `false` |

The existing `if row.Gate != nil { base.Gate = *row.Gate }` then applies
unchanged. `validate` has already run, so `row.Shape` is non-nil for a new row.

### `internal/relevo/writer_role.go` (new)

```
// ErrNotAWriterRole reports a role given to add/bind/fork that is a reader.
var ErrNotAWriterRole = errors.New("not a writer role")

// bindingRole is the writer role b runs: b.Role, or "builder" when it is "".
func bindingRole(b store.Binding) string

// normRole is the stored form of a requested role: "" and "builder" both give "".
func normRole(role string) string

// checkWriterRole returns nil when role (after normRole; "" means builder) is a
// writer in reg. Otherwise:
//   unknown:  fmt.Errorf("unknown role %q (known: %v): %w", role, reg.Names(), ErrUnknownRole)
//   reader:   fmt.Errorf("--role %s: a reader role runs through relevo ask --role %s: %w", role, role, ErrNotAWriterRole)
func checkWriterRole(reg *roles.Registry, role string) error

// roleGates reports whether a binding of role takes policy.json's gate.default
// when neither --gate nor --no-gate is given: the role's Gate, or true when
// the registry does not know the role (the caller has already refused that).
func roleGates(reg *roles.Registry, role string) bool
```

### Gate resolution (`internal/relevo/bind.go:452`)

Keep `resolveGate(gate, noGate, pol)` as a wrapper that calls
`resolveGateFor(gate, noGate, pol, true)`.

New `resolveGateFor(gate string, noGate bool, pol policy.Policy, roleGates bool) string`:

1. If `noGate`, return `""`.
2. If `gate != ""`, return `gate`.
3. If `!roleGates`, return `""`.
4. Otherwise, return `pol.GateDefault()`.

The three construction sites call `resolveGateFor(…, roleGates(rt.RoleRegistry(), role))`
with the new binding's role:

- `add.go` ~313;
- `bind.go` ~537;
- `fork.go` ~196.

### Role threading: every call site

`role` below means the binding's role, as a role name (`"builder"` for an empty
`Role`).

| file:line | today | after |
|---|---|---|
| `add.go` ~105 `Add` | — | First statement after the existing argument checks: `if err := checkWriterRole(rt.RoleRegistry(), opts.Role); err != nil { return AddResult{}, err }`. Then, **before** the `opts.Server != ""` branch (~115): if `normRole(opts.Role) != ""` and `opts.Server != ""`, return `fmt.Errorf("server %s does not run custom roles (role %q); not yet available", opts.Server, opts.Role)`. Round 3 replaces this with a feature check. |
| `add.go` ~134, ~140 | `resolveRole(…, "builder")`, `resolveRoleTier(…, "builder")` | the requested role: `roleName := bindingRole(store.Binding{Role: normRole(opts.Role)})`, or an equivalent local helper |
| `add.go` ~276 `bindOpts` | — | `Role: normRole(opts.Role)` |
| `add.go` Binding literal ~300 | — | `Role: normRole(opts.Role)`; `Gate: resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleGates(reg, role))` |
| `bind.go` `create` ~472 | — | `checkWriterRole` first, then the role in `resolveRole` / `resolveRoleTier` ~512/516, `Role: normRole(opts.Role)` in the literal ~527, and the gate as above ~537 |
| `bind.go` `resume` ~276, ~281 | `"builder"` | `bindingRole(b)`, the stored binding. Before calling `resolveBuilder` (~291), set `opts.Role = b.Role`. |
| `bind.go` `resolveBuilder` ~596, ~607 | `"builder"` | `bindingRole(store.Binding{Role: opts.Role})`. The error text `binding %q builder: %w` stays. |
| `fork.go` ~174, ~187 | `"builder"` | `bindingRole(*src)`, or `bindingRole(src)` depending on its type. Set `bindOpts.Role = src.Role` (~283). The Binding literal gets `Role: src.Role`. The gate (~196) uses `resolveGateFor(explicitGate, opts.NoGate, rt.Policy, roleGates(reg, bindingRole(src)))`. |
| `send.go` ~246 | `Spec("builder", …)` | See **Preflight** below. |
| `send.go` ~157 | `ResolveSendBuilder(rt, b.BuilderCandidate, opts.Builder)` | `ResolveSendBuilderFor(rt, bindingRole(b), b.BuilderCandidate, opts.Builder)` |
| `headless.go` ~203, ~292 | `Spec("builder", …)` | `Spec(bindingRole(b), …)` |
| `switch.go` ~101 | `resolveRole(…, "", "builder")` | `resolveRole(…, "", bindingRole(b))` |
| `switch.go` ~126 | `BindOptions{Candidate, CWD, Headless, Tier}` | add `Role: b.Role` |
| `builder_change.go` ~28 | `ResolveSendBuilder(rt, current, token)` | Rename it `ResolveSendBuilderFor(rt Runtime, role, current, token string)`, with `role` in `resolveRole`. Keep `ResolveSendBuilder(rt, current, token)` as a one-line wrapper passing `"builder"`, so its existing tests stay unedited. |
| `builder_change.go` ~48 `applyBuilder` | `resolveRoleTier(…, "builder")` | `resolveRoleTier(…, bindingRole(b))` |
| `candidate.go` ~363 `CandidateKind` | `"builder"` | Add `CandidateKindFor(rt Runtime, token, role string) string`. `CandidateKind` becomes a wrapper passing `"builder"`. Round 2's CLI calls the `For` form. |
| `status.go` ~333 | `Role("builder")` | `Role(bindingRole(b))`. Also set `row.Role = b.Role`. |
| `internal/serve/rounds.go` ~153 | `relevo.ResolveSendBuilder(rt, b.BuilderCandidate, candidate)` | `relevo.ResolveSendBuilderFor(rt, bindingRole-equivalent, …)`. `bindingRole` is unexported, so export a tiny `relevo.BindingRole(b store.Binding) string` that `bindingRole` delegates to, and use it here. |

`served.go` (~195, ~219: `ResolveServedTier`, `PickServedCandidate`) is the
**server's create path**. It is round 3. **Do not change it now.**

### Preflight: a vanished role (`send.go` ~236-250, `headless.go` ~203, ~292)

Before each `Spec(bindingRole(b), kind)` call:

```
if _, ok := rt.RoleRegistry().Role(bindingRole(b)); !ok {
    return <zero>, fmt.Errorf("binding %s runs role %q, which roles.json no longer defines: %w", b.Name, bindingRole(b), ErrUnknownRole)
}
```

There is no fallback to builder. Put this in one helper, `bindingSpec(rt, b, kind)`
in `writer_role.go`, that makes both checks. Call it from all three sites.

### RenderStatus builder line (`status.go` ~715-728)

Just before the `if b.Switches > 0` block, add
`if b.Role != "" { fmt.Fprintf(&sb, "   role %s", b.Role) }`. A builder binding's
line stays byte-identical.

## 5. Pseudocode: an add with a custom writer

```
Add(opts{Role:"ui-builder"}):
  checkWriterRole(reg, "ui-builder")        -> unknown? ErrUnknownRole; reader? ErrNotAWriterRole
  refuse if opts.Server != ""               (round 1 only)
  res  := resolveRole(reg, set, Gates(rt), opts.Candidate, "ui-builder")   // ui-builder's list, its roles_missing gates
  tier := resolveRoleTier(opts.Tier, res.Candidate, reg, "ui-builder")
  ... worktree ...
  resolveBuilder(bindOpts{Role:"ui-builder"}) -> Spec("ui-builder", kind) -> ui-builder's definition
  Binding{Role:"ui-builder", Gate: resolveGateFor(..., roleGates(reg,"ui-builder"))}
  save -> recordFormat = 2 -> {"format":2, ..., "role":"ui-builder"}
  pickEntry(now, 1, "builder", res)          // unchanged slot name
Send(b): bindingSpec(rt, b, kind) -> role gone? error; else Spec(bindingRole(b), kind)
```

## 6. Error handling

| condition | error | where |
|---|---|---|
| unknown role at add/bind/fork | `unknown role %q (known: …)` wrapping `ErrUnknownRole` | `checkWriterRole` |
| reader role | `--role r: a reader role runs through relevo ask --role r` wrapping `ErrNotAWriterRole` | `checkWriterRole` |
| role without a definition for the candidate's kind | unchanged: `resolveRole` / `Spec` errors (`ErrNoDefinition`, `ErrRoleNotServed`) | existing |
| stored role no longer in roles.json | `binding b runs role "r", which roles.json no longer defines` wrapping `ErrUnknownRole` | `bindingSpec` |
| remote add with a custom role | `server S does not run custom roles (role "r"); not yet available` | `Add` |
| older relevo saving a format-2 binding | the existing `ErrNewerFormat` | `store.save` |

`Fork` needs no `checkWriterRole` call: the source's role was checked when it was
created. A vanished role still fails at the fork's first round, in
`bindingSpec`.

## 7. Tests

Put new `internal/relevo` tests in `writer_role_test.go`. Reuse
`newRuntime(t)`, `rolesFileRegistry`, `newFakeRunner`, `startRound`,
`containsAdjacentPair`, `testClaudeRef`, `testOpencodeRef` and `testPlannerName`
from `roles_runtime_test.go` and its neighbours. Model the launch test on
`TestRolesRuntimeCustomBuilderLaunchesCustomAgent` (line 191).

**`internal/roles`**

1. **Port** the table row `"new writer role"` (`file_test.go` ~130-133): move it
   out of the refusal table. **Port** `TestLoadNewWriterRefused` (~291-300) into
   `TestLoadNewWriterAccepted`: `Load` of `{"my-writer": {"shape": "writer"}}`
   succeeds. Cite this plan's §4 in its comment. These are ports, not deletions.
2. **`TestBuildNewWriterRow`** (`registry_test.go`). Build a file with
   `ui-builder: {shape: writer, definitions: {claude: {agent: my-ui}}, candidates: [a claude token]}`:
   - `Role("ui-builder")` has `Shape == harness.ShapeBuilder` and `Gate == true`;
   - `Spec("ui-builder","claude").Definition == "my-ui"`.
3. **`TestBuildNewWriterGateFalse`**: `"gate": false` gives `Gate == false`.
4. **`TestValidateNewReaderGateStillRefused`**: a new reader with
   `"gate": true` is still refused with `"a reader role has no gate"`.

**`internal/store`**

5. Run `go test ./internal/store -run TestBindingShapeMatchesFormat -update`
   once, after bumping `BindingFormat` and adding the field. The golden's first
   line becomes `format 2` and it gains the `role` key. **This is the one
   sanctioned golden rewrite.**
6. **`TestSaveFormatFollowsRole`**:
   - a binding with `Role: ""` saves with no `"format"` key and no `"role"` key;
   - `Role: "ui-builder"` saves with `"format": 2` and `"role": "ui-builder"`;
   - both load back with the same `Role`.
7. **`TestSaveRefusesNewerThanKnown`** probably exists already (#372): confirm
   that a loaded `Format: 3` is still refused. If an existing test already pins
   this, cite it in the report instead of adding one.

**`internal/relevo`** (`writer_role_test.go`)

8. **`TestBindingRole`**: `""` and `"builder"` both give `"builder"`, and
   `normRole("builder") == ""`.
9. **`TestCheckWriterRole`**:
   - `builder`, `""` and a file-defined writer give nil;
   - `reviewer` gives `errors.Is(err, ErrNotAWriterRole)`, and the message
     contains `relevo ask --role reviewer`;
   - `nope` gives `ErrUnknownRole`.
10. **`TestBindCustomWriterLaunchesItsDefinition`**. Registry rows:
    - `builder`: candidates `[testClaudeRef]`;
    - `ui-builder`: `{Shape: "writer", Candidates: [testClaudeRef], Definitions: {claude: {Agent: "my-ui"}}}`.

    Then `Bind(… BindOptions{Role: "ui-builder", Candidate: testClaudeRef, …, Headless: true})`
    and `startRound`. Check:
    - the argv has `--agent my-ui`;
    - the loaded binding's `Role == "ui-builder"`.
11. **`TestBindCustomWriterPicksFromItsList`**. Rows:
    - `builder`: `[claude/test/a]`;
    - `ui-builder`: `[claude/test/b]`, a writer with a claude definition.

    Use `rolesRuntimeCandidatesJSON`. `Bind` with `Role: "ui-builder"` and no
    candidate gives `BuilderCandidate == "claude/test/b"`.
12. **`TestBindReaderRoleRefused`**: `Bind` with `Role: "reviewer"` gives
    `ErrNotAWriterRole`, and no binding is stored.
13. **`TestGateFollowsRole`**. Use a policy whose `GateDefault()` is non-empty
    (see how `bind_test.go` sets `gate.default`).
    - A `ui-builder` writer with `gate: false` gets `b.Gate == ""`.
    - A writer with no `gate` key gets the policy default.
    - Builder gets the policy default.
    - An explicit `Gate: "make x"` on the gate-false role gives `"make x"`.
14. **`TestForkInheritsRole`**. Fork a `ui-builder` binding (copy the setup of an
    existing fork test in `fork_test.go`). The fork's `Role == "ui-builder"`,
    and its first round launches `my-ui`.
15. **`TestSwitchPicksFromRoleList`**. Copy the setup of an existing
    `switch_test.go` test that switches on a usage limit. The binding has
    `Role: "ui-builder"`, whose list is `[claude/test/b, claude/test/c]`, and it
    is running on b. After the switch, the binding runs `claude/test/c`, never a
    builder-only candidate.
16. **`TestVanishedRoleFailsRoundStart`**:
    - store a binding with `Role: "gone"`;
    - build a runtime whose registry has no `gone`;
    - `startRound` returns an error wrapping `ErrUnknownRole` whose text contains
      `which roles.json no longer defines`;
    - no process was started.
17. **`TestAddCustomRoleOnServerRefused`**: `Add` with `Role: "ui-builder"` and
    `Server: "s"` returns the not-yet-available error before any network call.
    Use a runtime whose `Remote` is nil or a fake that fails if called.
18. **`TestStatusShowsRole`**:
    - a status row for a `ui-builder` binding has `Role == "ui-builder"`, and
      `RenderStatus` prints `role ui-builder` on the builder line;
    - a builder binding's JSON has no `"role"` key, and its builder line has no
      `role`.

**Existing tests must pass unedited**, apart from the two ports in test 1 and the
golden in test 5. If any other existing test fails, stop and report which one
and why.

**Mutations.** Apply each one, run the named package, confirm the named test
fails, then revert. List each one in the report.

- **M1:** `headless.go`/`send.go` go back to `Spec("builder", …)` in
  `bindingSpec`. `TestBindCustomWriterLaunchesItsDefinition` fails.
- **M2:** `recordFormat` always returns 1. `TestSaveFormatFollowsRole` fails.
- **M3:** in `buildFile`, a new writer's default `Gate` is false.
  `TestBuildNewWriterRow` and `TestGateFollowsRole` fail.
- **M4:** `switch.go` goes back to `"builder"`. `TestSwitchPicksFromRoleList`
  fails.
- **M5:** `bindingSpec` falls back to builder when the role is missing.
  `TestVanishedRoleFailsRoundStart` fails.

## 8. Working efficiently

Each model step costs a full round trip, so:

- Batch the reads of every file in §2 as parallel tool calls in one step. They
  are already located, with line ranges; do not re-find them.
- Make every change to one file in one edit call.
- The `"builder"` → role substitutions in §4's table are a closed list. Do them
  file by file, not by a blind search-and-replace: `pickEntry` and
  `ExplainResolution("builder", …)` literals must stay.
- **Focused loop**, fixing every error before the next run:
  - `go test -count=1 ./internal/roles/ ./internal/store/`
  - `go test -count=1 ./internal/relevo/ -run 'Role|Gate|Fork|Switch|Status|Bind|Add'`
  - `go build ./...`
- **Once at the end:** `make check` (gofmt over tracked files, go vet, the
  `go mod tidy` check, `go test -race ./...`) and `sh scripts/check-name.sh`.

## 9. Ordered steps

1. **roles.** Deliverable: the `validate` and `buildFile` changes and tests 1–4.
   Verify: `go test -count=1 ./internal/roles/`.
2. **store.** Deliverable: the `Role` field, `BindingFormat = 2`,
   `recordFormat`, `save`, the golden rewrite (test 5) and test 6 (and 7 if
   needed). Verify: `go test -count=1 ./internal/store/`.
3. **relevo accessors.** Deliverable: `writer_role.go` (`bindingRole`,
   `BindingRole`, `normRole`, `checkWriterRole`, `roleGates`, `bindingSpec`,
   `ErrNotAWriterRole`), `resolveGateFor`, and tests 8–9. Verify: the package
   builds, and `go test -count=1 ./internal/relevo/ -run 'TestBindingRole|TestCheckWriterRole'`
   passes.
4. **Threading.** Deliverable: every row of §4's table, the preflight, and the
   `serve/rounds.go` call. Verify: `go build ./...`, then
   `go test -count=1 ./internal/relevo/ ./internal/serve/`. Every existing test
   passes unedited.
5. **Status.** Deliverable: `BindingStatus.Role` and the render suffix.
6. **Tests 10–18.** Verify: `go test -count=1 ./internal/relevo/`.
7. **Mutations M1–M5.** Verify: every package passes again after the last revert.
8. **Full check and commit.** `make check` and `sh scripts/check-name.sh` pass.
   Make two commits:
   1. `docs(specs): writer roles design (#382)`, the spec file only;
   2. `feat(roles): a roles.json writer row runs as a binding's role (#382)`.

   Do not push. The report lists:
   - the files changed;
   - each mutation and the test that failed for it;
   - any existing test that needed more than §7's two ports (there should be
     none);
   - the `make check` result.
