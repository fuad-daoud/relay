# Fan-in Clobber Fix (#46) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `relayd` from injecting two payloads into the same planner pane in one tick, which merges the second into the planner's mid-turn input.

**Architecture:** `Daemon.Tick` fetches the herdr agent list once and reconciles every binding against that one snapshot. `DeliverPending` gates injection on `planner.Status`, read from that snapshot — so two bindings sharing a planner both see a stale `idle` and both type into the pane. The fix keeps the single-fetch cost model and makes the snapshot *self-updating*: after a successful injection, `DeliverPending` marks that planner `working` in the snapshot, which is simply the truth, and every later binding in the same tick then takes the existing `held` path. `Tick` copies the slice it got from herdr so the mutation cannot leak into the client's storage or across ticks.

**Tech Stack:** Go 1.x, stdlib `testing`. No new dependencies.

## Global Constraints

- Package `internal/relay`. No new third-party dependencies.
- Test style, verbatim from this package: no table tests, no testify, no subtests. `t.Fatalf` for setup/precondition failures, `t.Error`/`t.Errorf` for behavioural assertions, `%+v` for structs. Assertion messages state the **rule** being enforced, not just the values.
- Comments explain *why*, not *what* — match the density and tone of the existing comments in `deliver.go` and `daemon.go`.
- Exported signatures of `FindAgent`, `DeliverPending`, `Queue` and `Reconcile` must not change. Callers in `internal/ui` and `cmd/relay` are out of scope for this plan.
- Every task ends green: `go build ./... && go test ./...`.

---

### Task 1: Positional agent lookup

**Files:**
- Modify: `internal/relay/herdr.go:83-91`
- Test: `internal/relay/fake_test.go` (no change), assertions land in Task 2

**Interfaces:**
- Consumes: `SameAgent(a herdr.Agent, ep store.Endpoint) bool` (existing, `internal/relay/herdr.go`), `store.Endpoint`, `herdr.Agent`
- Produces: `findAgentIndex(agents []herdr.Agent, ep store.Endpoint) (int, bool)` — unexported, returns the slice index of the matching agent and `false`/`-1` when there is none. `FindAgent` keeps its exact existing signature `func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool)` and is reimplemented on top of it.

This task has no test of its own — it is a pure refactor with no behaviour change, and Task 2's tests are what exercise it. Verify by compilation and the existing suite staying green.

- [ ] **Step 1: Confirm the current suite is green before touching anything**

Run: `cd /home/fuad/projects/relay && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 2: Replace FindAgent with an index-based pair**

In `internal/relay/herdr.go`, replace this exact block (lines 83-91):

```go
// FindAgent locates a binding endpoint among the live agents using SameAgent.
func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool) {
	for _, a := range agents {
		if SameAgent(a, ep) {
			return a, true
		}
	}
	return herdr.Agent{}, false
}
```

with:

```go
// findAgentIndex is FindAgent's positional form. A caller that needs to record
// something against the agent it just located needs that agent's slot in the
// snapshot, not a copy of it.
func findAgentIndex(agents []herdr.Agent, ep store.Endpoint) (int, bool) {
	for i, a := range agents {
		if SameAgent(a, ep) {
			return i, true
		}
	}
	return -1, false
}

// FindAgent locates a binding endpoint among the live agents using SameAgent.
func FindAgent(agents []herdr.Agent, ep store.Endpoint) (herdr.Agent, bool) {
	i, ok := findAgentIndex(agents, ep)
	if !ok {
		return herdr.Agent{}, false
	}
	return agents[i], true
}
```

- [ ] **Step 3: Verify nothing changed behaviourally**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS. `FindAgent` returns the same values it always did; only its implementation moved.

- [ ] **Step 4: Commit**

```bash
cd /home/fuad/projects/relay
git add internal/relay/herdr.go
git commit -m "refactor(relay): add findAgentIndex beside FindAgent

