# Plan: the gate, consults and the verify reviewer run in their own scopes (#313)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. In
particular, halt and report if any of these turns out to be false:

- `proc.Runner.Start` (`internal/proc/proc.go`) and `proc.ScopeArgv`
  (`internal/proc/scope.go`) treat `ScopeSpec.Unit` as an opaque name. Nothing
  in `internal/proc` requires or special-cases the `relay-round-` prefix
  outside comments and tests. If something does, a `relay-gate-*` unit would
  behave differently from a round, and this plan's premise is wrong.
- No code path can start a second gate for the same binding **and round**
  while the first gate process might still be alive. A gate starts only when
  `b.GateRun == nil` (`gate.go` ~line 43). A unit name that repeats while its
  unit is still active makes `systemd-run --unit` fail.
- `reserveConsult` (`internal/relay/ask.go` ~line 244) loads the binding under
  the lock, so it can return the binding's `Owner` with no second load.

## 1. System Overview

#295, #290 and #309 put every builder round in its own transient systemd
scope, built in `startRound` (`internal/relay/headless.go` ~line 168) from the
`Runtime.Scope` template. Four other spawn paths build a bare `ProcSpec` with
no `Scope`, so they run unbounded in whatever cgroup spawned them:

| Path | File | What it runs |
|---|---|---|
| gate | `internal/relay/gate.go` ~44 | `sh -c <gate> 2>&1`, e.g. `make check`, relay's heaviest process |
| headless consult | `internal/relay/ask.go` ~198 | a `relay ask` consult |
| session consult | `internal/relay/ask.go` ~423 | a consult that resumes the builder's session |
| verify reviewer | `internal/relay/verify.go` ~288 | the verify consult in a throwaway worktree |

This change gives all four a scope from the same template, with a unit name
that says what it is. The gate may have its own CPU quota. On a host where
`proc.Runner`'s lazy probe (#295/#309) finds no usable systemd user manager,
the scope is dropped, exactly as for rounds today. That needs no work here.

**Decisions already made. Do not revisit them:**

1. **Gate quota.** `policy.json` gains an optional
   `scope.gate_cpu_quota` (and `serve.scope.gate_cpu_quota`, since
   `serve.scope` replaces the whole block). When it is set, the gate's scope
   uses it as `CPUQuota`. When it is unset, the gate uses the block's
   `cpu_quota`, the same as a round.
2. **Consults and verify** use the block's `cpu_quota`, the same as a round.
   They have no quota of their own.
3. **Gate rusage is out of scope.** Do not add fields to `store.GateRecord`,
   and do not change `supervisorScript`'s guard in `internal/proc/proc.go`.
   See §6 for the side effect this leaves in the gate log, and how step 4
   handles it.
4. **Unit names** follow `scopeUnitName`'s existing shape exactly, with the
   kind in place of `round`:
   - `relay-gate-<owner8>-<name>-<round>`
   - `relay-consult-<owner8>-<name>-<round>-<consultID>` (both consult paths)
   - `relay-verify-<owner8>-<name>-<round>-<consultID>`

   `owner8` and the `safeUnitPart` sanitising are exactly what `scopeUnitName`
   does today. `relay-round-…` names must stay byte-identical.

## 2. File Structure

```
internal/policy/policy.go         MODIFY  ScopePolicy.GateCPUQuota + validateScope rule
internal/policy/policy_test.go    MODIFY  good/bad gate_cpu_quota rows for both blocks
internal/relay/runner.go          MODIFY  ScopeSpec.GateCPUQuota (template-only field, doc says the Runner ignores it)
internal/relay/headless.go        MODIFY  scopeKind, scopeUnitNameFor, scopeFor; scopeUnitName and startRound go through them
internal/relay/gate.go            MODIFY  gate ProcSpec gets scopeFor(gate); tailLines skips the rusage trailer
internal/relay/ask.go             MODIFY  reserveConsult also returns the owner; both consult ProcSpecs get scopeFor(consult)
internal/relay/verify.go          MODIFY  verify ProcSpec gets scopeFor(verify)
internal/relay/headless_test.go   MODIFY  unit-name and scopeFor tables
internal/relay/gate_test.go       MODIFY  gate spec carries the scope; tail skips the rusage line
internal/relay/ask_test.go        MODIFY  both consult paths carry the scope
internal/relay/verify_test.go     MODIFY  verify carries the scope
internal/proc/proc_test.go        MODIFY  pin relay.RusageTrailerPrefix == proc.RusageTrailer
cmd/relay/serve.go                MODIFY  scopeFromPolicy copies GateCPUQuota; scopeStatusText mentions it
cmd/relay/serve_test.go           MODIFY  extend the existing pure TestScopeFromPolicy table only
README.md                         MODIFY  the **Scopes.** paragraph (~line 556)
```

