# Consults Task 3, round 2: finish steps 4-6

Round 1 stopped after Step 3 and reported it accurately. That was the right
call, and your resume instructions were correct. Nothing is wrong with the work
you did -- I verified it: `internal/alias/alias.go` matches the plan, `bind.go`
is untouched, no commit was made, and the four modified files are exactly the
plan's declared scope.

**Do not redo Steps 1-3.** Your uncommitted changes to
`internal/alias/alias.go`, `internal/alias/alias_test.go`,
`internal/relay/bind_test.go` and `internal/relay/fake_test.go` are correct and
must be kept. Resume at Step 4.

## Confirm you are resuming, not starting over

```bash
git status --porcelain
```

Expect exactly these four, all modified, none staged, no commits:

```
 M internal/alias/alias.go
 M internal/alias/alias_test.go
 M internal/relay/bind_test.go
 M internal/relay/fake_test.go
```

If the tree is clean, or `internal/relay/bind.go` already appears, **stop and
report** -- something else changed the worktree and the state below is not what
this round assumes.

## Task

- [ ] **Step 4: Refuse a consult alias at bind time**

In `internal/relay/bind.go`, add this immediately after the
`ErrBuilderUnverified` declaration, which ends at line 31:

```go
// ErrConsultAlias reports a bind or add naming a consult role. A consult is
// ephemeral, read-only and one-shot; installing one as a binding's builder
// would put a reader where the work happens.
var ErrConsultAlias = errors.New("that alias is a consult role, not a builder")
```

Then in `resolveBuilder`, immediately after this existing block (it ends at
line 291):

```go
	spec, err := rt.Aliases.Lookup(opts.Alias)
	if err != nil {
		return store.Endpoint{}, err
	}
```

insert:

```go
	if spec.IsConsult() {
		return store.Endpoint{}, fmt.Errorf(
			"%q is a consult role; ask it with `relay ask --role %s`: %w",
			opts.Alias, opts.Alias, ErrConsultAlias)
	}
```

It must sit **before** the `agentName := name + "-builder"` line and the
`builderPane` call that follows, so a refused bind never splits a pane. That
mirrors the existing cwd check at `bind.go:213`, which is placed early for the
same reason: a refusal that has already spawned something has stranded it.

`errors` and `fmt` are already imported in this file; do not add imports.

- [ ] **Step 5: Verify**

```bash
dev run make check
```

`make` is intercepted on this laptop and runs on the desktop -- `dev run` is how
it runs. Expect PASS, including the untouched arg-list assertions in
`internal/alias/alias_test.go`.

Then confirm `Add` inherits the refusal through `Bind` rather than needing its
own check:

```bash
dev run go test ./internal/relay/ -run TestAdd -v
```

Expect PASS. If any `TestAdd` case fails, stop and report: it means `Add` does
not reach `resolveBuilder` the way the plan assumed, which is a design question,
not something to patch around.

- [ ] **Step 6: Commit**

```bash
git add internal/alias/ internal/relay/bind.go internal/relay/bind_test.go internal/relay/fake_test.go
git commit -m "feat(alias): give a spec a role and a tree policy

Empty means builder and binding-tree, so the three shipped aliases and any
aliases.json written before roles existed parse unchanged.

Bind refuses a consult alias before it spawns anything: a consult is
ephemeral, read-only and one-shot, and installing one as a builder would put
a reader where the work happens."
```

Then confirm the tree is clean:

```bash
git status --porcelain
```

## Constraints

- Touch no file outside the four already modified plus `internal/relay/bind.go`.
- Do not change any of the three shipped `Spec` values in `DefaultTable`.
- If a step is impossible as written, stop and say so in your report.