FindAgent returns a copy, which is all every current caller needs. The
fan-in clobber fix needs to write back to the located agent's slot in the
tick snapshot, so the search grows a positional form and FindAgent is
reimplemented on top of it. No behaviour change.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 2: Mark the planner busy after injecting

**Files:**
- Modify: `internal/relay/deliver.go:56-98` (inside `DeliverPending`, after `tx.ConfirmLatest`)
- Test: `internal/relay/deliver_test.go` (append)

**Interfaces:**
- Consumes: `findAgentIndex` from Task 1; `herdr.StatusWorking` (`internal/herdr/types.go`); the existing `deliverPending(t, rt, b, agents)` and `queuedBinding(t, f)` helpers in `deliver_test.go`; `plannerWith(status string, focused bool) herdr.Agent` in `deliver_test.go`.
- Produces: a new test helper `twoBindingsOnePlanner(t *testing.T, f *fakeHerdr) (Runtime, store.Binding, store.Binding)` in `deliver_test.go`, returning a runtime plus two saved bindings that share one planner pane, each with a report already queued. Task 3 does **not** use it.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/deliver_test.go`:

```go
// twoBindingsOnePlanner seeds two active bindings that share one planner pane,
// each with a report already queued for that planner. This is the shape a
// planner running peer builders has, and the shape the fan-in clobber needs.
func twoBindingsOnePlanner(t *testing.T, f *fakeHerdr) (Runtime, store.Binding, store.Binding) {
	t.Helper()
	rt, first := queuedBinding(t, f)

	second := store.Binding{
		Name:    "storefront",
		CWD:     "/repo2",
		Planner: first.Planner,
		Builder: store.Endpoint{PaneID: "w2:p5"},
		Round:   1,
		State:   store.StateActive,
	}
	if err := rt.Store.Save(second); err != nil {
		t.Fatalf("save second binding: %v", err)
	}

	entry := store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Payload: "Builder finished round 1. Report: /x2/001-report.md",
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return Queue(context.Background(), rt, tx, second.Name, entry)
	})
	if err != nil {
		t.Fatalf("Queue second: %v", err)
	}

	stored, err := rt.Store.Load(second.Name)
	if err != nil {
		t.Fatalf("Load second: %v", err)
	}
	return rt, first, stored
}

