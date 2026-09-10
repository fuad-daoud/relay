# Consults Task 8: ship the reviewer role

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 8 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 7 (relay reap), Task 4 (runningConsults helper). Verify before you start:

```bash
test -f internal/relay/reap.go   # Task 7 (relay reap)
grep -q 'func runningConsults' internal/relay/ask.go   # Task 4 (runningConsults helper)
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

### Task 8: Ship the `reviewer` role

**Files:**
- Create: `internal/harness/agents/reviewer.claude.md`
- Create: `internal/harness/agents/reviewer.opencode.md`
- Modify: `internal/relay/status.go:106-160` (consult count), `:176-225` (render)
- Modify: `internal/relay/status_test.go`
- Modify: `docs/design.md:288`
- Modify: `CLAUDE.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: everything from Tasks 1-7.
- Produces: `BindingStatus.Consults int` — running consults on that binding.

- [ ] **Step 1: Write the failing test**

Add to `internal/relay/status_test.go`:

```go
func TestStatusCountsOnlyRunningConsults(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultRunning},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultDone},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings", len(rep.Bindings))
	}
	if rep.Bindings[0].Consults != 2 {
		t.Errorf("Consults = %d, want 2 running (the done one awaits reap)", rep.Bindings[0].Consults)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
go test ./internal/relay/ -run TestStatusCountsOnlyRunningConsults -v
```

Expected: FAIL to compile — `BindingStatus.Consults` undefined.

- [ ] **Step 3: Add the count**

In `internal/relay/status.go`, add to `BindingStatus`:

```go
	// Consults is how many consults are still running on this binding.
	// Terminal ones are omitted: they are a reap chore, not work in flight.
	Consults int `json:"consults,omitempty"`
```

In `statusRow`, set `Consults: runningConsults(b)` (the helper from Task 4).

In `RenderStatus`, append ` +Nc` to the row when `Consults > 0`, so a binding with two running consults reads `webshop  active  r3  +2c`. Keep it out of the row entirely when zero.

- [ ] **Step 4: Write the role definitions**

Create `internal/harness/agents/reviewer.claude.md` with `model: opus` frontmatter, and `internal/harness/agents/reviewer.opencode.md` with `model: openrouter/z-ai/glm-5.3-flash`. Match the frontmatter shape of the existing `plan-executor.*.md` and `researcher.*.md` files in the same directory — read one first.

Body, both files:

- A read-only tool set, with an explicit prohibition on edits, creates, deletes and any tree-modifying command.
- Write findings to the path named in the prompt; reply with only that path.
- Cite file and line references, not summaries.
- State plainly when the diff contains no problem, rather than manufacturing one. A reviewer that always finds something is pinning nothing.
- Do not dispatch sub-agents.

Note in a comment or the README that this is deliberately **not** the `researcher` role: `researcher` is dispatched by `plan-executor` and returns findings in-band to its parent, while `reviewer` runs in its own relay pane and hands back a file path.

- [ ] **Step 5: Amend the pane-closing rule**

`docs/design.md:288` currently ends "Relay never kills a pane." Replace that sentence with:

> Relay kills a pane only in `relay reap`, and only a consult pane it spawned itself.

In `CLAUDE.md`, the "relay never closes a pane it spawned" bullet becomes:

> relay closes a pane only in `relay reap`, and only a terminal consult pane it spawned. After an `unbind`, a mis-bind, or any `--assume-dead` rebind, close the orphaned builder pane yourself with `herdr pane close <id>` or it holds memory indefinitely (an idle opencode builder is roughly 800 MB).

- [ ] **Step 6: Document the role in the README**

Add a short section covering: what a consult is, the `relay ask --role reviewer --file q.md` invocation, where findings land, that `relay reap` closes finished panes, and where to copy the reviewer definition for each harness. State plainly that read-only is a property of the role's configuration, not something relay enforces.

Before adding the alias to `DefaultTable`, **check how #24 resolved the "does relay ship a table at all" question**. If defaults are being removed, ship the reviewer entry as a documented example only. Do not decide this unilaterally.

- [ ] **Step 7: Full verification**

```bash
make check
git diff --stat
```

Compare the diff against this plan's declared scope. Then run it for real:

```bash
go build -o /tmp/relay ./cmd/relay
# in a herdr session, from a bound tree with at least one completed round:
/tmp/relay ask --role reviewer --file /tmp/q.md webshop
/tmp/relay status          # shows +1c
# wait for findings to arrive in the planner, then:
/tmp/relay reap webshop --dry-run
/tmp/relay reap webshop
/tmp/relay status          # no consult column
```

- [ ] **Step 8: Commit**

```bash
git add internal/harness/agents/ internal/relay/status.go internal/relay/status_test.go docs/design.md CLAUDE.md README.md
git commit -m "feat(relay): ship the reviewer consult role

relay status counts running consults only; a terminal one is a reap chore,
not work in flight.

docs/design.md and CLAUDE.md are amended: relay kills a pane in relay reap and
nowhere else, and only a consult pane it spawned itself."
```
