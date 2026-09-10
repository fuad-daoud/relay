# Consults Task 2, round 2: unblock the non-comparable Binding

Round 1 did the right thing. You hit a genuine design error in the plan you were
given, stopped, and reported it accurately instead of guessing at a fix. The
diagnosis is correct and I verified it independently:

- `internal/relay/daemon.go:96` is the **only** `==` on a `Binding` in the tree
  (`go build ./...` reports exactly one error).
- The short-circuit cannot simply be deleted. `store.save` stamps
  `b.UpdatedAt = now` unconditionally (`internal/store/store.go:304`), so
  saving unconditionally would rewrite every `bind.json` on every daemon tick
  and turn `UpdatedAt` from "last change" into "last poll".

**Keep everything you wrote in round 1.** All five files are correct and stay as
they are. This round adds the replacement comparison, fixes the one call site,
pins the behaviour with tests, and commits.

## Confirm you are resuming

```bash
git status --porcelain
```

Expect exactly these five, modified, unstaged, with no commits:

```
 M internal/store/log.go
 M internal/store/store.go
 M internal/store/store_test.go
 M internal/store/types.go
 M internal/store/types_test.go
```

If the tree is clean, or `internal/relay/daemon.go` already appears, **stop and
report**.

## Why this fix and not the obvious ones

Two alternatives were considered and rejected. Do not substitute either.

- **A hand-written field-by-field `Equal`.** It keeps compiling after someone
  adds a field and silently stops noticing changes to it -- reintroducing this
  exact bug with no compile error to catch it next time.
- **`reflect.DeepEqual`.** Works, but `time.Time` values that represent the same
  instant can compare unequal under it (monotonic clock reading, location
  pointer), so it can report "changed" when nothing did.

Comparing the marshalled forms asks the question the caller actually means --
"would this write a different bind.json?" -- and cannot drift as fields are
added or removed.

## Task

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/types_test.go`:

```go
func TestSameBindingIgnoresNothingAndCatchesAConsult(t *testing.T) {
	a := newBinding("webshop", "/repo")
	b := newBinding("webshop", "/repo")

	if !SameBinding(a, b) {
		t.Fatal("two identically-built bindings compare different")
	}

	b.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultRunning}}
	if SameBinding(a, b) {
		t.Error("a binding differing only by an appended consult compares same; the daemon would skip the save")
	}
}

func TestSameBindingCatchesAConsultStateChange(t *testing.T) {
	a := newBinding("webshop", "/repo")
	a.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultRunning}}

	b := a
	b.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultDone}}

	if SameBinding(a, b) {
		t.Error("a consult moving running -> done compares same; the daemon would never persist the transition")
	}
}
```

Add to `internal/relay/daemon_test.go`. Read `TestTickReconcilesAndPersists`
(`:17`) first and follow its setup style; it shows how this package builds a
runtime and drives one tick.

```go
func TestTickDoesNotRestampAnUnchangedBinding(t *testing.T) {
	// The next == fresh short-circuit this replaces was never tested. save()
	// stamps UpdatedAt unconditionally, so without the short-circuit every tick
	// rewrites every bind.json and UpdatedAt stops meaning "last change".
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	before, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved %v -> %v on a tick that changed nothing",
			before.UpdatedAt, after.UpdatedAt)
	}
}
```

If `Tick` is unexported or takes different arguments, match what
`TestTickReconcilesAndPersists` does rather than inventing a call. If a tick
against `seedBound`'s binding legitimately changes it (so `UpdatedAt` *should*
move), **stop and report** -- the test needs a different fixture and that is a
decision for the planner.

- [ ] **Step 2: Run them and watch them fail**

```bash
dev run go test ./internal/store/ ./internal/relay/ -run 'TestSameBinding|TestTickDoesNotRestamp' -v
```

Expected: the store tests fail to compile (`SameBinding` undefined) and the
relay package still fails on `daemon.go:96`.

If `dev run` reports "no desktop is reachable", zen is down; run the bare
command instead and say so in your report.

- [ ] **Step 3: Add `SameBinding`**

Append to `internal/store/types.go`. It needs `bytes` and `encoding/json`
imports in that file.

```go
// SameBinding reports whether two bindings hold the same state, by comparing
// their serialised forms.
//
// Binding stopped being comparable with == when Consults, a slice, was added.
// The daemon's per-tick short-circuit needs an equality test that survives that
// and every field added later.
//
// A hand-written field-by-field Equal was rejected: it keeps compiling when a
// field is added and silently stops noticing changes to it, which is the same
// bug with no compiler error to catch it. Comparing the serialised form asks
// what the caller means -- would this write a different bind.json -- and cannot
// drift as fields come and go.
//
// A marshal error reports "not the same", so a caller saves rather than
// skipping a write it needed.
func SameBinding(a, b Binding) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}

	return bytes.Equal(ra, rb)
}
```

- [ ] **Step 4: Fix the call site**

In `internal/relay/daemon.go`, replace line 96:

```go
			if next == fresh {
```

with:

```go
			if store.SameBinding(next, fresh) {
```

Leave the body and the surrounding comment alone. `store` is already imported.

- [ ] **Step 5: Verify**

```bash
dev run make check
```

Expected: PASS, all packages.

- [ ] **Step 6: Prove the no-op-tick test pins something**

Temporarily change the Step 4 line to `if false {` so the binding is always
saved, then:

```bash
dev run go test ./internal/relay/ -run TestTickDoesNotRestampAnUnchangedBinding -v
```

Expected: **FAIL**, reporting UpdatedAt moved. Then revert:

```bash
git checkout internal/relay/daemon.go
```

and re-apply the Step 4 edit properly. Confirm `dev run make check` passes again.

If the test PASSES with `if false {`, stop and report -- it is pinning nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/store/ internal/relay/daemon.go internal/relay/daemon_test.go
git commit -m "feat(store): add the consult record and its log vocabulary

A consult is a record on its owning binding, not a binding: it has no round
counter, no diff baseline, no worktree and no persistent session, and every
one of those fields would be dead weight on a Binding.

Both new Binding fields carry omitempty, so a bind.json written before this
change round-trips byte-identically until its first consult.

Adding a slice field also makes Binding non-comparable, which breaks the
daemon's next == fresh short-circuit. That short-circuit cannot just be
dropped: save() stamps UpdatedAt unconditionally, so every tick would rewrite
every bind.json. SameBinding compares marshalled forms instead, which asks the
question the caller means and cannot drift as fields are added."
```

## Constraints

- Do not change any file from round 1 except to add `SameBinding` to
  `internal/store/types.go`.
- Outside `internal/store/`, touch only `internal/relay/daemon.go` and
  `internal/relay/daemon_test.go`.
- If a step is impossible as written, stop and say so in your report.