func TestDeliverMarksPlannerBusyForTheRestOfTheTick(t *testing.T) {
	f := &fakeHerdr{}
	rt, first, second := twoBindingsOnePlanner(t, f)

	// One snapshot, shared by both bindings, exactly as Tick shares it.
	agents := []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	got, err := deliverPending(t, rt, first, agents)
	if err != nil {
		t.Fatalf("DeliverPending first: %v", err)
	}
	if !got.Delivered {
		t.Fatalf("the first payload should land, got %+v", got)
	}

	got, err = deliverPending(t, rt, second, agents)
	if err != nil {
		t.Fatalf("DeliverPending second: %v", err)
	}
	if got.Delivered {
		t.Error("a planner already prompted in this tick must not be typed into again")
	}
	if len(f.prompts) != 1 {
		t.Fatalf("one planner pane takes at most one injection per tick, got %+v", f.prompts)
	}
	if _, pending, err := rt.Store.PendingForPlanner(second.Name); err != nil || !pending {
		t.Errorf("the undelivered payload must stay pending: pending=%v err=%v", pending, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestDeliverMarksPlannerBusyForTheRestOfTheTick -v`
Expected: FAIL — `a planner already prompted in this tick must not be typed into again`, and `prompts` showing two entries both targeting `w2:p3`. That failure **is** issue #46 reproduced.

- [ ] **Step 3: Write the implementation**

In `internal/relay/deliver.go`, find this exact block at the end of `DeliverPending`:

```go
	// Address the agent FindAgent just located; see internal/ui/fetch.go:170.
	if err := promptWithRetry(ctx, rt, planner.PaneID, pending.Payload); err != nil {
		return Delivery{}, fmt.Errorf("prompt planner: %w", err)
	}
	if err := tx.ConfirmLatest(b.Name); err != nil {
		return Delivery{}, err
	}

	return Delivery{Delivered: true}, nil
```

and replace it with:

```go
	// Address the agent FindAgent just located; see internal/ui/fetch.go:170.
	if err := promptWithRetry(ctx, rt, planner.PaneID, pending.Payload); err != nil {
		return Delivery{}, fmt.Errorf("prompt planner: %w", err)
	}
	if err := tx.ConfirmLatest(b.Name); err != nil {
		return Delivery{}, err
	}

	// The planner is mid-turn on this payload now, but the snapshot still says
	// idle: it was taken once, before the daemon began its pass over the
	// bindings, and nothing refreshes it. Two bindings sharing a planner would
	// both read that stale idle and both type into the pane -- which is the
	// exact clobber the status gate above exists to prevent (#46). Record what
	// we just did, so every later binding in this pass sees the truth and takes
	// the ordinary "planner is working" path. Tick owns a copy of the snapshot,
	// so this never reaches the Herdr client or the next tick.
	if i, ok := findAgentIndex(agents, b.Planner); ok {
		agents[i].Status = herdr.StatusWorking
	}

	return Delivery{Delivered: true}, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestDeliver -v`
Expected: PASS, including the pre-existing `TestDeliverInjectsWhenIdleAndUnfocused` and `TestDeliverHoldsWhilePlannerPaneFocused` — a single binding's behaviour must be untouched.

- [ ] **Step 5: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/fuad/projects/relay
git add internal/relay/deliver.go internal/relay/deliver_test.go
git commit -m "fix(relay): never inject twice into one planner in a tick (#46)

Tick takes one agent snapshot and reconciles every binding against it, so
two bindings sharing a planner both read a stale idle and both call
agent prompt -- the second landing while the planner is mid-turn on the
first. That is precisely what the status gate in DeliverPending exists to
prevent.

DeliverPending now marks the planner working in the snapshot after it
injects, which is simply what has become true. Later bindings in the same
pass take the existing held path and deliver on a subsequent tick.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 3: Give the tick its own snapshot

**Files:**
- Modify: `internal/relay/daemon.go:63-67` (inside `Daemon.Tick`)
- Test: `internal/relay/daemon_test.go` (append)

**Interfaces:**
- Consumes: `Daemon.Tick(ctx context.Context) error` (existing); `queuedBinding(t, f)` and `plannerWith(status string, focused bool)` from `deliver_test.go` (same package, directly usable); `NewDaemon(rt Runtime, interval time.Duration) *Daemon`.
- Produces: nothing new. This task closes the leak Task 2 opened.

- [ ] **Step 1: Write the failing test**

Append to `internal/relay/daemon_test.go`:

```go
func TestTickDoesNotMutateTheHerdrAgentList(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := queuedBinding(t, f)
	f.agents = []herdr.Agent{plannerWith(herdr.StatusIdle, false)}
	f.prompts = nil

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(f.prompts) != 1 {
		t.Fatalf("expected the payload to be delivered, prompts = %+v", f.prompts)
	}

	// DeliverPending writes the planner's new status into the tick's snapshot.
	// That record belongs to the tick and must not reach the Herdr client's own
	// list, where it would outlive the pass that made it true.
	if f.agents[0].Status != herdr.StatusIdle {
		t.Errorf("Tick must own its snapshot; herdr's agent list now reads %q", f.agents[0].Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestTickDoesNotMutateTheHerdrAgentList -v`
Expected: FAIL — `Tick must own its snapshot; herdr's agent list now reads "working"`. The fake returns `f.agents` directly, so Task 2's write goes straight through to it.

- [ ] **Step 3: Write the implementation**

In `internal/relay/daemon.go`, find this exact block inside `Tick`:

```go
	agents, err := d.rt.Herdr.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
```

and replace it with:

```go
	agents, err := d.rt.Herdr.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	// Own the snapshot. DeliverPending records a planner it has just prompted
	// by marking it working in this slice, and that record is true only for the
	// remainder of this pass -- it must not reach the Herdr implementation's own
	// storage, nor survive into the next tick, which fetches fresh state anyway.
	agents = append([]herdr.Agent(nil), agents...)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestTick -v`
Expected: PASS, including the pre-existing tick tests (notably the one asserting `f.listCalls == 1`, since the copy adds no herdr calls).

- [ ] **Step 5: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/fuad/projects/relay
git add internal/relay/daemon.go internal/relay/daemon_test.go
git commit -m "fix(relay): copy the agent snapshot each tick

DeliverPending now writes a just-prompted planner's status back into the
snapshot so the rest of the pass sees it. That fact is true for the pass
and no longer, so the tick takes its own copy rather than writing through
to whatever the Herdr client handed back. Still one herdr call per tick.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 4: End-to-end guard through Tick

**Files:**
- Test: `internal/relay/daemon_test.go` (append)

**Interfaces:**
- Consumes: `twoBindingsOnePlanner` from Task 2; `Daemon.Tick`; `builderAgent(status string) herdr.Agent` from `reconcile_test.go`.
- Produces: nothing. This is the regression fence that proves Tasks 2 and 3 compose.

Both builders are present and idle in the snapshot so neither binding is short-circuited as broken — the test must fail for the right reason if the fix is reverted, not because a binding never reached delivery.

- [ ] **Step 1: Write the test**

Append to `internal/relay/daemon_test.go`:

```go
func TestTickInjectsOncePerPlannerPane(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, second := twoBindingsOnePlanner(t, f)

	f.agents = []herdr.Agent{
		plannerWith(herdr.StatusIdle, false),
		builderAgent(herdr.StatusIdle),
		{Kind: "agy", Status: herdr.StatusIdle, CWD: "/repo2", PaneID: "w2:p5", Title: "storefront-builder"},
	}
	f.prompts = nil

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	toPlanner := 0
	for _, p := range f.prompts {
		if p.Target == "w2:p3" {
			toPlanner++
		}
	}
	if toPlanner != 1 {
		t.Fatalf("one planner pane takes at most one injection per tick, got %d: %+v", toPlanner, f.prompts)
	}
	if _, pending, err := rt.Store.PendingForPlanner(second.Name); err != nil || !pending {
		t.Errorf("the payload that lost the race must still be pending: pending=%v err=%v", pending, err)
	}
}
```

- [ ] **Step 2: Run it and confirm it passes on the fixed code**

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestTickInjectsOncePerPlannerPane -v`
Expected: PASS.

- [ ] **Step 3: Prove it is a real fence**

Temporarily comment out the `if i, ok := findAgentIndex(...)` block added in Task 2, re-run the test, and confirm it FAILS with `got 2`. Then restore the block and confirm it passes again. Do not commit the commented-out state.

Run: `cd /home/fuad/projects/relay && go test ./internal/relay/ -run TestTickInjectsOncePerPlannerPane -v`
Expected: FAIL while the block is commented out; PASS once restored.

- [ ] **Step 4: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/fuad/projects/relay
git add internal/relay/daemon_test.go
git commit -m "test(relay): fence the fan-in clobber at the Tick level (#46)

Closes #46

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

## Notes for the reviewer

- **Why not coalesce payloads into one message?** Merging N reports into a single injection changes what `ConfirmLatest` means per binding and rewrites the payload contract the planner reads. One-per-tick costs the loser a single tick (~2s by default) and changes no semantics. Coalescing stays available later if the delay is ever felt.
- **Why mutate the snapshot rather than thread a `claimed` set through?** A `claimed map[string]bool` parameter would have to pass through `Reconcile`, whose exported signature is used by `internal/ui` and the tests. Marking the agent busy expresses the same fact, in the structure that already carries agent state, with no signature churn.
- **`relay pull` is unaffected.** It claims payloads without injecting, and never shares a snapshot across bindings.
