# Plan: a scoped spawn gets `GOMAXPROCS` equal to the CPUs its scope allows (#315)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of these is false:

- **`proc.Runner.Start` makes both scope fallbacks before it builds the child
  environment.** The scope probe may drop `spec.Scope`, and the pin probe may
  clear `AllowedCPUs` on a copy (`internal/proc/proc.go` ~174–209). Both run
  before `cmd.Env = ChildEnv(os.Environ(), DeniedEnv, spec.Env)` (~215). This
  plan derives `GOMAXPROCS` from the scope that is **actually launched**, so it
  must run after both fallbacks.
- **`ChildEnv` appends `extra` after the parent**, and `os/exec` keeps the
  **last** value of a duplicated name. The plan never relies on this, because
  it adds nothing when the name is already present. If you find the opposite
  precedence documented or tested anywhere, say so.
- **`internal/relay` may import `internal/policy`** (`cpus.go` already does,
  for `policy.ParseCPUList`), and **`internal/proc` imports `internal/relay`**.
  The pure rule lives in relay and proc calls it.
- **No candidate or harness mechanism lets a user set a builder's
  environment.** `candidate.Candidate` has `ExtraArgs` and no env field. If one
  exists, an explicit `GOMAXPROCS` there must win. Halt and report where it is.

## 1. System Overview

