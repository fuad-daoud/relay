# Bounding a round: CPUQuota on the scope, and a scope for local headless builders

**Issue:** #295. **Depends on:** nothing open; builds on the scope work in
#244/#216 (`2026-09-21-serve-queue-and-scopes-design.md`) and the cap in
#285.
**Amends:** `policy.json` gains a top-level `scope` block and
`scope.cpu_quota`; `relay.ScopeSpec` gains `CPUQuota`; `proc.Runner` gains a
lazy scope probe; `remote.BuildersView` gains `quota`.
**Status:** design, not yet implemented. Plans:
`docs/plans/2026-09-22-scope-quota.md` (part 1),
`docs/plans/2026-09-22-scope-local.md` (part 2).

## 1. System overview

#244/#216 put every **served** round in a transient systemd scope and #285
capped how many run at once. Neither bounds what one round does to the box,
and neither applies off the server:

- `ScopeSpec` (`internal/relay/runner.go:20-28`) carries `CPUWeight`, which
  is `cpu.weight` -- a *proportional share that only bites under
  contention*. One round on an idle 16-core box still takes all 16. There
  is no `CPUQuota` in `ScopeArgv` (`internal/proc/scope.go:29-45`).
- `Runtime.Scope` is set in exactly one place, `cmd/relay/serve.go:354`.
  `newRuntime()` (`cmd/relay/main.go:394-452`), the single constructor for
  the local daemon **and** all ~45 CLI verbs, never sets it, so a
  `relay bind --headless` round spawns with no scope: no memory ceiling, no
  task ceiling, no CPU anything. `scopeUnitName` already computes
  `relay-round-local-<name>-<round>` for a binding with no `Owner`
  (`headless.go:37-52`) -- dead code today.

The CPU is not the harness: `claude -p` is blocked on the API for most of a
round, and the cores go to what the round *spawns* (`go test ./...`,
`go build`, linters at `GOMAXPROCS = nproc`). That is the argument for
doing this at the cgroup rather than per harness: the scope covers the whole
process tree including every tool call.

This design does two things:

1. **`cpu_quota` on the scope** -- a hard ceiling in systemd's own units
   (`"200%"` = two cores' worth), plumbed exactly as `memory_max` already
   is, and surfaced wherever the slice already is.
2. **Scope local headless builders too** -- the same scope treatment for
   `relay bind`/`relay add --headless` rounds, from a new top-level `scope`
   block that `serve.scope` overrides as a whole.

### The side effect that matters most

A local builder in its own scope is a *sibling* of `relay.service`, not a
child. So `systemctl --user restart relay` (what `make service` does) stops
being able to kill running local builders -- the local analogue of #244,
and a bug that has bitten this project repeatedly: a second planner's
`make service` once killed two live rounds in the same second, and relay
then gated the provider for "exiting without a report". Part 2 fixes that
class locally, and it is directly testable with one local headless builder.

### Decisions taken

- **Whole-block override, not field merge.** A top-level `scope` applies to
  both contexts; when `serve.scope` exists it replaces it *entirely* for
  served rounds. No field-by-field inheritance, so there is never a question
  of what an empty field means, and contabo's existing `serve.scope` keeps
  behaving exactly as it does today.
- **No default quota.** `cpu_quota` defaults to `""` (omitted); behaviour is
  unchanged until an operator sets one. The measured numbers we have (8.5 s
  and 19.7 s of CPU per round) are totals, not rates, and cannot tell us a
  safe ceiling. `relay show`'s rusage is the instrument for picking one
  later.
- **Local scopes default ON.** With no quota and no memory cap configured,
  the only change for an existing user is *where* the process sits in the
  cgroup tree -- and that change is the restart-survival win above. Opt out
  with `scope.enabled: false`. This is the one behaviour change on upgrade
  and it is called out in the README note.
- **The probe moves into the Runner, lazily.** See §2.2; this is the only
  part that is not a straight copy of the server's wiring.

### Scope boundary

- No `AllowedCPUs`/pinning (#295 item 3): `cpuset` is not delegated to the
  user manager by default, so it needs its own probe, a `Delegate=cpuset`
  drop-in and a doctor check. Separate round.
- No `ProcSpec.Env`/`GOMAXPROCS` (#295 item 4) until measurement says the
  quota alone schedules badly.
- No per-harness or per-tier quotas, no dynamic quota, no IO throttling.
- **Not scoped by this design, and worth their own issue:** the four spawn
  paths that bypass `startRound` and therefore never get a scope --
  `gate.go:44` (the gate command, i.e. `make check`, the CPU-heaviest thing
  relay runs), `ask.go:212` and `ask.go:468` (consults), `verify.go:288`
  (the reviewer). A quota on rounds that leaves the gate unbounded is a
  half-measure; it is out of scope here only to keep this change reviewable.

## 2. Design

### 2.1 Config shape

```
policy.json
{
  "scope":   { ... ScopePolicy ... },          // both contexts
  "serve":   { "scope": { ... } }              // replaces `scope` entirely, served rounds only
}
```

`ScopePolicy` (`internal/policy/policy.go:112-120`) gains one field:

```go
CPUQuota string `json:"cpu_quota,omitempty"` // "" = omit; else ^[0-9]+%$, >= 1%
```

`Policy` gains `Scope *ScopePolicy \`json:"scope,omitempty"\`` beside
`Serve` (policy.go:100). Validation for the two blocks is one function
called twice with a prefix (`"scope"` / `"serve.scope"`), so the five
existing error strings keep their exact shape (policy.go:415-433) and the
new one reads
`policy.json: serve.scope.cpu_quota: must match ^[0-9]+%$, got "200"`.

Resolution, one accessor, replacing the direct struct reach in
`scopeFromPolicy`:

```go
// ScopeFor returns the scope block that applies to a context. A served
// round takes Serve.Scope when it is set, else the top-level Scope; nil
// means "defaults" (enabled, no limits), not "disabled".
func (p Policy) ScopeFor(served bool) *ScopePolicy
```

`nil` must keep meaning *enabled with defaults*, because that is what
`serve.scope: absent` means today on a box with scopes on.

### 2.2 Where the probe lives

Today `cmd/relay/serve.go:322-337` probes eagerly at startup and sets
`Config.Scope = nil` on failure. That cannot simply be copied to the local
side:

- `newRuntime()` is shared by every CLI verb, so probing there would make
  `relay status` shell out to `systemd-run` (10 s timeout) on every
  invocation.
- Probing only in `cmdDaemon` (main.go:2122, where `StartedAt`/`DB` are
  set) would scope rounds the daemon starts (switch, repair, the #244
  relaunch) but *not* rounds `relay send` starts directly from the CLI
  (`send.go:371` calls `startRound` in the caller's process). Same machine,
  same binding, different cgroup depending on who spawned it.

So the fallback moves into the Runner, where the spawn actually happens:

```go
// (*proc.Runner) gains, unexported:
//   probeOnce sync.Once
//   scopesOK  bool
// On the first Start with spec.Scope != nil, run ProbeScopes(spec.Scope.Slice).
// On failure: log once at Warn ("scopes unavailable; builders will run in
// the daemon's cgroup", err) and set scopesOK=false. While !scopesOK every
// subsequent Start ignores spec.Scope and spawns plainly.
```

Properties this buys: a machine with no `systemd-run` (macOS, a container,
CI) degrades to today's behaviour instead of failing every round with a
spawn error; `relay status` never probes because it never spawns; and every
local spawn path agrees, whichever process it happens in.

`newRuntime()` then sets `Scope: scopeFromPolicy(pol.ScopeFor(false))`
unconditionally and cheaply -- no probe, no syscall.

The server keeps its eager startup probe, because `whoami`/`doctor` must
report `scopes: on|unavailable` before any round runs. In the success case
that is one extra probe per process, which is a `true` in a scope.

### 2.3 Plumbing the quota

`ScopeSpec` (`runner.go:20-28`) gains `CPUQuota string`. Three places copy
scope fields one by one and all three need the new field:

- `scopeFromPolicy` (`cmd/relay/serve.go:189-214`), which becomes
  `scopeFromPolicy(sc *policy.ScopePolicy) *relay.ScopeSpec` so both
  contexts share it.
- `startRound`'s block (`internal/relay/headless.go:143-157`).
- `ScopeArgv` (`internal/proc/scope.go:29-45`), emitting
  `-p CPUQuota=<v>` **immediately after the `CPUWeight` pair** and before
  `MemoryMax`. `TestScopeArgv` compares whole argv slices with
  `reflect.DeepEqual`, so every existing row moves; that is intended and the
  rows are rewritten, not relaxed.

### 2.4 Surfaces

- `remote.BuildersView` (`internal/remote/proto.go:128-135`) gains
  `Quota string \`json:"quota,omitempty"\``; `handleWhoAmI`
  (`internal/serve/routes.go:109-113`) fills it from `s.cfg.Scope`.
- `buildersText` (`cmd/relay/doctor.go:395-408`) and `RenderServers`
  (`internal/relay/remote.go:890-925`) print `scopes on (relay.slice, 200%)`
  when a quota is set, `scopes on (relay.slice)` when not. These two render
  the same words in two places today; this design does **not** unify them
  (that is a separate cleanup) but the plan requires both to change
  together, with one test each.
- `relay serve`'s startup line (`cmd/relay/serve.go:365`) already prints
  `scopes=on (slice X)`; it gains the quota the same way.
- The local daemon logs its own one-line equivalent at start
  (`cmdDaemon`), so a laptop user can see whether local scopes are on.

## 3. Error handling

| Situation | Class | Handling |
|---|---|---|
| Bad `cpu_quota` in either block | non-recoverable at load | `policy.Load` error names the block and field; the daemon and every CLI verb refuse to start, as for the other five |
| `systemd-run` missing / user manager refuses | recoverable | first scoped `Start` probes, logs once, and runs unscoped for the process lifetime |
| Quota set below what a round needs | expected, not an error | the round is slower; `relay show`'s `cpu`/`peak` is how it is noticed |
| Local scope fails only for one round | n/a | not possible: the probe result is per process, not per round |

## 4. Testing

- `internal/policy`: `cpu_quota` good/bad rows for **both** blocks;
  `ScopeFor` table (serve.scope present -> it wins whole; absent -> top
  level; both absent -> nil); the unknown-key guard still rejects
  `{"scope":{"frobnicate":true}}`.
- `internal/proc`: `TestScopeArgv` rows updated, one with a quota and one
  without; a new `TestStartFallsBackWhenProbeFails` using the existing
  stub-`PATH` technique (`scope_test.go:185-211`) -- a `systemd-run` stub
  that exits 1 makes `Start` spawn plainly and still produce the exit
  trailer, and a second `Start` must not probe again.
- `internal/relay`: `startRound` copies `CPUQuota` onto the spec;
  `scopeUnitName` for a local binding is `relay-round-local-<name>-<round>`.
- `cmd/relay`: `scopeFromPolicy` table (pure). No test may execute a
  subcommand that reaches herdr, and none may call `ProbeScopes` for real.
- **Live check, planner-run, part 2 only:** one local headless binding, a
  round in flight, `systemctl --user restart relay`, and the builder's pid
  is still alive in `relay-round-local-<name>-<round>.scope` with the round
  closing normally afterwards. This is the whole point of part 2 and no
  unit test can stand in for it.

## 5. Rounds

1. **Part 1 -- quota** (`docs/plans/2026-09-22-scope-quota.md`): the policy
   field and validation, `ScopeFor`, `ScopeSpec.CPUQuota`, `ScopeArgv`,
   `startRound`, `scopeFromPolicy`, and the four surfaces. Server-side
   behaviour only; nothing local changes yet.
2. **Part 2 -- local scopes** (`docs/plans/2026-09-22-scope-local.md`): the
   Runner's lazy probe and fallback, `newRuntime` setting `Scope`, the
   daemon's startup line, the README note about the upgrade behaviour
   change, and the live restart check.

Part 2 depends on part 1 only for `ScopeFor`; if they are dispatched
together, part 2's builder must be told part 1's branch is its base.