## 3. Data Structures & Type Definitions

### `policy.ScopePolicy` (`internal/policy/policy.go` ~line 124)

Add after `CPUQuota`:

- `GateCPUQuota string` with json tag `gate_cpu_quota,omitempty`. "" means the
  gate uses `cpu_quota`. Otherwise it must match `^[0-9]+%$` and be at least
  1%, the same grammar and pattern (`cpuQuotaPattern`) as `cpu_quota`.

### `relay.ScopeSpec` (`internal/relay/runner.go` ~line 22)

Add a last field:

- `GateCPUQuota string`, **template only**. It is set on `Runtime.Scope` from
  policy, and read only by `scopeFor` when it builds a gate's spec. `scopeFor`
  always returns a spec with this field zeroed. The local Runner
  (`proc.ScopeArgv`) never reads it. Say all of this in the field's doc
  comment.

Why here and not a `Runtime` field: `Runtime.Scope` already travels from
policy to both the local daemon (`cmd/relay/main.go` ~522) and the server
(`cmd/relay/serve.go` ~341, then `serve.Config.Scope`, then `internal/serve/serve.go`
~136). A template field needs no new plumbing through `serve.Config`.

### `relay.scopeKind` (`internal/relay/headless.go`)

An unexported string type with four values: `scopeRound = "round"`,
`scopeGate = "gate"`, `scopeConsult = "consult"`, `scopeVerify = "verify"`.

### `relay.RusageTrailerPrefix` (`internal/relay/runner.go`)

`const RusageTrailerPrefix = "relay-rusage:"`, exported. The relay package
cannot import `internal/proc` (proc imports relay), so relay keeps its own
copy, and a proc test pins that the two are equal.

### Exact strings

| Where | Text |
|---|---|
| unit, round (unchanged) | `relay-round-<owner8>-<safe(name)>-<round>` |
| unit, gate | `relay-gate-<owner8>-<safe(name)>-<round>` |
| unit, consult | `relay-consult-<owner8>-<safe(name)>-<round>-<safe(id)>` |
| unit, verify | `relay-verify-<owner8>-<safe(name)>-<round>-<safe(id)>` |
| policy error, bad pattern | `"%s: %s.gate_cpu_quota: must match ^[0-9]+%%$, got %q: %w"` with `path, prefix, sc.GateCPUQuota, ErrBadPolicy` |
| policy error, below 1% | `"%s: %s.gate_cpu_quota: must be at least 1%%, got %q: %w"`, same args |
| `scopeStatusText` suffix | when the spec's `GateCPUQuota != ""`, append `", gate <quota>"` inside the parentheses, e.g. `on (slice relay.slice, 200%, gate 300%)` and `on (gate 300%)` |

`owner8` means exactly what it means in `scopeUnitName`: `"local"` when
`Owner == ""`, the first 8 hex characters of the `ClientID` dir when it
parses, and `"unknown"` otherwise.

## 4. Interface Definitions & Component Contracts

### `scopeUnitNameFor` (pure, `headless.go`)

`scopeUnitNameFor(kind scopeKind, owner, name string, round int, id string) string`

- Returns `"relay-" + kind + "-" + owner8(owner) + "-" + safeUnitPart(name) + "-" + round`,
  followed by `"-" + safeUnitPart(id)` when `id != ""`.
- `scopeUnitName(b)` becomes
  `scopeUnitNameFor(scopeRound, b.Owner, b.Name, b.Round, "")`. Its output is
  unchanged, and the existing `headless_test.go` ~332 assertions pass as they
  are.
- Extract the owner8 logic into a small helper that both use. Do not
  duplicate it.

### `scopeFor` (pure, `headless.go`)

`scopeFor(rt Runtime, kind scopeKind, unit string) *ScopeSpec`

- It returns nil when `rt.Scope == nil`.
- Otherwise it returns a **copy** of `*rt.Scope` with `Unit = unit` and
  `GateCPUQuota = ""`.
- When `kind == scopeGate` and `rt.Scope.GateCPUQuota != ""`, it sets
  `CPUQuota = rt.Scope.GateCPUQuota`.
- It never mutates `rt.Scope`.