`cpu_quota` (#308) caps how much CPU a round may use, and `allowed_cpus`
(#314, #362) pins each round to one core. A Go process started inside such a
scope may still size itself for the whole machine: `GOMAXPROCS` and, through
it, `go build`/`go test` parallelism (`-p` and `-parallel` both default to
`GOMAXPROCS`). The #314 benchmark measured single-core jobs run with
`GOMAXPROCS=1`.

**What Go already does, and why relay still sets the value:**

- Go has always derived its default `GOMAXPROCS` from the CPU affinity mask
  (`sched_getaffinity`). A process pinned by `AllowedCPUs` (cpuset) already
  sees one CPU. Setting `GOMAXPROCS=1` explicitly for a pinned round is
  **redundant but harmless**. It is also correct when the pin was not
  applied, which the pin fallback handles (§5).
- Go 1.25+ also reads the cgroup CPU **bandwidth** limit (`cpu.max`, which is
  what `CPUQuota` sets), but only when the **main module's** `go` line is 1.25
  or later (the `containermaxprocs` GODEBUG default). A test binary built from
  a repo whose `go.mod` says `go 1.22` ignores the quota, and so does any Go
  program built for an older module. The value this change adds is therefore
  **quota-only rounds in repos below Go 1.25, and every non-toolchain Go
  binary a round runs**.

So relay sets `GOMAXPROCS` explicitly for any scoped spawn that has a quota or
a pin, and leaves it alone otherwise. It is Go-only by nature; the README says
so.

**Decisions:**

1. **The rule**, a pure function in relay: count the CPUs the scope may run
   on. With `AllowedCPUs` set, that is the number of CPUs in the list (1 for a
   pinned round). With `CPUQuota` set, it is `ceil(percent / 100)`, at least 1.
   With both, it is the smaller of the two. With neither, it is **not set**.
2. **Where:** in `proc.Runner.Start`, **after** both probe fallbacks, from the
   scope actually launched. That is the only place that knows whether the pin
   or the whole scope was dropped, so a round whose pin was refused gets the
   quota-derived value (or none), never a stale `1`. It covers every scoped
   spawn (round, gate, consult, verify) in one place, with no call site
   changes.
3. **Precedence:** relay adds `GOMAXPROCS` **only when neither the parent
   environment nor `spec.Env` already has it**. A user who exports
   `GOMAXPROCS` for the daemon or the served process keeps their value.
4. **No `GOFLAGS=-p`:** the go command's `-p` defaults to `GOMAXPROCS`, and so
   does `go test -parallel`. Touching `GOFLAGS` would add a merge rule for no
   gain.
5. **No new policy knob:** it is on whenever a quota or a pool is set, both of
   which are already opt-in. An unset scope block changes nothing, and neither
   does a host without scopes (where `spec.Scope` is dropped).

## 2. File Structure

```
internal/relay/runner.go      MODIFY  GoMaxProcsFor (pure rule) next to ScopeSpec
internal/relay/runner_test.go MODIFY or CREATE  GoMaxProcsFor table (say which)
internal/proc/env.go          MODIFY  goMaxProcsEnv (pure: which entry, if any, to add)
internal/proc/env_test.go     MODIFY  goMaxProcsEnv table
internal/proc/proc.go         MODIFY  Start appends goMaxProcsEnv's entry after both fallbacks
internal/proc/proc_test.go    MODIFY  Start-level tests: env after the pin fallback, after the scope fallback, parent wins
README.md                     MODIFY  the **Scopes.** paragraph (~575): one sentence
```

## 3. Data Structures & Type Definitions

No new types or fields.

### Exact values

| Scope launched | `GOMAXPROCS` added |
|---|---|
| `nil` (no scopes, or the scope probe failed) | none |
| no `CPUQuota`, no `AllowedCPUs` | none |
| `AllowedCPUs: "2"` | `1` |
| `AllowedCPUs: "0-2"` | `3` |
| `AllowedCPUs: "0,2-3"` | `3` |
| `CPUQuota: "200%"` | `2` |
| `CPUQuota: "150%"` | `2` |
| `CPUQuota: "50%"` | `1` |
| `CPUQuota: "300%"`, `AllowedCPUs: "1"` | `1` (the smaller) |
| `CPUQuota: "100%"`, `AllowedCPUs: "0-3"` | `1` |
| `AllowedCPUs: "2"` refused by the pin probe, `CPUQuota: "200%"` | `2` |
| `AllowedCPUs: "2"` refused by the pin probe, no quota | none |
| any of the above, with `GOMAXPROCS` already in the parent env or in `spec.Env` | none; the existing value stands |

An unparseable `AllowedCPUs` or `CPUQuota` contributes nothing, as if it were
unset. It cannot happen after `policy.Load`, but the rule must not panic.

## 4. Interface Definitions & Component Contracts

### `relay.GoMaxProcsFor(s ScopeSpec) (int, bool)` (pure, `runner.go`)

- Responsibility: the number of CPUs a process launched with `s` may use, per
  §3. `ok == false` means "no limit, set nothing".
- It counts `AllowedCPUs` with `policy.ParseCPUList`. It parses `CPUQuota` as
  digits followed by `%`, then takes `ceil(n / 100)` with a minimum of 1.
- It reads nothing else. `GateCPUQuota` is template-only and is always empty
  on a launched spec.

### `proc.goMaxProcsEnv(parent, extra []string, scope *relay.ScopeSpec) []string` (pure, `env.go`)

- Returns `nil` when `scope == nil`, when `GoMaxProcsFor(*scope)` is not ok,
  or when a `GOMAXPROCS=` entry (or a bare `GOMAXPROCS`) is in `parent` or in
  `extra`.
- Otherwise it returns `[]string{"GOMAXPROCS=<n>"}`.
- It never mutates its inputs.

### `(*proc.Runner).Start`, changed

After both fallbacks and before `cmd.Env` is built:
`spec.Env = append(spec.Env[:len(spec.Env):len(spec.Env)], goMaxProcsEnv(os.Environ(), spec.Env, spec.Scope)...)`.
The full-slice expression forces a copy, so the caller's backing array is
never written. `spec` is a value copy, but `spec.Env` shares the caller's
array. Then `ChildEnv` runs as today.

## 5. High-Level Pseudocode

```
Start(spec):
    ... existing checks ...
    scope fallback   (may set spec.Scope = nil)
    pin fallback     (may re-point spec.Scope at a copy with AllowedCPUs = "")
    extra = goMaxProcsEnv(os.Environ(), spec.Env, spec.Scope)
    spec.Env = copy-then-append(spec.Env, extra)
    argv = buildArgv(spec, bin)
    cmd.Env = ChildEnv(os.Environ(), DeniedEnv, spec.Env)
    ...

GoMaxProcsFor(s):
    n, have = 0, false
    if s.AllowedCPUs != "": if cpus, err = ParseCPUList(...); err == nil and len(cpus) > 0: n, have = len(cpus), true
    if s.CPUQuota != "" and parses as P%:
        q = max(1, ceil(P / 100))
        n = have ? min(n, q) : q; have = true
    return n, have
```

## 6. Error Handling Strategy

Nothing here can fail a spawn. A malformed value contributes nothing, and a
user-set `GOMAXPROCS` wins silently. No new log lines: the pin and scope
fallbacks already warn once each.

## 7. Ordered Implementation Steps

Run `make check` after each step, and check gofmt with
`gofmt -l $(git ls-files '*.go')`. The dev-server copy of a worktree has no
`.git`, so the Makefile's own gofmt line may check nothing there. All tests go
in `internal/relay` and `internal/proc`. **Add no test in `cmd/relay`.** CI
runners have no harness binary and no network.

1. **The rule.** Add `GoMaxProcsFor` and its doc comment, which states that it
   is Go-only and why it is set even though Go honours the affinity mask.
   *Verify:* a table test covers every "Scope launched" row in §3 that does
   not involve a probe or precedence, plus a malformed quota (`"abc"`) and a
   malformed cpu-list (`"x"`), both giving "not set".

2. **The env helper.** Add `goMaxProcsEnv`.
   *Verify:* a table test covers nil scope; no limit; a single core → `1`;
   quota only → `2`; a parent carrying `GOMAXPROCS=8` → nil; `extra` carrying
   `GOMAXPROCS=4` → nil; and a bare `GOMAXPROCS` in the parent → nil. Inputs
   are unchanged after each call (compare copies).

3. **Wire it into `Start`,** after both fallbacks and with the copy-then-append.
   *Verify:* follow the existing Start tests' pattern
   (`TestStartFallsBackWhenScopeProbeFails`, `TestStartWrapsArgvWithScope`, and
   the #362 pin-fallback test) with the same fake `systemd-run` or probe setup
   they use. Name the tests:
   - `TestStartSetsGoMaxProcsFromThePin`: a scope with `AllowedCPUs: "1"` and
     the pin probe OK. The child's environment (have the argv print `env`, as
     `TestStartRunsInDirWithExtraEnv` does) contains `GOMAXPROCS=1`.
   - `TestStartGoMaxProcsFollowsThePinFallback`: `AllowedCPUs: "1"` and
     `CPUQuota: "200%"`, with the pin probe **failing**. The child sees
     `GOMAXPROCS=2`, not `1`.
   - `TestStartNoGoMaxProcsWithoutScopes`: the scope probe fails, so no
     `GOMAXPROCS` is added. Unset any inherited `GOMAXPROCS` for the test
     with `t.Setenv` (set it to empty, then unset it).
   - `TestStartParentGoMaxProcsWins`: `t.Setenv("GOMAXPROCS", "7")` with a
     pinned scope; the child sees `7`.
   - `TestStartDoesNotMutateCallerEnv`: the caller's `spec.Env`, with spare
     capacity, is unchanged after `Start`.

   *Mutation (required):*
   - (1) Make `GoMaxProcsFor` return `(1, true)` whenever any limit is set.
     The step-1 table's pool and quota rows must fail. Restore it.
   - (2) Compute `goMaxProcsEnv` **before** the pin fallback, i.e. from the
     original scope. `TestStartGoMaxProcsFollowsThePinFallback` must fail.
     Restore it.

4. **README.** In the **Scopes.** paragraph (~575), add one sentence: a scoped
   builder, gate, consult or verify reviewer gets `GOMAXPROCS` set to the CPUs
   its scope allows (1 for a pinned round, `ceil(quota)` otherwise), unless
   the environment already sets it. This affects Go processes only; Go's
   `-p`/`-parallel` follow it.
   *Verify:* `make check` passes.

## Report

List the files touched per step. Give the two mutation checks: what you
changed and which named test failed. For each halt condition, give what you
found, with file:line. Say whether any existing proc test needed its
environment isolated from an inherited `GOMAXPROCS`.
