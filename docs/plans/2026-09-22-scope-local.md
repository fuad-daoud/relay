# Part 2 of 2: give local headless builders a scope too (#295)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder. Never run `make check` (the planner runs it);
run the gate in §6 exactly as written. Every command in the foreground; no
sub-agents for edits. Do not touch any file outside your worktree.

**Do not run `systemctl` and do not restart anything**: other rounds may be
running on this machine, and the live restart check in §7 is the planner's,
not yours. `systemd-run` must appear in your work only through the
stub-`PATH` technique in §5.

Design: `docs/specs/2026-09-22-scope-quota-and-local-design.md`. Part 1
(`docs/plans/2026-09-22-scope-quota.md`) added `Policy.Scope`,
`Policy.ScopeFor`, `ScopeSpec.CPUQuota` and
`scopeFromPolicy(sc *policy.ScopePolicy)`; this plan builds on that
signature. If `ScopeFor` is not in the tree, halt: your base is wrong.

## 1. Why, and the one behaviour change

`Runtime.Scope` is set only at `cmd/relay/serve.go:354`. `newRuntime()`
(`cmd/relay/main.go:394-452`) -- the single constructor for the local daemon
**and** every CLI verb -- never sets it, so a `relay bind --headless` round
spawns unscoped: no memory ceiling, no task ceiling, no CPU ceiling, and it
lives inside `relay.service`'s own cgroup.

That last part is a bug with history: `systemctl --user restart relay`
(what `make service` does) kills every running local headless builder,
relay then sees "exited without a report", gates the provider and burns
switches. A scope makes the builder a *sibling* of the service, so a daemon
restart leaves it running -- the local analogue of #244.

`scopeUnitName` (`internal/relay/headless.go:37-52`) already produces
`relay-round-local-<name>-<round>` for a binding with no `Owner`; that
branch is dead code today and this plan makes it live.

**The behaviour change on upgrade:** with no `scope` block configured,
local headless builders move from `relay.service`'s cgroup into their own
scope. No limits are applied (no quota, no memory cap) -- only the location
changes, and with it restart survival. `scope: {"enabled": false}` opts out.
Say this plainly in the README note (§3.4).

## 2. Files

```
internal/proc/proc.go          Runner gains a once-guarded scope probe + fallback
internal/proc/proc_test.go     fallback + probe-once tests (stub PATH)
cmd/relay/main.go              newRuntime sets Scope; cmdDaemon logs one line
cmd/relay/main_test.go         (only if a pure helper is added; no subcommand may reach herdr)
README.md                      the note in §3.4
docs/plans/2026-09-22-scope-local.md   copy of this plan (last step)
```

Do **not** change `internal/serve` or `cmd/relay/serve.go`: the server keeps
its eager startup probe, because `whoami` and `doctor` must report
`scopes: on|unavailable` before any round runs.

## 3. Changes

### 3.1 The Runner's lazy probe (`internal/proc/proc.go`)

`proc.Runner` (proc.go:70-73, today just `KillGrace`) gains two unexported
fields:

```go
probeOnce sync.Once
scopesOK  bool
```

In `Start`, before `buildArgv` decides whether to wrap: if
`spec.Scope != nil`, run the probe exactly once for the lifetime of this
Runner:

```
r.probeOnce.Do(func() {
    if err := ProbeScopes(ctx, spec.Scope.Slice); err != nil {
        slog.Warn("scopes unavailable; builders will run in this process's cgroup", "err", err)
        r.scopesOK = false
        return
    }
    r.scopesOK = true
})
if !r.scopesOK {
    spec.Scope = nil        // local copy; the caller's spec is not mutated
}
```

`Start` takes its spec **by value** (`func (r *Runner) Start(ctx
context.Context, spec relay.ProcSpec)`, proc.go:120), so clearing
`spec.Scope` affects only this call and never the caller's struct.

Rules:
- A Runner that is never asked for a scope never probes.
- After a failed probe, every later `Start` spawns plainly and logs nothing
  more (the `sync.Once` guarantees one log line per process).
- The probe uses the *first* scoped spec's slice. That is correct here
  because one process has one policy; do not add per-slice caching.

### 3.2 `newRuntime` sets the scope (`cmd/relay/main.go:394-452`)

Add to the returned literal:

