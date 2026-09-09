# Plan C: bind --resume Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `relay bind --resume --builder` from spawning a replacement — and orphaning a builder that is still running the round — when relay cannot tell a dead builder from one whose pane moved between workspaces.

**Architecture:** A new sentinel error and one guard in `resume`, placed beside the existing `ErrBuilderAlive` check and above the irreversible spawn. The guard fires when the builder could not be located **and** no session was ever recorded for it, and is released by an explicit `--assume-dead` flag.

**Tech Stack:** Go. Standard library only. Tests use the existing `fakeHerdr` fixtures in `internal/relay/*_test.go`.

**Spec:** `docs/specs/2026-09-09-broken-binding-recovery-design.md`, sections 6.4, 6.5 and 7. Issue: [#21](https://github.com/fuad-daoud/relay/issues/21), direction 2.

**Depends on:** Plan A must have landed (the branch must contain `internal/relay/diagnose.go`). This plan does not call `DiagnoseBuilder` — the guard reads `b.Builder.SessionID` directly, because it must key on live evidence rather than stored state (see Task 1 background) — but it shares the branch and the concept, and reviewing it without Plan A's doc comments will be confusing.

## Global Constraints

- **The guard must land before anything is spawned.** `resume` contains the line `// IRREVERSIBLE: a pane may now exist. Never closed by relay.` Every check added here goes **above** it. A test asserts no pane was split and no agent started.
- **`--assume-dead` must never override `ErrBuilderAlive`.** A builder relay can positively see is alive is still refused. That is #20's guarantee and this plan does not weaken it.
- **No interactive prompt.** `relay` is driven by planner agents as often as humans; a y/N prompt could hang a non-TTY caller. The refusal is an error plus an opt-in flag.
- Do not modify `store` types, `displayState`, or `SameAgent`'s behaviour.
- Run `go test ./...` from the repo root and `gofmt -l ./internal ./cmd` (expect no output) before each commit.

---

### Task 1: The sentinel error and the guard

**Files:**
- Modify: `internal/relay/bind.go` (the error block near line 17, `BindOptions`, and `resume`)
- Test: `internal/relay/bind_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `ErrBuilderUnverified error` and `BindOptions.AssumeDead bool`. Task 2 wires the flag to `AssumeDead`.

**Background you need:** In `resume` (`internal/relay/bind.go`), the `if rebinding` block loads the binding, refuses a `done` binding, lists agents, then runs:

```go
		if _, alive := FindAgent(agents, b.Builder); alive {
			return store.Binding{}, ErrBuilderAlive
		}
		builder, err = resolveBuilder(ctx, rt, opts, opts.Name, planner.PaneID)
		...
		// IRREVERSIBLE: a pane may now exist. Never closed by relay.
```

`SameAgent` matches on session id when one is recorded, and falls back to pane plus kind when one is not. A workspace move changes the pane id, so for a session-less builder a failed match means either "dead" or "moved", and relay cannot tell which.

**Why the guard keys on live evidence, not `b.State == store.StateBroken`:** the daemon sets `StateBroken` on a tick. Keying the guard on the stored state would make it fire or not depending on whether a tick had happened since the pane went away — intermittent in production and racy in tests. The two conditions the guard actually needs are both available right here: `FindAgent` just missed, and `b.Builder.SessionID` is empty.

Note the two checks are mutually exclusive by construction: the first returns when the builder **was** found, so the second is only reached when it was not.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/bind_test.go`:

```go
// A session-less builder that cannot be located may be dead or may be alive in
// a pane that moved workspaces. relay cannot tell, so it must not spawn a
// replacement on the guess -- that is how a live builder gets orphaned.
func TestBindResumeRefusesUnverifiableBuilder(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"}, // never session-identified
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderUnverified) {
		t.Fatalf("got err = %v, want ErrBuilderUnverified", err)
	}
	if !strings.Contains(err.Error(), "--assume-dead") {
		t.Errorf("error must name the flag that releases it, got %q", err)
	}
	if !strings.Contains(err.Error(), "w2:p4") {
		t.Errorf("error must name the pane to check, got %q", err)
	}

	// The refusal must land before anything irreversible.
	if f.splits != 0 {
		t.Errorf("no pane may be split, got %d splits", f.splits)
	}
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}

	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Builder.PaneID != "w2:p4" || loaded.Round != 3 {
		t.Errorf("binding must be untouched, got %+v", loaded)
	}
}

func TestBindResumeProceedsWithAssumeDead(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3",
		CWD: "/repo", AssumeDead: true,
	})
	if err != nil {
		t.Fatalf("Bind with AssumeDead: %v", err)
	}
	if got.Builder.PaneID != "w2:p5" {
		t.Errorf("builder pane = %q, want the newly spawned w2:p5", got.Builder.PaneID)
	}
}

// A builder with a recorded session is unambiguous: if no live agent carries
// that session it really is gone, so the gate must not fire.
func TestBindResumeUnaffectedWhenSessionRecorded(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "dead-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3", CWD: "/repo",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

// --assume-dead releases only the unverifiable case. A builder relay can
// positively see is alive is still refused: that is #20's guarantee.
func TestAssumeDeadNeverOverridesBuilderAlive(t *testing.T) {
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p4",
				Session: herdr.Session{Value: "live-builder-sess"}},
		},
		newPane: "w2:p5",
	}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "live-builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Alias: "builder", PlannerPane: "w2:p3",
		CWD: "/repo", AssumeDead: true,
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("got err = %v, want ErrBuilderAlive even with AssumeDead", err)
	}
	if f.splits != 0 || len(f.starts) != 0 {
		t.Errorf("nothing may be spawned, splits=%d starts=%+v", f.splits, f.starts)
	}
}
```

`internal/relay/bind_test.go` already imports `context`, `errors`, `strings`, `testing`, `time`, `alias`, `herdr` and `store`. Add nothing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run 'TestBindResumeRefusesUnverifiable|TestBindResumeProceedsWithAssumeDead|TestBindResumeUnaffectedWhenSessionRecorded|TestAssumeDeadNeverOverrides' -v`
Expected: FAIL to build, with `undefined: ErrBuilderUnverified` and `unknown field AssumeDead in struct literal`.

- [ ] **Step 3: Add the sentinel error**

In `internal/relay/bind.go`, immediately after the `ErrBuilderAlive` declaration:

```go
// ErrBuilderUnverified reports a rebind attempt against a binding whose builder
// could not be located but was never session-identified.
//
// SameAgent falls back to pane plus kind when no session is recorded, and a
// workspace move changes the pane id -- so a failed match means either "dead"
// or "moved, still running". relay cannot tell which, and rebinding on the
// guess spawns a replacement and orphans a builder that is still working.
// It refuses instead, until a human says the builder really is gone.
var ErrBuilderUnverified = errors.New("builder was never session-identified")
```

- [ ] **Step 4: Add the option**

In `BindOptions`, after `Resume`:

```go
	// AssumeDead releases the ErrBuilderUnverified guard: the caller asserts a
	// builder relay cannot verify is gone really is gone. It never overrides
	// ErrBuilderAlive -- a builder relay can positively see is refused either
	// way.
	AssumeDead bool
```

- [ ] **Step 5: Add the guard**

In `resume`, directly after the `ErrBuilderAlive` check and **above** the `resolveBuilder` call:

```go
		if _, alive := FindAgent(agents, b.Builder); alive {
			return store.Binding{}, ErrBuilderAlive
		}
		// Reached only when the builder was NOT located. Without a recorded
		// session that miss is ambiguous: the pane id it would match on is the
		// one a workspace move invalidates. Refuse rather than guess.
		//
		// Keyed on live evidence rather than b.State so the guard does not
		// depend on whether the daemon has ticked since the pane went away.
		if b.Builder.SessionID == "" && !opts.AssumeDead {
			return store.Binding{}, fmt.Errorf(
				"%w: relay cannot tell a dead builder for %q from a moved pane. "+
					"Check %s is really gone, then re-run with --assume-dead",
				ErrBuilderUnverified, opts.Name, b.Builder.PaneID)
		}
```

Also update `resume`'s doc comment, which lists its errors:

```go
// Errors: store.ErrNotFound; ErrBuilderAlive; ErrBuilderUnverified; a wrapped
// herdr failure.
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/relay/ -run 'TestBindResume|TestAssumeDead' -v`
Expected: PASS, including the pre-existing resume tests.

- [ ] **Step 7: Verify the whole suite and formatting**

Run: `gofmt -l ./internal ./cmd && go test ./...`
Expected: no gofmt output; all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/relay/bind.go internal/relay/bind_test.go
git commit -m "feat(bind): refuse to rebind a builder relay cannot verify is gone

Without a recorded session, SameAgent matches on pane id -- the one field a
workspace move invalidates. A failed match therefore means 'dead' or 'moved,
still running', and rebinding on that guess orphans a live builder mid-round.

Refuses before anything is spawned, released by --assume-dead. Never
overrides ErrBuilderAlive.

Refs #21."
```

---

### Task 2: The CLI flag

**Files:**
- Modify: `cmd/relay/main.go` (`cmdBind`, around lines 240-275)
- Test: `cmd/relay/main_test.go`

**Interfaces:**
- Consumes: `BindOptions.AssumeDead` from Task 1.
- Produces: the `--assume-dead` flag. Nothing depends on it.

**Background you need:** `cmdBind` builds a `flag.FlagSet`, parses, then constructs `relay.BindOptions`. The existing flags are `--name`, `--builder`, `--resume`, `--tab`, `--timeout`.

Tests in `cmd/relay/main_test.go` drive the real entry point, `run([]string{...})`. The existing `TestBindResumeWithoutNameIsRejected` works without touching herdr or the state directory because `cmdBind`'s `--resume` needs `--name` check runs *before* `newRuntime()` is called. This task's test rides on that same property: if `--assume-dead` were not a defined flag, `fs.Parse` would fail with "flag provided but not defined" and never reach the `--name` check, so reaching the `--name` error proves the flag parsed.

- [ ] **Step 1: Write the failing test**

Append to `cmd/relay/main_test.go`:

```go
// --assume-dead must be defined on the bind flag set. If it were not, parsing
// would fail with "flag provided but not defined" and never reach the --name
// check -- which runs before any runtime is built, so this test touches
// neither the state directory nor herdr.
func TestBindAcceptsAssumeDeadFlag(t *testing.T) {
	err := run([]string{"bind", "--resume", "--assume-dead"})
	if err == nil {
		t.Fatal("relay bind --resume without --name must still be rejected")
	}
	if strings.Contains(err.Error(), "not defined") {
		t.Fatalf("--assume-dead is not a defined flag: %v", err)
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected the --name error, got %q", err)
	}
}
```

`cmd/relay/main_test.go` already imports `strings` and `testing`. Add nothing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/relay/ -run TestBindAcceptsAssumeDead -v`
Expected: FAIL with `--assume-dead is not a defined flag: flag provided but not defined: -assume-dead`.

- [ ] **Step 3: Add and wire the flag**

In `cmdBind`, after the `resume` flag:

```go
	assumeDead := fs.Bool("assume-dead", false,
		"confirm a builder relay cannot verify is gone really is gone")
```

and in the `relay.BindOptions` literal, after `Resume`:

```go
		AssumeDead:   *assumeDead,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/relay/ -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole suite and formatting**

Run: `gofmt -l ./internal ./cmd && go test ./...`
Expected: no gofmt output; all PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "feat(cli): add relay bind --assume-dead

Releases the ErrBuilderUnverified guard when the human has checked the pane
and confirmed the builder is gone.

Closes #21."
```

---

### Task 3: Correct the SameAgent doc comment

**Files:**
- Modify: `internal/relay/herdr.go` (the `SameAgent` doc comment)

**Interfaces:** none. Comment only, no behaviour change.

**Background you need:** `SameAgent`'s doc comment currently ends:

> This exposure lasts until a session is recorded: brief for claude (the window before the next tick backfills the session), and for agy lasting until the agent has begun a conversation and herdr reports its session.

Verified live on 2026-09-09 (herdr integration v10, opencode 1.18.29): **opencode behaves identically to agy.** A freshly spawned idle opencode pane carries no `agent_session`; after one prompt it carries one; and that session survives a workspace move while the pane id changes. Both halves of its integration are turn-driven — `plugins/herdr-agent-state.js` deliberately reports nothing on `session.created`, and `herdr-tui-session.js` only reports on a `session` route while a fresh opencode sits on the splash screen.

This matters because `builder` is opencode and `abuilder` is agy, while `claude` — the immune harness — is the planner. Every builder harness relay ships is in the late-session population, so the comment currently understates the exposure by naming one alias.

- [ ] **Step 1: Rewrite the final paragraph of the comment**

Replace the sentence beginning "This exposure lasts until a session is recorded" with:

```go
// This exposure lasts until a session is recorded. For claude it is brief: the
// window before the next tick backfills the session, since claude reports one
// at spawn. For agy and opencode it lasts until the agent has begun a
// conversation, because both report a session only once one exists -- verified
// live 2026-09-09 against herdr integration v10. Since `builder` is opencode
// and `abuilder` is agy, while claude is the planner, every builder harness
// relay ships is in the late-session population.
```

- [ ] **Step 2: Verify nothing changed but the comment**

Run: `gofmt -l ./internal && go test ./...`
Expected: no gofmt output; all PASS. Run `git diff -- internal/relay/herdr.go` and confirm every changed line begins with `//`.

- [ ] **Step 3: Commit**

```bash
git add internal/relay/herdr.go
git commit -m "docs(relay): opencode is in agy's late-session population, not claude's

Verified live against herdr integration v10: a fresh opencode pane reports no
session until its first turn, exactly like agy. Both of relay's shipped
builder aliases are therefore exposed, not just abuilder -- the comment named
one and generalised.

Refs #21, #41."
```

---

## Done when

- `go test ./...` passes.
- `relay bind --resume --builder` against a session-less, unlocatable builder refuses and names both the pane and `--assume-dead`.
- The same command with `--assume-dead` proceeds.
- A live builder is still refused with `ErrBuilderAlive`, with or without `--assume-dead`.
- `git diff main...HEAD --stat` touches only: `internal/relay/bind.go`, `internal/relay/bind_test.go`, `cmd/relay/main.go`, `cmd/relay/main_test.go`, `internal/relay/herdr.go`.