`startRound` replaces its inline `&ScopeSpec{…}` literal with
`spec.Scope = scopeFor(rt, scopeRound, scopeUnitName(b))`. The result must be
field-for-field what the literal built, and `TestStartRoundSetsScope` must
pass unchanged.

### `reserveConsult` (`ask.go` ~244), changed signature

Old: `(store.Consult, string, error)`, meaning `(consult, cwd, err)`.
New: `(store.Consult, string, string, error)`, meaning
`(consult, cwd, owner, err)`. `owner` is the loaded binding's `Owner`. Update
both callers, and nothing else.

### The four spawn sites

| Site | Added to its `ProcSpec` |
|---|---|
| `gateStep` | `Scope: scopeFor(rt, scopeGate, scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, ""))` |
| headless consult (`ask.go` ~198) | `Scope: scopeFor(rt, scopeConsult, scopeUnitNameFor(scopeConsult, owner, opts.Name, consult.Round, consult.ID))` |
| session consult (`ask.go` ~423) | same as the headless consult |
| verify (`verify.go` ~288) | `Scope: scopeFor(rt, scopeVerify, scopeUnitNameFor(scopeVerify, b.Owner, b.Name, round, id))`, where `round` and `id` are the variables already in scope there for the consult's paths |

No other field of any `ProcSpec` changes.

### `tailLines` (`gate.go` ~148)

It now skips any line that starts with `RusageTrailerPrefix`, before it
counts the last `n` non-empty lines. Nothing else changes: the
`relay-exit:<n>` line is still included, as it is today.

### `scopeFromPolicy` (`cmd/relay/serve.go` ~195)

It also copies `sc.GateCPUQuota` into `spec.GateCPUQuota`. `scopeStatusText`
gains the suffix in the strings table.

## 5. High-Level Pseudocode

```
policy.Load → validateScope(block):
    ... existing checks ...
    if sc.GateCPUQuota != "": same two checks as cpu_quota, with the gate_cpu_quota field name

cmd wiring (unchanged flow): pol.ScopeFor(served) → scopeFromPolicy → Runtime.Scope (template, now with GateCPUQuota)

any spawn site:
    unit  = scopeUnitNameFor(kind, owner, name, round, id)
    spec.Scope = scopeFor(rt, kind, unit)     // nil when scopes are off
    rt.Runner.Start(spec)                     // proc drops the scope itself if its lazy probe failed

scopeFor(rt, kind, unit):
    if rt.Scope == nil: return nil
    s = *rt.Scope; s.Unit = unit; s.GateCPUQuota = ""
    if kind == gate and rt.Scope.GateCPUQuota != "": s.CPUQuota = rt.Scope.GateCPUQuota
    return &s

gate FAIL payload / repair plan:
    tail = tailLines(log, n)                  // now skips "relay-rusage:" lines
```

## 6. Error Handling Strategy

- **Bad `gate_cpu_quota`**: a `policy.Load` error wrapping `ErrBadPolicy`, and
  it names the block. The daemon and every CLI verb refuse to start, as they
  do for a bad `cpu_quota`. This is not recoverable at runtime.
- **Scope start failure**: no new handling. A `Runner.Start` error is already
  handled at each site: the gate records `Result: "error"`, and consults and
  verify record `spawn failed: …`. With a scope, `systemd-run` failing is one
  more way `Start` can fail, and it lands in the same place.
- **Host without systemd**: `proc.Runner`'s lazy probe drops the scope. No
  change is needed.
- **Side effect to know about (this corrects the issue):** #313 says the
  rusage trailer "only fires inside a `relay-round-*.scope`". That is not
  quite true. The supervisor's guard is on `want`, the unit `Start` asked for
  (`internal/proc/proc.go` `buildArgv`), so **any** scoped spawn prints a
  `relay-rusage:` line before its `relay-exit:` trailer. For a gate, stream
  and log are the same file, so a scoped gate's log gains that line. Two
  consequences:
  - `ExitCode` is unaffected, because it reads only the last line.
  - The FAIL payload's 5-line tail and the repair plan's tail would carry the
    rusage line and lose one line of real output. Step 4 filters it in
    `tailLines`.

  Consults and verify read their streams through `transcript`, which already
  ignores non-JSON lines (`internal/transcript/final.go`). Gate rusage is still
  **not recorded**: nothing parses the line for a gate.
- **Logging**: no new log lines.

## 7. Ordered Implementation Steps

