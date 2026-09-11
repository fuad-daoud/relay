# Tab-only placement: every relay-spawned agent opens in its own tab (#79)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #79. Closes it. No spec -- the issue is the spec; its "Scope" and
"Acceptance" sections are reproduced in the tasks below.
**Depends on:** nothing open.

**Goal:** Delete the pane-split placement path. `bind`, `add`, `fork` and `ask`
always open the agent they spawn in a new herdr tab; the `--tab` / `--new-tab`
flags and `Herdr.SplitPane` disappear.

**Architecture:** `builderPane` (bind.go) and `consultPane` (ask.go) collapse
into one `openTab` helper that only calls `rt.Herdr.CreateTab`. The `NewTab`
field leaves every option struct. `SplitPane` leaves the `relay.Herdr`
interface, the real client, and both fakes. Tests that pinned "a refused
bind/ask never splits" now pin "never creates a tab" -- the invariant
(validation precedes spawning) is the point, not the herdr verb.

**Tech stack:** Go, `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` yourself. The live verification is the planner's, after
merge.

## Global constraints

- **Keep** `Herdr.CreateTab` and its `--no-focus` behaviour: focus stays with the planner.
- **Keep** `WorkspaceID` on every option struct and the `HERDR_WORKSPACE_ID` reads in `cmd/relay/main.go`. The tab is scoped to the planner's workspace.
- **Keep** the tab label (`<name>-builder` for builders, the consult's agent name for consults).
- **Keep** `PlannerPane` on the option structs. It is still the planner's identity even though it is no longer a split target.
- **Keep** `relay ask --workspace`; only its usage string changes.
- `pane close` in `relay reap` is untouched. Adopting a pane via `--builder <pane-id>` is untouched.
- One commit per task, on the worktree's branch.

---

### Task 1: CLI -- drop `--tab` / `--new-tab`

**Files:**
- Modify: `cmd/relay/main.go:493,520` (bind), `:597,627` (fork), `:657,682` (add), `:952-953,980` (ask)
- Modify: `cmd/relay/main_test.go:441-460` (`TestParseFlagsAcceptsFlagsAfterPositionals`)
- Modify: `cmd/relay/followup_test.go:48-52` (comment only)

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new. After this task the `NewTab` fields in `internal/relay` still exist but are never set; Task 2 removes them.

- [ ] **Step 1: Write the failing test**

Append to `cmd/relay/main_test.go`. This test fails inside `parseFlags`, before
`newRuntime`, so it never reaches herdr -- which is why it is allowed in
`cmd/relay` (CI runners have no `herdr` binary).

```go
// TestBindRejectsTabFlag pins #79: placement is not a per-bind decision any
// more, so the old --tab spelling must be an unknown flag, not a silent no-op.
// It fails in parseFlags, before newRuntime, so it never reaches herdr.
func TestBindRejectsTabFlag(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--tab"},
		{"add", "--name", "x", "--tab"},
		{"fork", "x", "--round", "1", "--new-name", "y", "--tab"},
		{"ask", "--role", "reviewer", "--file", "q.md", "--new-tab"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%v: got %v, want an unknown-flag error", args, err)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./cmd/relay -run TestBindRejectsTabFlag -v`
Expected: FAIL -- each case returns nil or a different error, because the flags are still defined.

- [ ] **Step 3: Remove the flags**

In `cmd/relay/main.go`:

- `cmdBind`: delete the `newTab := fs.Bool("tab", ...)` line and the `NewTab: *newTab,` field in `relay.BindOptions{...}`.
- `cmdFork`: delete the `newTab := fs.Bool("tab", ...)` line and `NewTab: *newTab,` in `relay.ForkOptions{...}`. In the usage error string, change `[--builder CANDIDATE] [--tab] [--cwd DIR]` to `[--builder CANDIDATE] [--cwd DIR]`.
- `cmdAdd`: delete the `newTab := fs.Bool("tab", ...)` line and `NewTab: *newTab,` in `relay.AddOptions{...}`.
- `cmdAsk`: delete `newTab := fs.Bool("new-tab", ...)` and `NewTab: *newTab,` in `relay.AskOptions{...}`. Change the `workspace` flag's usage from `"workspace for --new-tab"` to `"workspace for the consult's tab (default: $HERDR_WORKSPACE_ID)"`.

In `cmd/relay/main_test.go` `TestParseFlagsAcceptsFlagsAfterPositionals`: the
local flagset is a fixture, not the real command, but `--tab` no longer exists
anywhere and the test should not suggest it does. Rename the flag in that test
from `tab` to `dry` (three places: `fs.Bool("dry", false, "")`, the args slice
`"--dry"`, and the two error messages `--dry after a positional must still parse`).

In `cmd/relay/followup_test.go`, rewrite the doc comment sentence
`so `relay ask --new-tab` opened the consult in` to `so `relay ask` opened the
consult in` (the rest of the comment stays).

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./cmd/relay`
Expected: PASS (including `TestBindRejectsTabFlag`).

- [ ] **Step 5: Commit**

```bash
git add cmd/relay/main.go cmd/relay/main_test.go cmd/relay/followup_test.go
git commit -m "refactor(cli): drop --tab and --new-tab; placement is not per-bind (#79)"
```

---

### Task 2: `internal/relay` -- one `openTab` helper, no `SplitPane`, no `NewTab`

**Files:**
- Modify: `internal/relay/herdr.go:26` (interface)
- Modify: `internal/relay/bind.go:16-17,52-54,302-303,348,393-411`
- Modify: `internal/relay/ask.go:49,85,186,262-279`
- Modify: `internal/relay/add.go:20,162`
- Modify: `internal/relay/fork.go:37,203`
- Modify: `internal/relay/fake_test.go:25-27,178-179,194,244-251`
- Modify: `internal/relay/bind_test.go` (assertions at 78, 186, 244, 382, 525, 576, 663, 701, 721, 737, 742-785, 1121, 1249)
- Modify: `internal/relay/ask_test.go` (27, 75, 179-203, 246, 273, 428, 522-535)
- Modify: `internal/relay/add_test.go:170-171`
- Modify: `internal/relay/fork_test.go` (268, 281, 294, 308, 326, 340, 402-403, 430)

**Interfaces:**
- Consumes: `Herdr.CreateTab(ctx, workspaceID, cwd, label string) (string, error)` -- unchanged.
- Produces:
  - `func openTab(ctx context.Context, rt Runtime, workspaceID, cwd, label string) (string, error)` in `bind.go`, used by both `resolveBuilder` and `Ask`. Error shape: `fmt.Errorf("create tab %q: %w", label, err)`.
  - `relay.Herdr` interface without `SplitPane`.
  - `BindOptions`, `AddOptions`, `ForkOptions`, `AskOptions` without `NewTab`.
  - `fakeHerdr` without `SplitPane`, `splits`, `splitCalls`; `onSplit` renamed `onSpawn` (it now runs only at the top of `CreateTab`).

- [ ] **Step 1: Flip the default-placement test so it fails**

In `internal/relay/bind_test.go`, replace `TestBindSplitsThePlannerPaneByDefault` (lines 768-785) with:

```go
func TestBindOpensBuilderInItsOwnTab(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4", newTab: "w2:pT"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		WorkspaceID: "w2",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if len(f.tabs) != 1 {
		t.Fatalf("got %d tab creations, want 1: placement is tab-only (#79)", len(f.tabs))
	}
	if got := f.tabs[0]; got.WorkspaceID != "w2" || got.CWD != "/repo" || got.Label != "webshop-builder" {
		t.Errorf("tab call = %+v", got)
	}
	if b.Builder.PaneID != "w2:pT" {
		t.Errorf("builder pane = %q, want the tab's root pane", b.Builder.PaneID)
	}
	if len(f.starts) != 1 || f.starts[0].Pane != "w2:pT" {
		t.Errorf("agent must start in the tab's root pane, got %+v", f.starts)
	}
}
```

Delete `TestBindOpensBuilderInItsOwnTabWhenAsked` (lines 742-766) -- the new test above is its replacement with `NewTab` gone.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/relay -run TestBindOpensBuilderInItsOwnTab -v`
Expected: FAIL with `got 0 tab creations, want 1` (the default still splits).

