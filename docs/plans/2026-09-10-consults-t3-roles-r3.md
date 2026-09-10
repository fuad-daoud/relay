# Consults Task 3, round 3: pin the refusal's placement

Round 2 was correct and is committed (`edcd7f0`). `make check` passes, the diff
scope matches, and both mutations I tried against `IsConsult` killed a named
test. This round fixes a defect in **the plan you were given**, not in your
work.

## Why round 3 exists

I mutation-tested your commit. I moved the `spec.IsConsult()` refusal out of its
place and re-inserted it *after* `builderPane` had already run -- so a refused
bind splits a pane, strands it, and only then returns the error.

`TestBindRefusesAConsultAlias` **passed**.

It passed because it asserts only `len(f.starts) != 0` -- that no *agent* was
started. `builderPane` calls `SplitPane`, which the fake counts separately in
`f.splits`. So the test cannot tell "refused before anything was created" from
"refused after leaving a pane behind", and the placement the plan was explicit
about -- "it must sit before `builderPane` so a refused bind spawns nothing" --
is not pinned by anything.

That matters concretely rather than theoretically. A stranded pane holds roughly
800 MB and nothing points at it, which is the leak `CLAUDE.md` warns about and
the exact failure `relay ask` is being designed to avoid in a later task.

## Task

- [ ] **Step 1: Strengthen the test**

In `internal/relay/bind_test.go`, replace the two closing assertions of
`TestBindRefusesAConsultAlias` -- keep the `errors.Is` check above them
unchanged -- so the test also asserts no pane was split:

```go
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	// Also assert no pane was SPLIT. Checking only f.starts cannot tell a
	// refusal that happened before anything was created from one that ran after
	// builderPane and left a pane behind with nothing pointing at it -- the
	// ~800 MB leak CLAUDE.md warns about. This is what pins the refusal's
	// placement ahead of builderPane.
	if f.splits != 0 {
		t.Errorf("split %d panes; a refused bind must not create a pane it then abandons", f.splits)
	}
```

- [ ] **Step 2: Prove the new assertion pins, by breaking the implementation**

Temporarily move the refusal in `internal/relay/bind.go` so it runs after the
pane exists. Cut this block out of `resolveBuilder`:

```go
	if spec.IsConsult() {
		return store.Endpoint{}, fmt.Errorf(
			"%q is a consult role; ask it with `relay ask --role %s`: %w",
			opts.Alias, opts.Alias, ErrConsultAlias)
	}
```

and paste it back immediately above this line, further down the same function:

```go
	if err := rt.Herdr.StartAgent(ctx, agentName, spec.Kind, paneID, spec.Args); err != nil {
```

Run:

```bash
dev run go test ./internal/relay/ -run TestBindRefusesAConsultAlias -v
```

Expected: **FAIL**, reporting `split 1 panes`.

If it PASSES, stop and report -- the assertion still is not pinning and there is
no point continuing.

- [ ] **Step 3: Revert the mutation**

```bash
git checkout internal/relay/bind.go
git status --porcelain
```

Expect only ` M internal/relay/bind_test.go`. Re-run the test and expect PASS.

- [ ] **Step 4: Verify**

```bash
dev run make check
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/bind_test.go
git commit -m "test(relay): pin the consult refusal ahead of pane creation

The test asserted only that no agent was started, so a refusal moved after
builderPane -- splitting a pane and stranding it -- passed unchanged. An idle
pane nothing points at holds roughly 800 MB.

Asserting f.splits is what makes the placement, rather than merely the
refusal, the thing under test."
```

## Constraints

- Do not change `resolveBuilder`'s logic. Round 2's implementation is correct
  and verified; this round changes one test.
- Touch no file other than `internal/relay/bind_test.go`.
- If a step is impossible as written, stop and say so in your report.
