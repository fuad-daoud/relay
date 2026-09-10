# Consults Task 7, round 3: fix the test seam, then apply the skip

Round 2's report was correct on every point, and I reproduced the deadlock
myself — one goroutine, two nested `WithLock`s, `Store.Delete` blocked on
`sync.Mutex` inside `Reap`'s closure. Your diagnosis, your refusal to apply a
fix you could not validate, and your rejection of the goroutine workaround as
test-bending were all right.

**The planner's decision: keep `Reap` as it is.** Closing under the lock stays,
because `DeliverPending` and `Send` already hold the same lock across a herdr
`Prompt`. The cost you identified is real and is now recorded in the design spec
(§7.7) rather than fixed; restructuring a verified path into three phases is not
worth it against a cost that only bites when herdr itself is hung.

So the fix is to the **test seam**, not to `Reap`. Your `onList` analysis is why:
the hook fires inside a locked section, so no store write from it can work. It
does not need one.

**Keep your uncommitted Steps 1 and 2.** The `onClose` hook and both tests stay;
only the hook's body changes.

## Confirm you are resuming

```bash
git status --porcelain
```

Expect exactly:

```
 M internal/relay/fake_test.go
 M internal/relay/reap_test.go
```

with `git log --oneline -1` still at `0ede61b`. If anything else appears, stop
and report.

## Task

- [ ] **Step 1: Make the hook lock-free**

In `internal/relay/reap_test.go`, inside
`TestReapAllSurvivesABindingVanishingMidSweep`, replace the `f.onClose` body.
`rt.Store.Delete` re-enters `WithLock`; removing the directory does not.

```go
	// A `relay unbind zlast` that has already completed, observed from inside
	// the sweep. Deliberately os.RemoveAll and not rt.Store.Delete: Delete
	// re-enters WithLock, Reap already holds it, and Go mutexes are not
	// reentrant (see status.go:229), so the test would deadlock rather than
	// fail. Removing the directory reproduces the same end state -- zlast is
	// gone by the time its own closure loads it.
	//
	// The race is real despite the hook firing inside a closure: Reap takes the
	// lock per binding, not per sweep, so a real unbind can land between two
	// bindings' closures.
	var once bool
	f.onClose = func() {
		if once {
			return
		}
		once = true
		if err := os.RemoveAll(rt.Store.Dir("zlast")); err != nil {
			t.Fatalf("RemoveAll: %v", err)
		}
	}
```

`os` must be imported in `reap_test.go`; add it if it is not there.

- [ ] **Step 2: Run it and watch it fail — this time for the right reason**

```bash
dev run go test ./internal/relay/ -run 'TestReapAllSurvives|TestReapANamed' -timeout 60s -v
```

Expected now: `TestReapAllSurvivesABindingVanishingMidSweep` **FAILS with an
error, not a hang** — the sweep aborting on `ErrNotFound`.
`TestReapANamedBindingThatDoesNotExistIsAnError` PASSES.

Always pass `-timeout 60s` on reap runs this round; if anything hangs again,
stop and report rather than waiting out the ten-minute default.

- [ ] **Step 3: Skip a vanished binding, but only under `--all`**

In `internal/relay/reap.go`, replace the load at the top of the `WithLock`
closure:

```go
			b, err := tx.Load(name)
			if err != nil {
				return err
			}
```

with:

```go
			b, err := tx.Load(name)
			if errors.Is(err, store.ErrNotFound) && opts.All {
				// A `relay unbind` landed between List and here. The lock is
				// taken per binding, not per sweep, so this race is real.
				// Skipping matches the daemon's tick, which resolves the same
				// List-then-Load race the same way (daemon.go:82). A sweep must
				// not abandon every binding after the one that vanished.
				//
				// Deliberately NOT extended to a named binding: there,
				// ErrNotFound is the human's typo and must surface.
				return nil
			}
			if err != nil {
				return err
			}
```

Check whether `errors` is already imported in `reap.go`; add it if not.

- [ ] **Step 4: Both tests pass**

```bash
dev run go test ./internal/relay/ -run 'TestReapAllSurvives|TestReapANamed' -timeout 60s -v
```

Both PASS. The named-binding test is the guard that the skip did not swallow a
genuine mistake — if it now fails, the `&& opts.All` condition is wrong.

- [ ] **Step 5: Add `--name`, for consistency**

Your round-1 Note 4 was right: `reap` is the only binding-taking command without
`--name`. In `cmdReap`, add it beside `--all`/`--dry-run`:

```go
	nameFlag := fs.String("name", "", "binding name (default: the binding for this cwd)")
```

and pass `*nameFlag` to `resolveBinding` in place of the empty string. Leave the
`--all` path skipping `resolveBinding` entirely, for the reason you gave in
Note 3.

- [ ] **Step 6: Verify**

```bash
dev run make check
```

If `dev run` fails again with `Unknown: ChildProcess.kill`, run `make check`
bare and say so — that error is not "no desktop reachable", but the fallback is
the same.

Then:

```bash
go build -o /tmp/relay ./cmd/relay
/tmp/relay reap --name nope 2>&1 | head -1
/tmp/relay reap nope 2>&1 | head -1
/tmp/relay reap --name nope nope 2>&1 | head -1
```

First two: the same not-found error. Third: refused for naming the binding
twice.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/reap.go internal/relay/reap_test.go internal/relay/fake_test.go cmd/relay/main.go
git commit -m "fix(relay): let reap --all survive a binding unbound mid-sweep

--all lists names, then loads each under the lock, which is taken per binding
rather than per sweep. An unbind landing between two closures made tx.Load
return ErrNotFound and aborted the sweep, leaving every later binding unreaped
over something that is ordinary use.

The daemon's tick already resolves the same race by skipping (daemon.go:82);
reap now matches it. A NAMED binding still errors: there a missing binding is
the human's typo, not a race.

Also adds --name, which reap was the only binding-taking command to lack."
```

## Constraints

- Do not restructure `Reap`. Close-under-lock is a decided trade-off (spec
  §7.7), not an oversight.
- Do not change `ClosePane` on the real client, the `Herdr` interface, or the
  `internal/ui` stub.
- If a step is impossible as written, stop and say so in your report.