- [ ] **Step 3: Collapse the helpers and delete the split path**

`internal/relay/bind.go`:
- Delete the `splitDirection` const and its comment (lines 16-17).
- In `BindOptions`, delete the `NewTab bool` field and its comment; keep `WorkspaceID` with the comment `// WorkspaceID scopes the builder's tab to the planner's workspace.`
- In the `resolveBuilder` doc comment, change `or splits a sibling pane and starts the candidate agent in it` to `or opens a tab and starts the candidate agent in its root pane`.
- Change the call at line 348 to `paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, agentName)`. The `plannerPane` parameter of `resolveBuilder` stays (other callers pass it; it is out of scope to reshape the signature).
- Replace `builderPane` (lines 393-411) with:

```go
// openTab makes somewhere for a spawned agent to live: its own herdr tab in
// the planner's workspace, rooted at cwd and labelled so the tab strip says
// which builder or consult lives there. Focus stays with the planner.
// Every spawn site (bind, add, fork, ask) comes through here; placement is
// not a per-command decision (#79).
func openTab(ctx context.Context, rt Runtime, workspaceID, cwd, label string) (string, error) {
	paneID, err := rt.Herdr.CreateTab(ctx, workspaceID, cwd, label)
	if err != nil {
		return "", fmt.Errorf("create tab %q: %w", label, err)
	}
	return paneID, nil
}
```

