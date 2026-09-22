# Part 1 of 2: a hard CPU ceiling on a round's scope (#295)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder. Never run `make check` (the planner runs it);
run the gate in §6 exactly as written. Every command in the foreground; no
sub-agents for edits. Do not touch any file outside your worktree. **Do not
run `systemd-run` or any `systemctl` command**: every test here is a pure
function or uses the stub-`PATH` technique described in §5.

Design: `docs/specs/2026-09-22-scope-quota-and-local-design.md` (in this
tree). Part 2 (scoping the local daemon's builders) is a separate plan and
is **not** your work: do not set `Runtime.Scope` in `cmd/relay/main.go`.

## 1. Why

`ScopeSpec.CPUWeight` is `cpu.weight` -- a proportional share that only
bites under contention. One round on an idle box still takes every core, so
a single `go test ./...` saturates a 4-core server and makes the daemon's
own tick compete with it. `CPUQuota` is the hard ceiling (systemd's units:
`"200%"` = two cores' worth). This part adds it and surfaces it; it changes
nothing about *which* rounds get a scope.

Default stays `""` (omitted) -- behaviour is unchanged until an operator
sets a quota. Do not invent a default.

## 2. Files

```
internal/policy/policy.go         Policy.Scope; ScopePolicy.CPUQuota; shared validateScope; ScopeFor
internal/policy/policy_test.go    quota rows for both blocks; ScopeFor table; unknown-key guard
internal/relay/runner.go          ScopeSpec.CPUQuota
internal/proc/scope.go            ScopeArgv emits -p CPUQuota
internal/proc/scope_test.go       TestScopeArgv rows rewritten (+ a quota row)
internal/relay/headless.go        startRound copies CPUQuota
internal/relay/headless_test.go   assert it lands on the spec
internal/remote/proto.go          BuildersView.Quota
internal/serve/routes.go          handleWhoAmI fills Quota
internal/serve/serve_test.go      TestWhoAmI expectation
cmd/relay/serve.go                scopeFromPolicy takes *policy.ScopePolicy; startup line shows the quota
cmd/relay/serve_test.go           scopeFromPolicy table
cmd/relay/doctor.go               buildersText prints the quota
cmd/relay/doctor_test.go          one row with, one without
internal/relay/remote.go          RenderServers prints the quota (same words as buildersText)
internal/relay/remote_test.go     TestRenderServersBuilders extended
docs/specs/2026-09-22-scope-quota-and-local-design.md   the design (already in the tree; do not edit)
docs/plans/2026-09-22-scope-quota.md                    copy of this plan (last step)
```

Before adding any field, grep the struct for an existing one with the same
meaning; if one exists, halt and report rather than duplicate it.

## 3. Changes

### 3.1 `internal/policy/policy.go`

Add to `ScopePolicy` (currently policy.go:112-120), after `MemoryMax`:

```go
CPUQuota string `json:"cpu_quota,omitempty"` // "" = none; else ^[0-9]+%$, at least 1%
```

Add to `Policy`, beside `Serve` (policy.go:100):

```go
// Scope is the systemd scope template for rounds this host runs. Serve.Scope
// replaces it entirely for served rounds (#295). nil means defaults, not off.
Scope *ScopePolicy `json:"scope,omitempty"`
```

Add the pattern beside `memoryMaxPattern` (policy.go:31):

```go
var cpuQuotaPattern = regexp.MustCompile(`^[0-9]+%$`)
```

**Factor the existing validation** at policy.go:419-432 into

```go
func validateScope(path, prefix string, sc *ScopePolicy) error
```

which returns exactly today's five errors with `prefix` substituted for the
literal `serve.scope` (so `serve.scope.slice: must end in ".slice"` is
byte-identical to today), plus a sixth:

```
%s: %s.cpu_quota: must match ^[0-9]+%%$, got %q: %w
```

and reject `"0%"` with

```
%s: %s.cpu_quota: must be at least 1%%, got %q: %w
```

Call it twice inside `Load`, after the `serve.max_builders` check
(policy.go:415-417): `validateScope(path, "scope", p.Scope)` and
`validateScope(path, "serve.scope", p.Serve.Scope)` (the latter only when
`p.Serve != nil`). A nil block is not an error.

Add the accessor:

```go
// ScopeFor returns the scope block that applies to a context: a served
// round takes Serve.Scope when set, else the top-level Scope. nil means
// defaults (enabled, no limits) -- never "disabled".
func (p Policy) ScopeFor(served bool) *ScopePolicy
```

### 3.2 `internal/relay/runner.go`

`ScopeSpec` (runner.go:20-28) gains, after `MemoryMax`:

```go
CPUQuota string // "" = omit; systemd units, e.g. "200%" = two cores' worth
```

### 3.3 `internal/proc/scope.go`

In `ScopeArgv` (scope.go:29-45), emit the quota **immediately after the
`CPUWeight` pair and before `MemoryMax`**:

```go
if s.CPUQuota != "" {
    argv = append(argv, "-p", "CPUQuota="+s.CPUQuota)
}
```

Nothing else in that function changes; `ProbeScopes` is untouched (it builds
a `ScopeSpec` with only `Unit`/`Slice`/`CPUWeight`, so it emits no quota --
leave it that way, a probe must not be refused for a quota the box cannot
honour).

### 3.4 `internal/relay/headless.go`

`startRound`'s copy block (headless.go:143-157) gains
`CPUQuota: rt.Scope.CPUQuota,`. This is the only place a round's spec is
built; do not add scope code anywhere else.

### 3.5 `cmd/relay/serve.go`

Change the signature to take the resolved block, so part 2 can reuse it:

```go
func scopeFromPolicy(sc *policy.ScopePolicy) *relay.ScopeSpec
```

Same semantics as today (serve.go:189-214): nil block -> a spec with
`CPUWeight: 100`; `Enabled` explicitly false -> nil; otherwise copy
`Slice`, `CPUWeight` (only when non-zero), `MemoryMax`, `TasksMax` and now
`CPUQuota`. Its caller becomes `scopeFromPolicy(pol.ScopeFor(true))`.

The startup line (serve.go:322-337, :365) gains the quota:
`scopes=on (slice relay.slice, 200%)`, `scopes=on (200%)` with no slice,
and unchanged when no quota is set. Build that string once in a helper if
it is cleaner, but the existing `off`/`unavailable` words must not change.

### 3.6 Surfaces

- `remote.BuildersView` (proto.go:128-135): `Quota string \`json:"quota,omitempty"\``.
- `handleWhoAmI` (routes.go:109-113): fill it from `s.cfg.Scope.CPUQuota`
  when `s.cfg.Scope != nil`.
- `buildersText` (doctor.go:395-408) and `RenderServers` (remote.go:890-925)
  render the same words: `scopes on (relay.slice, 200%)`, `scopes on (200%)`,
  `scopes on (relay.slice)`, `scopes on`, `scopes off`. Change both; a
  mismatch between them is a defect this plan is responsible for.

## 4. Behaviour that must not change

- A policy.json with no `scope` and no `serve.scope` produces exactly
  today's argv for a served round (no `CPUQuota` element).
- `ProbeScopes` argv is unchanged.
- The local daemon still gets no scope (part 2's job): `newRuntime` in
  `cmd/relay/main.go` must be untouched by this plan.

## 5. Tests

`internal/policy/policy_test.go` (follow `TestServeScopeValidation`'s shape,
policy_test.go:919-962):
- `TestScopeQuotaValidation`: good `"200%"`, `"1%"`, `"1000%"`; bad `"200"`,
  `"200 %"`, `"%"`, `"0%"` -- each asserting `errors.Is(err, ErrBadPolicy)`
  and the substring `scope.cpu_quota`. Run every row against **both**
  `{"scope":{...}}` and `{"serve":{"scope":{...}}}`, asserting the prefix in
  the message differs accordingly.
- `TestScopeForWholeBlockOverride`: serve.scope set -> `ScopeFor(true)` is
  that block and **none of** the top-level fields leak into it (set
  different values in each and assert field by field); serve.scope absent ->
  `ScopeFor(true)` is the top-level block; both absent -> nil;
  `ScopeFor(false)` is always the top-level block.
- Extend the existing unknown-key guard to `{"scope":{"frobnicate":true}}`.

`internal/proc/scope_test.go`: rewrite `TestScopeArgv`'s four rows for the
new element order and add a fifth, `"with quota"`, asserting the exact
position of `-p CPUQuota=200%` between the weight and the memory pairs.

`internal/relay/headless_test.go`: extend the existing scope test so
`rt.Scope.CPUQuota = "150%"` appears on the recorded `ProcSpec.Scope`.

`cmd/relay/serve_test.go`: `TestScopeFromPolicy` table -- nil block; quota
passed through; `Enabled:false` -> nil; `CPUWeight:0` -> 100.

`cmd/relay/doctor_test.go` and `internal/relay/remote_test.go`: one probe
with a quota and one without, asserting the exact strings in §3.6.

`internal/serve/serve_test.go`: `TestWhoAmI` asserts `Builders.Quota` is
set when the server has one and empty when it does not.

Mutation-check two of these yourself before reporting, and put both
outputs in the report:
1. Drop the `if s.CPUQuota != ""` block in `ScopeArgv` -> the new
   `TestScopeArgv` row must fail.
2. Make `ScopeFor(true)` return the top-level block unconditionally ->
   `TestScopeForWholeBlockOverride` must fail.
Restore by re-editing, never `git checkout`.

## 6. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/policy/ ./internal/proc/ ./internal/relay/ ./internal/serve/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Commits: one per §3 group is fine, each `feat(scope): ...` naming #295; the
plan copy as `chore(plans): scope quota`.

## Report

Both mutation checks, the gate tail, commit shas, every exported identifier
added or changed with its signature, and anything you did that this plan did
not say -- especially if `buildersText` and `RenderServers` could not be kept
in step.