```go
Scope: scopeFromPolicy(pol.ScopeFor(false)),
```

`pol` is already in scope at main.go:410. This is cheap and syscall-free --
**do not call `ProbeScopes` here**; §3.1 is what handles absence. Every CLI
verb now carries a scope template, which is what makes a round started by
`relay send` land in the same kind of cgroup as one started by the daemon.

`ConfigWatcher.Refresh` (`internal/relay/reload.go:97-131`) reassigns
`rt.Policy` on a policy.json change but does not touch `rt.Scope`; leave it
that way and note it in the report -- a scope change needs a daemon restart,
same as today's server.

### 3.3 The daemon's startup line (`cmdDaemon`, main.go:2112-2185)

Beside the existing `rt.StartedAt`/`rt.HeldGrace` assignments (main.go:2128-2129),
log one line in the same shape the server uses (`serve.go:365`):

```
scopes=on (slice relay.slice, 200%) | scopes=on | scopes=off
```

`off` means `rt.Scope == nil` (i.e. `scope.enabled: false`). There is no
`unavailable` here, because nothing has probed yet at that point -- that
word would be a lie. If the probe later fails, §3.1's one-line warning is
what says so.

### 3.4 README note

In the headless-builders section, state: a local headless round runs in its
own transient systemd scope named `relay-round-local-<binding>-<round>`;
it therefore survives `systemctl --user restart relay`; `policy.json`'s
top-level `scope` block configures it (`enabled`, `slice`, `cpu_weight`,
`cpu_quota`, `memory_max`, `tasks_max`), `serve.scope` replaces it entirely
for served rounds, and on a host without a usable systemd user manager
relay logs one warning and runs builders unscoped as before.

## 4. Behaviour that must not change

- A host where `systemd-run` is missing or refused behaves exactly as
  today: one warning, unscoped builders, no round failures.
- `relay status`, `relay show` and every other read-only verb must not
  spawn `systemd-run`. If you find a path where constructing a Runtime
  causes a probe, you have put the probe in the wrong place.
- Pane builders are untouched: they are herdr panes, not processes relay
  spawns.
- The four spawn paths that bypass `startRound` (`gate.go:44`,
  `ask.go:212`, `ask.go:468`, `verify.go:288`) stay unscoped in this plan.
  Do not scope them here; they are named as a follow-up in the design.

## 5. Tests

`internal/proc/proc_test.go`, using the stub-`PATH` technique from
`scope_test.go:185-211` (`writeStub` + `t.Setenv("PATH", ...)`):

- `TestStartFallsBackWhenScopeProbeFails`: stub `systemd-run` that exits 1
  with a stderr line; `Start` with a `Scope` set still spawns (the stub is
  never used for the real command because the fallback drops the wrap), the
  stream gets the normal `relay-exit:` trailer, and `Alive`/`ExitCode`
  behave as for any plain spawn. Assert the process really ran (e.g. the
  command writes a known line to the stream).
- `TestStartProbesOnlyOnce`: the same failing stub, but counting
  invocations by having the stub append a line to a file in `t.TempDir()`;
  two `Start` calls with a scope must leave exactly one line.
- `TestStartWithoutScopeNeverProbes`: a stub that appends on every call;
  two `Start` calls with `Scope == nil` leave the file absent or empty.

Do not write a test that calls the real `systemd-run`: CI has none, and
macOS runners would fail.

Mutation-check before reporting: replace `sync.Once` with an unconditional
probe and confirm `TestStartProbesOnlyOnce` fails; restore by re-editing
(never `git checkout`).

## 6. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/proc/ ./internal/relay/ ./cmd/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Commits: `feat(scope): probe once in the runner and fall back unscoped
(#295)`, `feat(scope): local headless builders run in their own scope
(#295)`, `docs(readme): local scopes`, and the plan copy as
`chore(plans): scope local`.

## 7. Not yours -- the planner runs these after the round

- `make check`, `make e2e`.
- The live proof: one local headless binding, a round in flight,
  `systemctl --user restart relay`, then the builder's pid still alive in
  `relay-round-local-<name>-<round>.scope` and the round closing normally.
- Deciding a default `cpu_quota` from measured `relay show` rusage.

## Report

The mutation check both ways, the gate tail, commit shas, every exported
identifier added or changed, and anything you did that this plan did not
say.
