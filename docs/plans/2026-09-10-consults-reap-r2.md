# Consults Task 7, round 2: make `--all` survive a binding vanishing

Round 1 is correct and committed (`0ede61b`). I verified it independently:
`make check` green on zen, scope matches, and I re-ran two of your three
mutations myself — the running-guard deletion and the dry-run deletion both
killed their named test with the exact predicted message. Your `internal/ui`
ripple stub is right too: that fake fails the test on every mutation call, so a
`ClosePane` that does the same matches its convention.

Your Note 5 found a real bug in the plan. This round fixes it, plus one
consistency gap (your Note 4).

## Why round 2 exists

`--all` builds a name list from `rt.Store.List()`, then loads each binding under
the lock. A `relay unbind` landing between those two moments makes `tx.Load`
return `store.ErrNotFound`, and the plan's code returns it — **aborting the
whole sweep**. Every binding after the vanished one goes unreaped, and the
command exits non-zero over something that is ordinary use.

The codebase already decided this exact question the other way.
`internal/relay/daemon.go:82-86` does the same List-then-Load and says:

```go
			fresh, err := tx.Load(b.Name)
			if errors.Is(err, store.ErrNotFound) {
				// A `relay unbind` landed between List and here. That is
				// normal use, not a failure worth logging.
				return nil
			}
```

Reap must match it. Note the asymmetry, and preserve it: under `--all` a missing
binding is a race to skip, but when the human **named** a binding, `ErrNotFound`
is a real mistake and must still surface.

## Task

- [ ] **Step 1: Give the fake a close hook**

`internal/relay/fake_test.go`. Add the field beside `closed`/`closeErr`:

```go
	onClose func()
```

and call it at the top of `ClosePane`, before anything else:

```go
func (f *fakeHerdr) ClosePane(_ context.Context, paneID string) error {
	if f.onClose != nil {
		f.onClose()
	}
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, paneID)
	return nil
}
```

This mirrors the existing `onList` hook on `ListAgents`, which exists for the
same purpose: `daemon_test.go`'s `TestTickIgnoresBindingUnboundMidTick` uses it
to land an unbind inside the one window that matters.

- [ ] **Step 2: Write the failing test**

Add to `internal/relay/reap_test.go`:

```go
func TestReapAllSurvivesABindingVanishingMidSweep(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	// A second binding, sorted after "webshop" so the first close comes from
	// webshop. Store.List reads the state root with os.ReadDir, which returns
	// entries sorted by name, so this ordering is deterministic.
	zlast := store.Binding{
		Name:    "zlast",
		CWD:     "/zlast",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{PaneID: "w2:pC", Kind: "opencode"},
		Round:   1,
		State:   store.StateActive,
		Consults: []store.Consult{{
			ID: "dddddddd", Role: "reviewer", State: store.ConsultDone,
			Endpoint: store.Endpoint{PaneID: "w2:pD"},
		}},
	}
	if err := rt.Store.Save(zlast); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A `relay unbind zlast` lands the moment the sweep closes its first pane,
	// which is webshop's. The sweep must skip zlast, not abort over it.
	var once bool
	f.onClose = func() {
		if once {
			return
		}
		once = true
		if err := rt.Store.Delete("zlast"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}

	res, err := Reap(context.Background(), rt, ReapOptions{All: true})
	if err != nil {
		t.Fatalf("a binding unbound mid-sweep aborted the whole sweep: %v", err)
	}

	var closed int
	for _, r := range res {
		closed += len(r.Closed)
	}
	if closed != 2 {
		t.Errorf("closed %d panes, want webshop's 2; the sweep did not finish", closed)
	}
}

func TestReapANamedBindingThatDoesNotExistIsAnError(t *testing.T) {
	// The --all skip must not swallow a genuine mistake: when the human names a
	// binding, a missing one is a typo, not a race.
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if _, err := Reap(context.Background(), rt, ReapOptions{Name: "nope"}); err == nil {
		t.Fatal("reaping a binding that does not exist returned nil")
	}
}
```

- [ ] **Step 3: Run them and watch the first one fail**

```bash
dev run go test ./internal/relay/ -run 'TestReapAllSurvives|TestReapANamedBinding' -v
```

Expected: `TestReapAllSurvivesABindingVanishingMidSweep` FAILS with the sweep
aborting on `ErrNotFound`. `TestReapANamedBindingThatDoesNotExistIsAnError`
should already PASS — it pins behaviour you must not break in Step 4.

- [ ] **Step 4: Skip a vanished binding, but only under `--all`**

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
				// A `relay unbind` landed between List and here. Skipping it
				// matches the daemon's tick, which resolves the same race the
				// same way (daemon.go:82). A sweep must not abandon every
				// binding after the one that vanished.
				//
				// Deliberately not extended to a NAMED binding: there,
				// ErrNotFound is the human's typo and must surface.
				return nil
			}
			if err != nil {
				return err
			}
```

`errors` is already imported in this file; `store` is too.

- [ ] **Step 5: Add `--name`, for consistency with every other command**

Your Note 4 is right: `reap` is the only binding-taking command with no
`--name`. `bindingArg` (`main.go:1018`) exists to accept either spelling and
refuses both at once. In `cmdReap`, add the flag beside `--all`/`--dry-run`:

```go
	nameFlag := fs.String("name", "", "binding name (default: the binding for this cwd)")
```

and pass it to `resolveBinding` in place of the empty string you pass today.
Leave the `--all` path alone: it must keep skipping `resolveBinding` for the
reason you gave in Note 3.

- [ ] **Step 6: Verify**

```bash
dev run make check
```

Then confirm both spellings reach the same place:

```bash
go build -o /tmp/relay ./cmd/relay
/tmp/relay reap --name nope 2>&1 | head -1
/tmp/relay reap nope 2>&1 | head -1
/tmp/relay reap --name nope nope 2>&1 | head -1
```

The first two must give the same not-found error; the third must be refused for
naming the binding twice.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/reap.go internal/relay/reap_test.go internal/relay/fake_test.go cmd/relay/main.go
git commit -m "fix(relay): let reap --all survive a binding unbound mid-sweep

--all lists names, then loads each under the lock. An unbind landing between
those two moments made tx.Load return ErrNotFound and aborted the sweep,
leaving every later binding unreaped over something that is ordinary use.

The daemon's tick already resolves the same race by skipping (daemon.go:82);
reap now matches it. A NAMED binding still errors: there a missing binding is
the human's typo, not a race.

Also adds --name, which reap was the only binding-taking command to lack."
```

## Constraints

- Do not change `ClosePane` on the real client, the `Herdr` interface, or the
  `internal/ui` stub. They are verified.
- If a step is impossible as written, stop and say so in your report.