Run `make check` after every step; the step is not done until it passes. All
new tests go in `internal/policy`, `internal/relay` or `internal/proc`. The
**only** `cmd/relay` test change allowed is extending the existing pure
`TestScopeFromPolicy` table and a `scopeStatusText` table if one exists. It
spawns nothing and reads no config. Add no test in `cmd/relay` that runs a
subcommand: CI runners have no harness binary and no network.

1. **Policy field.** Add `ScopePolicy.GateCPUQuota` and its `validateScope`
   rule.
   *Verify:* `internal/policy` tests add good and bad `gate_cpu_quota` rows
   for **both** `scope` and `serve.scope` ("300%" loads; "300", "0%" and
   "abc%" are refused), and each error names the field and the block.

2. **Template field and `scopeFromPolicy`.** Add `ScopeSpec.GateCPUQuota` and
   `RusageTrailerPrefix`. Make `scopeFromPolicy` copy the new field, and add
   the suffix to `scopeStatusText`.
   *Verify:* the `TestScopeFromPolicy` table gains a row where
   `GateCPUQuota: "300%"` passes through. A `scopeStatusText` check (extend
   the existing table if there is one; otherwise one pure assertion in
   `serve_test.go`) covers the `gate` suffix. A new `internal/proc` test
   asserts `relay.RusageTrailerPrefix == proc.RusageTrailer`.

3. **Unit names and `scopeFor`.** Add `scopeKind`, `scopeUnitNameFor`, the
   owner8 helper and `scopeFor`. Route `scopeUnitName` and `startRound`
   through them.
   *Verify:*
   - `TestStartRoundSetsScope` and the existing `scopeUnitName` assertions
     pass **unchanged**.
   - A new `scopeUnitNameFor` table covers each kind, `id` present and absent,
     an owned binding (owner8 from a real `ClientID`), and a name that needs
     sanitising.
   - A new `scopeFor` table covers: nil template gives nil; round and consult
     keep `CPUQuota`; a gate with `GateCPUQuota` set uses it; a gate without
     it falls back to `CPUQuota`; the returned `GateCPUQuota` is always "";
     and the template is not mutated (compare it before and after).

4. **Gate.** Scope the gate `ProcSpec` and filter the rusage line in
   `tailLines`.
   *Verify:*
   - A `gate_test.go` test sets `rt.Scope` to a template with
     `CPUQuota: "150%", GateCPUQuota: "300%"`. After `gateStep` starts the
     gate, the fake runner's recorded spec has `Scope.Unit ==
     "relay-gate-local-<name>-<round>"` and `Scope.CPUQuota == "300%"`.
   - The same test with `rt.Scope == nil` gives `Scope == nil`.
   - A `tailLines` test on a file ending
     `"a\nb\n\nrelay-rusage:cpu_usec=1 mem_peak=2\n\nrelay-exit:2\n"` with
     `n = 3` returns `["a", "b", "relay-exit:2"]`.

   *Mutation:* remove the `Scope:` field from the gate's `ProcSpec` and
   confirm the new gate test fails. Put it back.

5. **Consults.** Change `reserveConsult` to return the owner, and scope both
   consult `ProcSpec`s.
   *Verify:* in `ask_test.go`, both the headless-consult path and the
   session-consult path, run with `rt.Scope` set, record a spec whose
   `Scope.Unit` is `relay-consult-local-<name>-<round>-<consult.ID>` and
   whose `CPUQuota` is the template's `CPUQuota`, **not** its
   `GateCPUQuota`. Set both in the template so the test can tell them apart.

6. **Verify reviewer.** Scope the verify `ProcSpec`.
   *Verify:* a `verify_test.go` test with `rt.Scope` set records a spec whose
   `Scope.Unit` starts `relay-verify-local-<name>-<round>-` and ends with the
   consult's id, with the template's `CPUQuota`.

   *Mutation:* change `scopeFor` so a verify spec gets `GateCPUQuota`, and
   confirm the step 6 test fails. Put it back.

7. **README.** In the **Scopes.** paragraph (~line 556), say that the gate,
   consults and the verify reviewer each get a scope too, and give the unit
   names. Add `gate_cpu_quota` to the list of `scope` keys, with one clause:
   it is the gate's own ceiling, and defaults to `cpu_quota`.
   *Verify:* `make check` passes.

**Out of scope, do not do:** recording gate rusage (`GateRecord`, the
supervisor guard); `AllowedCPUs` pinning (#314); `GOMAXPROCS` (#315); any
change to `relay-round-*` naming or behaviour.

Report: the files you touched and the step each change belongs to, both
mutation checks (what you broke and which named test failed), and what you
found for each of the three halt conditions at the top, even when it held.