`internal/relay/ask.go`:
- Delete the `NewTab bool` field from `AskOptions`.
- Line 85 doc comment: change `(SplitPane/CreateTab itself failed)` to `(CreateTab itself failed)`.
- Line 186: `pane, err := openTab(ctx, rt, opts.WorkspaceID, cwd, consult.Endpoint.AgentName)`.
- Delete `consultPane` entirely (lines 262-279).

`internal/relay/add.go`: delete `NewTab bool` from `AddOptions` (line 20) and `NewTab: opts.NewTab,` (line 162).
`internal/relay/fork.go`: delete `NewTab bool` from `ForkOptions` (line 37) and `NewTab: opts.NewTab,` (line 203).
`internal/relay/herdr.go`: delete the `SplitPane` line from the `Herdr` interface.

- [ ] **Step 4: Update the fake**

`internal/relay/fake_test.go`:
- Delete the `splitCall` type (lines 25-27).
- Delete the `splitCalls []splitCall` and `splits int` fields.
- Rename `onSplit` to `onSpawn` and rewrite its comment: `// onSpawn runs at the top of CreateTab, before any other logic. Ask calls CreateTab as its first herdr call after releasing the lock, which is where a test proves the lock is free and where it can rewrite the reservation to simulate a slow spawn.`
- Delete the `SplitPane` method.
- In `CreateTab`, change `if f.onSplit != nil { f.onSplit() }` to `if f.onSpawn != nil { f.onSpawn() }`. Keep the `newTab`-else-`newPane` fallback: most fixtures set only `newPane`, and that fallback is what keeps them green now that every spawn is a tab.

- [ ] **Step 5: Rewrite the assertions that counted splits**

Mechanical: every `f.splits` / `fh.splits` becomes `len(f.tabs)` / `len(fh.tabs)`, and the message says "tab" instead of "split". Concretely:

`bind_test.go`:
- 78-79: `if len(f.starts) != 0 || len(f.tabs) != 0 { t.Errorf("a refused name must touch no pane: tabs = %d, starts = %d", len(f.tabs), len(f.starts)) }`
- 186-187 and 244-245: `if len(f.tabs) != 0 { t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs)) }`
- 382-383: `if len(f.tabs) != 0 { t.Errorf("no tab may be created, got %d", len(f.tabs)) }`
- 525-526: `if len(f.starts) != 0 || len(f.tabs) != 0 { t.Errorf("adopting must not create tabs or start agents, tabs=%d starts=%+v", len(f.tabs), f.starts) }`
- 576-577: `if len(f.tabs) != 0 { t.Errorf("no tab may be created when builder is alive, got %d", len(f.tabs)) }`
- 663-664, 701-702, 721-722, 737-738: `if len(f.tabs) != 0 || len(f.starts) != 0 { t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts) }`
- 1121-1122: `if len(f.tabs) != 0 { t.Errorf("no tab may be created, got %d", len(f.tabs)) }`
- 1249-1250: `if len(f.tabs) != 0 || len(f.starts) != 0 { t.Errorf("nothing may be spawned, tabs=%d starts=%+v", len(f.tabs), f.starts) }`

`add_test.go` 170-171 and `fork_test.go` 402-403: `if len(fh.tabs) != 0 || len(fh.starts) != 0 { t.Errorf("a refused name must touch no pane: tabs = %d, starts = %d", len(fh.tabs), len(fh.starts)) }`

`fork_test.go` 268, 281, 294, 308, 340: replace `fh.splits != 0` with `len(fh.tabs) != 0` inside the existing condition; 326 and 430 likewise.

