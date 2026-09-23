# Plan (round 2): a scope's `GOMAXPROCS` overrides an inherited one (#315)

This is round 2 on branch `relay/gomaxprocs`. Round 1 is commit `6503790` and
is complete. It adds `relay.GoMaxProcsFor`, `proc.goMaxProcsEnv`,
`hasEnvName`, and the append in `proc.Runner.Start` after both scope
fallbacks. Keep all of it, except the one rule this round changes.

If any step is impossible as written or contradicts the code you find, STOP
and report. Halt in particular if:

- `cmd.Env` is **not** built as `ChildEnv(os.Environ(), DeniedEnv, spec.Env)`
  right after the `GOMAXPROCS` append (`internal/proc/proc.go` ~218–223).
- Anything outside `internal/proc` calls `goMaxProcsEnv`, or relies on its
  "parent wins" behaviour.

## 1. System Overview

**Why the rule changes.** Round 1 made an inherited `GOMAXPROCS` win. On the
owner's machine, `~/.config/fish/config.fish` exports `GOMAXPROCS=$(nproc)`
(22). That value reached the systemd user manager and the running relay
daemon. So the scope-derived value was never set, and a round pinned to one
core ran Go with 22 threads on it. A scope limits CPUs only when the operator
opted in, through `cpu_quota` or `allowed_cpus`. That is the more specific
setting. A blanket shell export must not defeat it.

**New rule, decided:**

- When a scoped spawn's scope limits CPUs (`GoMaxProcsFor` returns ok), the
  child gets `GOMAXPROCS=<n>` **even if the daemon's own environment has one**.
  The inherited entry is **removed** from the child's environment, not just
  shadowed, so the child sees exactly one `GOMAXPROCS`.
- `spec.Env` still wins. If relay's own caller put `GOMAXPROCS` in `spec.Env`,
  nothing is added. No caller does today, and no candidate env exists. This
  keeps a seam for a future per-candidate env, which would travel in
  `spec.Env`.
- No limit, a dropped scope, or a refused pin with no quota: nothing changes.
  The inherited `GOMAXPROCS`, if any, passes through untouched, as before #315.

## 2. File Structure

```
internal/proc/env.go        MODIFY  goMaxProcsEnv: drop the parent check; doc comment
internal/proc/env_test.go   MODIFY  TestGoMaxProcsEnv rows
internal/proc/proc.go       MODIFY  Start: deny the parent's GOMAXPROCS when an entry is added; comment
internal/proc/proc_test.go  MODIFY  TestStartParentGoMaxProcsWins → TestStartScopeGoMaxProcsOverridesParent
README.md                   MODIFY  the one sentence at ~583
```

No other file changes.

## 3. Contracts

### `goMaxProcsEnv(parent, extra []string, scope *relay.ScopeSpec) []string`

- The signature is unchanged. `parent` stays a parameter so the call site is
  untouched; it is simply no longer used for the decision. If `go vet` or a
  linter objects to an unused parameter, rename it `_`.
- It returns nil when `scope` is nil, when `GoMaxProcsFor` is not ok, or when
  `extra` has a `GOMAXPROCS` entry (matched by `hasEnvName`).
- Otherwise it returns `[]string{"GOMAXPROCS=<n>"}`, **regardless of
  `parent`**.
- Doc comment: replace "or when parent or extra already carries a GOMAXPROCS
  entry -- a user who set it for the daemon or the served process keeps their
  value" with: "or when extra (relay's own spec.Env) already carries one. An
  inherited GOMAXPROCS in parent does not stop it: a scope that limits CPUs is
  the operator's explicit choice, and Start removes the parent's entry so the
  child sees one value (#315 round 2)."

### `Runner.Start`, at the env site

```
add = goMaxProcsEnv(os.Environ(), spec.Env, spec.Scope)
spec.Env = append(spec.Env[:len:len], add...)        // unchanged copy discipline
deny = DeniedEnv
if len(add) > 0: deny = append(DeniedEnv[:len:len], "GOMAXPROCS")   // copy; never mutate the package var
cmd.Env = ChildEnv(os.Environ(), deny, spec.Env)
```

Update the comment above the append to say the scope's value replaces an
inherited one.

### README (~583)

Replace "-- unless the environment already sets it;" with "-- replacing any
`GOMAXPROCS` the daemon inherited, such as a shell-wide export;".

## 4. Ordered Steps

Run `make check` after each step, and check gofmt with
`gofmt -l $(git ls-files '*.go')`. Add no test in `cmd/relay`. CI has no
harness binary and no network.

1. **Rule and table.** Change `goMaxProcsEnv` and its doc comment. In
   `TestGoMaxProcsEnv`:
   - rename `parent GOMAXPROCS wins` to `parent GOMAXPROCS is overridden`,
     wanting `[]string{"GOMAXPROCS=1"}`;
   - rename `bare parent name wins` to `bare parent name is overridden`,
     wanting `[]string{"GOMAXPROCS=1"}`;
   - keep `extra GOMAXPROCS wins` (nil);
   - add `parent GOMAXPROCS, no limits`: parent `GOMAXPROCS=8`, scope
     `&relay.ScopeSpec{}`, want nil.

   *Verify:* `make check` passes.

2. **Start denies the inherited entry.** Change the env site as §3 says.
   Replace `TestStartParentGoMaxProcsWins` with
   `TestStartScopeGoMaxProcsOverridesParent`: `t.Setenv("GOMAXPROCS", "7")`,
   then a scope with `AllowedCPUs: "1"`, so the child should get 1. Assert the
   child's env (read the stream the way the existing tests do) has **exactly
   one** `GOMAXPROCS` line and that its value is `1`. Count the lines; do not
   only take the last value. If `childEnvValue` returns only the first match,
   add a small test helper that counts. Also add
   `TestStartParentGoMaxProcsPassesThroughWithoutLimits`: parent `7`, a scope
   with no quota and no pin, and the child sees `7`, once.

   *Mutation (required):* remove the `deny` change, keeping the rule change.
   `TestStartScopeGoMaxProcsOverridesParent` must fail on the count, because
   the child has two entries. Restore it.

   *Mutation (required):* restore the parent check in `goMaxProcsEnv`. The
   override test must fail with the value `7`. Restore it.

   *Verify:* `make check` passes. `DeniedEnv` is unchanged after `Start`: the
   package variable is never appended in place. Add an assertion for this to
   the new test.

3. **README.** Change the sentence as §3 says, then commit:
   `fix(scope): a scope's GOMAXPROCS overrides an inherited one (#315)`.

   *Verify:* `make check` passes.

## Report

List the files touched, both mutation results (what you changed and which
test failed, with its message), and whether `childEnvValue` needed a counting
variant.
