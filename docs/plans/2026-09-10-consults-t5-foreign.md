# Consults Task 5: consults are not foreign agents

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 5 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 2 (consult schema). Verify before you start:

```bash
grep -q 'Consults \[\]Consult' internal/store/types.go   # Task 2 (consult schema)
```

If any check fails, **stop immediately and report it**. Your base is wrong, and
every step below will fail in confusing ways. This is not something to work
around by implementing the missing piece yourself -- that is another task's
work and would collide with it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. A halt that surfaces a design error
is worth more than a green suite that worked around one.

## Running commands

`make` is intercepted on this laptop and runs on the desktop. Use
`dev run make check`, and `dev run go test ./... -run ...` for single tests.

## Global Constraints

- Verification is `make check`, never `go test ./...` alone. It adds `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- Go stdlib only. Do not add a module dependency; `go mod tidy` must produce no diff.
- Compose relay config paths through `userConfigRoot()` (`cmd/relay/main.go`), never by hand.
- relay makes no judgements. The only gate on a consult is `does the findings file exist`.
- "Read-only" is a property of the role's configuration, never a claim relay enforces. Do not write a comment or a doc line saying relay prevents writes.
- Every new exported symbol gets a doc comment saying what it does and, where a choice was made, why. Match the existing house style: comments explain rationale, not mechanics.
- Existing `bind.json` files must load, round-trip and re-serialise unchanged until a consult exists on them. Both new `Binding` fields carry `omitempty`.
- Commit when the task's steps are all done. Do not push, and do not open a PR.

- You are already in your own git worktree on your own branch. Do not create
  another branch and do not switch branches.

---

### Task 5: Consults are not foreign agents

`knownEndpoints` returns only `b.Planner` and `b.Builder`, so `ForeignAgents` flags every live agent in a bound tree it does not recognise — including a consult relay spawned itself. Without this, `relay status` reports a foreign agent for every consult, for its whole life.

Land this **before** Task 6 or every manual test of the daemon is noisy with false rows.

**Files:**
- Modify: `internal/relay/foreign.go:58-64`
- Test: `internal/relay/foreign_test.go`

**Interfaces:**
- Consumes: `Binding.Consults` (Task 2).
- Produces: no signature change.

- [ ] **Step 1: Write the failing test**

Add to `internal/relay/foreign_test.go`:

```go
func TestConsultInABoundTreeIsNotForeign(t *testing.T) {
	b := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
		Consults: []store.Consult{{
			ID:       "7f2a3c1d",
			Role:     "reviewer",
			State:    store.ConsultRunning,
			Endpoint: store.Endpoint{PaneID: "w2:p9", Kind: "claude"},
		}},
	}

	agents := []herdr.Agent{
		{PaneID: "w2:p4", Kind: "agy", CWD: "/repo", Status: herdr.StatusWorking},
		{PaneID: "w2:p9", Kind: "claude", CWD: "/repo", Status: herdr.StatusWorking, Title: "webshop-reviewer-7f2a3c1d"},
		{PaneID: "w2:pX", Kind: "claude", CWD: "/repo", Status: herdr.StatusWorking, Title: "someone else"},
	}

	got := ForeignAgents(agents, knownEndpoints([]store.Binding{b}), "/repo")

	if len(got) != 1 {
		t.Fatalf("got %d foreign agents, want 1:\n%+v", len(got), got)
	}
	if got[0].PaneID != "w2:pX" {
		t.Errorf("foreign = %q; a consult relay spawned and recorded is not foreign", got[0].PaneID)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/relay/ -run TestConsultInABoundTreeIsNotForeign -v
```

Expected: FAIL — `got 2 foreign agents, want 1`, listing `w2:p9`.

- [ ] **Step 3: Include consult endpoints**

Replace `knownEndpoints` in `internal/relay/foreign.go`:

```go
func knownEndpoints(bindings []store.Binding) []store.Endpoint {
	eps := make([]store.Endpoint, 0, len(bindings)*2)
	for _, b := range bindings {
		eps = append(eps, b.Planner, b.Builder)
		// A consult is spawned by relay and its endpoint is recorded, so it is
		// referenced by a binding and is not foreign. This does not soften the
		// rule that a sanctioned read-only pane relay did NOT spawn still shows
		// as foreign: relay holds no endpoint for one of those, and suppressing
		// it would mean trusting a title.
		for _, c := range b.Consults {
			eps = append(eps, c.Endpoint)
		}
	}
	return eps
}
```

Update the function's doc comment to mention consults alongside planners and builders.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
make check
```

- [ ] **Step 5: Commit**

```bash
git add internal/relay/foreign.go internal/relay/foreign_test.go
git commit -m "fix(relay): stop reporting relay's own consults as foreign agents

knownEndpoints returned only Planner and Builder, so a consult relay spawned
into the binding's own tree matched nothing and was reported as an unaccounted
occupant for its entire life.

This does not soften the rule for read-only panes relay did not spawn: relay
holds no endpoint for those, and suppressing them would mean trusting a title."
```