`ask_test.go`:
- 27: `f.starts, f.tabs = nil, nil`
- 75-76: `if len(f.tabs) != 0 { t.Errorf("a refused ask must touch no pane: tabs = %d", len(f.tabs)) }`
- 246-247 and 273-274: `if len(f.tabs) != 0 { t.Errorf("tabs = %d, want 0", len(f.tabs)) }`
- 428-429: `if !(len(f.tabs) == 0 && len(f.starts) == 0) { t.Errorf("expected no tabs and no starts, got tabs=%d starts=%d", len(f.tabs), len(f.starts)) }`
- Every `f.onSplit =` (522, 553, 661, 715) becomes `f.onSpawn =`; the three `t.Fatal("timed out waiting for ... inside onSplit")` messages become `inside onSpawn`; line 535 `t.Fatal("state lock is held during SplitPane")` becomes `during CreateTab`; the comment at 525 `if Ask holds the store lock across SplitPane` becomes `across CreateTab`.
- Replace `TestAskOpensTheConsultInTheBindingsTree` (179-203) with:

```go
func TestAskOpensTheConsultInTheBindingsTree(t *testing.T) {
	// Tree: "binding" is the role's contract, and nothing else in the suite can
	// observe it: passing the planner's cwd -- or an empty one -- to the tab
	// would go unnoticed without this pin.
	f := &fakeHerdr{}
	rt, b := seedForAsk(t, f)
	q := writeQuestion(t, "review it")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3", WorkspaceID: "w2",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(f.tabs) != 1 {
		t.Fatalf("got %d tabs, want 1", len(f.tabs))
	}
	if got := f.tabs[0].CWD; got != b.CWD {
		t.Errorf("consult tab opened in %q, want the binding's tree %q", got, b.CWD)
	}
	if got := f.tabs[0].WorkspaceID; got != "w2" {
		t.Errorf("consult tab in workspace %q, want the planner's w2", got)
	}
}
```

- [ ] **Step 6: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS. If any test still references `splits`, `splitCalls`, `onSplit`, `SplitPane`, or `NewTab`, the compiler names it -- fix that reference the same way as above; do not add the symbol back.

- [ ] **Step 7: Commit**

```bash
git add internal/relay
git commit -m "refactor(relay): tab-only placement -- one openTab helper, SplitPane and NewTab gone (#79)"
```

---

### Task 3: `internal/herdr` client and `internal/ui` fake

**Files:**
- Modify: `internal/herdr/client.go:207-225` (delete `SplitPane`), `:242-244` (comment)
- Modify: `internal/herdr/client_test.go:51-61` (delete `TestClientSplitPaneReturnsPaneID`)
- Modify: `internal/ui/fake_test.go:52-55` (delete `SplitPane`)

**Interfaces:**
- Consumes: nothing from earlier tasks (the `relay.Herdr` interface no longer requires `SplitPane`, so removing it here compiles).
- Produces: `*herdr.Client` without `SplitPane`. `paneEnvelope` (lines 199-205) is now unused -- delete it too; `go vet` does not flag unused types but leaving a dead envelope invites the next split.

- [ ] **Step 1: Delete `SplitPane` from the real client**

In `internal/herdr/client.go`: delete the `paneEnvelope` type and the `SplitPane` method with its comment. In the `CreateTab` doc comment, replace the second sentence (`A builder in its own tab keeps the planner pane full width, at the cost of not being able to watch the builder work side by side.`) with `Every agent relay spawns lives in its own tab (#79).`

- [ ] **Step 2: Delete its test**

In `internal/herdr/client_test.go`: delete `TestClientSplitPaneReturnsPaneID`. If a `CreateTab` test does not already exist in this file (`grep -n CreateTab internal/herdr/client_test.go`), add one so the tab envelope decode stays pinned:

```go
func TestClientCreateTabReturnsRootPaneID(t *testing.T) {
	c := NewClient(stubHerdr(t, `{"result":{"root_pane":{"pane_id":"w2:pT"}}}`, 0), 5*time.Second)

	id, err := c.CreateTab(context.Background(), "w2", "/tmp", "webshop-builder")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if id != "w2:pT" {
		t.Fatalf("pane id = %q, want w2:pT", id)
	}
}
```

- [ ] **Step 3: Delete `SplitPane` from the ui fake**

In `internal/ui/fake_test.go`: delete the `SplitPane` method (lines 52-55). `CreateTab` stays as a read-only-violation stub.

