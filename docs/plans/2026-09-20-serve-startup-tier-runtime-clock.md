# relay serve: the startup tier runtime has no clock, so every start panics

## 1. System overview

#226 (`818fb2f`) made `relay serve` log the builder tier at startup:
`cmd/relay/serve.go` builds `builderTierRT := relay.Runtime{Candidates, Policy,
LedgerPath}` and calls `relay.ServedBuilderTier(builderTierRT)`. That chain
(`PickServedCandidate -> Gates`) calls `rt.Now()`, and `Runtime.Now` is a plain
`func() time.Time` field left nil, so the daemon dies with a nil dereference
at `internal/relay/ledger.go:196` before it listens. Every other Runtime in
`cmd/relay/serve.go` (lines ~156, ~372, ~410, ~457) sets `Now: time.Now`;
this one was missed. Seen 2026-09-20 deploying `5e0a889` to contabo: the
unit crash-looped and `srv deploy` rolled back. Nothing pins it because the
only test-side runtimes carry a clock.

Deliverable: the startup runtime carries the clock, and a `cmd/relay` test
that builds the same runtime the way `cmdServeRun` does and calls
`relay.ServedBuilderTier` on it -- a test that panics without the fix.

## 2. File structure

```
cmd/relay/serve.go        -- extract the startup runtime into serveTierRuntime(); set Now
cmd/relay/serve_test.go   -- TestServeTierRuntimeHasClock (new test, no herdr, no network)
```

## 3. Data structures

None new. `relay.Runtime` is unchanged.

## 4. Interfaces

```
// serveTierRuntime is the Runtime cmdServeRun uses only to log the builder
// tier at startup: candidates, policy, the server ledger and a clock.
// It is a pure constructor: no I/O beyond what the caller already loaded.
func serveTierRuntime(candidates *candidate.Set, pol policy.Policy, root string) relay.Runtime
```

Postcondition: the returned Runtime has `Candidates == candidates`,
`Policy == pol`, `LedgerPath == filepath.Join(root, "ledger.json")`,
`Now != nil` (`time.Now`).

## 5. High-level pseudocode

```
cmdServeRun:
  ... load root, candidates, pol as today ...
  builderTierRT := serveTierRuntime(candidates, pol, root)   // replaces the literal
  ... unchanged from here ...

serveTierRuntime(candidates, pol, root):
  return relay.Runtime{Candidates: candidates, Policy: pol,
                       LedgerPath: filepath.Join(root, "ledger.json"), Now: time.Now}

TestServeTierRuntimeHasClock:
  root := t.TempDir()
  write root/candidates.json with one builder candidate
      (same shape as the fixtures other cmd/relay tests use; reuse a helper if one exists)
  candidates := candidate.Load(that file); pol := policy.Policy{} (or policy.Load of a
      minimal file -- whichever the package makes easy; an empty policy is fine)
  rt := serveTierRuntime(candidates, pol, root)
  if rt.Now == nil -> t.Fatal
  tier := relay.ServedBuilderTier(rt)      // this is the line that panicked in production
  if tier == "" -> t.Fatal("expected a tier")
```

The test must not shell out, must not touch `$HOME`, `$XDG_CONFIG_HOME` or
herdr (CI runners have no herdr binary), and must not read the real
`~/.config/relay/candidates.json` (#235): everything it needs is under
`t.TempDir()`.

## 6. Error handling

No new error paths. If `candidate.Load` in the test needs a field the plan
did not anticipate, copy the minimal fixture from an existing `cmd/relay` or
`internal/relay` test rather than inventing one.

## 7. Ordered implementation steps

1. Write `TestServeTierRuntimeHasClock` in `cmd/relay/serve_test.go` calling a
   not-yet-existing `serveTierRuntime`. Verify: `go test ./cmd/relay/ -run
   TestServeTierRuntimeHasClock` fails to compile (undefined: serveTierRuntime).
2. Add `serveTierRuntime` to `cmd/relay/serve.go` WITHOUT `Now` first and run
   the test. Verify: it panics (`nil pointer dereference` in
   `internal/relay.Gates`). This proves the test pins the bug.
3. Add `Now: time.Now` to `serveTierRuntime` and make `cmdServeRun` use it in
   place of the `builderTierRT := relay.Runtime{...}` literal (~line 143).
   Verify: `go test ./cmd/relay/ -run TestServeTierRuntimeHasClock` passes.
4. Run `make check`. Verify: passes (gofmt, vet, tests, mod tidy).
5. Commit with the message
   `fix(serve): startup tier runtime carries a clock -- relay serve panicked on every start since #226`
   Verify: `git diff --stat HEAD~1` lists exactly `cmd/relay/serve.go` and
   `cmd/relay/serve_test.go`.

## Scope

Only the two files above. Do not touch `internal/relay` (making `Gates`
tolerate a nil clock would hide the next missing field). If any step is
impossible as written or contradicts what is in the tree -- for instance
if `Runtime.Now` is already a method with a default -- halt and report
rather than improvise.