- [ ] **Step 4: Confirm nothing is left**

Run: `grep -rn SplitPane --include='*.go' .`
Expected: no output.

Run: `go test -count=1 ./internal/herdr ./internal/ui`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/herdr internal/ui
git commit -m "refactor(herdr): remove SplitPane; CreateTab is the only spawn placement (#79)"
```

---

### Task 4: Docs, `make check`, and the mutation check

**Files:**
- Modify: `README.md:148,160,193,196,259-262,588`
- Modify: `docs/specs/2026-09-10-consults-design.md` (one note near line 259)

- [ ] **Step 1: README**

- Line 148: `relay bind --builder claude/anthropic/sonnet     # split a builder pane and bind it to this tree` → `relay bind --builder claude/anthropic/sonnet     # open a builder tab and bind it to this tree`
- Line 160: remove `[--tab]` from the `relay bind` synopsis.
- Line 193: remove `[--tab]` from the `relay add` synopsis.
- Line 196: remove `[--tab]` from the `relay fork` synopsis.
- Lines 259-262, the "Where the builder appears" section body, becomes:

  ```
  Every agent relay spawns -- builders from `bind`, `add`, `fork` and consults
  from `ask` -- opens in its own herdr tab in the planner's workspace, labelled
  with the agent's name, without moving focus. There is no split option: side-
  by-side panes stop being readable at two or three builders, and tabs scale.
  ```

- Line 588: `relay stages the question, splits a pane beside the planner, and starts the` → `relay stages the question, opens a tab in the planner's workspace, and starts the`

Then: `grep -n -e '--tab' -e 'new-tab' -e 'split' README.md` -- the only remaining `split` hits should be unrelated prose (line ~824, "why the CLI and the daemon are split"). Anything about pane placement gets the same treatment.

- [ ] **Step 2: Spec note**

In `docs/specs/2026-09-10-consults-design.md`, directly under the `- **Dependencies:**` line at ~259, add:

```
- **Placement note (2026-09-12, #79):** `SplitPane` is gone; every spawn goes
  through `CreateTab`. References to `SplitPane` below are historical.
```

Leave the pseudocode and §7 text as they are; the spec is a record.

- [ ] **Step 3: Full verification**

Run: `make check`
Expected: green (gofmt clean, vet clean, all tests pass, go.mod/go.sum unchanged).

Run: `grep -rn SplitPane . --include='*.go'; grep -rn -e 'NewTab' -e 'splitDirection' --include='*.go' .`
Expected: no output.

- [ ] **Step 4: Mutation check -- the never-spawns invariant still bites**

The invariant the renamed tests pin is "validation precedes spawning". Prove
they still pin it. In `resolveBuilder` (`internal/relay/bind.go`) the spawn
path reads, in order:

```go
agentName, err := builderAgentName(name)   // validates; refuses a long name
if err != nil { return ... }
paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, agentName)
```

Temporarily swap them so the tab is created before the name is validated:

```go
paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, name+"-builder")
if err != nil { return store.Endpoint{}, Resolution{}, err }
agentName, err := builderAgentName(name)
if err != nil { return store.Endpoint{}, Resolution{}, err }
```

Run: `go test -count=1 ./internal/relay -run TestBindRefusesABuilderNameHerdrWouldRefuse -v`
Expected: FAIL with `a refused name must touch no pane: tabs = 1, starts = 0`.

Then `git checkout internal/relay/bind.go` and re-run `go test -count=1 ./internal/relay` -- PASS. Do not commit the mutation.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/specs/2026-09-10-consults-design.md
git commit -m "docs: placement is tab-only; --tab and --new-tab are gone (#79)"
```

---

## Report

Your report says, in this order:

1. `make check` output tail (or the constituents, if `make` was intercepted).
2. The `grep -rn SplitPane` result (must be empty outside `docs/`).
3. Which test failed under the Task 4 mutation, and its message.
4. `git log --oneline main..HEAD` and `git diff --stat main..HEAD`.
5. Anything you had to stop on.

## Planner verification (after merge, not the builder's job)

- `relay bind --builder abuilder` from a planner pane opens the builder in a new tab in `$HERDR_WORKSPACE_ID`, labelled `<name>-builder`, without moving focus.
- `relay bind --tab` prints `flag provided but not defined: -tab`.
- `relay ask --role reviewer --file q.md` opens the consult in a tab in the planner's workspace.
