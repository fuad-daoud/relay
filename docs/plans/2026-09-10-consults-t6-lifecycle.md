# Consults Task 6: consult lifecycle in the daemon

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 6 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 4 (relay ask), Task 5 (foreign fix). Verify before you start:

```bash
test -f internal/relay/ask.go   # Task 4 (relay ask)
grep -q 'range b.Consults' internal/relay/foreign.go   # Task 5 (foreign fix)
```

If any check fails, **stop immediately and report it**. Your base is wrong, and
every step below will fail in confusing ways. This is not something to work
around by implementing the missing piece yourself -- that is another task's
work and would collide with it.

## Where you are working

Two directories look almost identical in tool output. Getting them confused
costs a builder several tool calls, and an edit to the wrong one silently does
nothing:

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-diff.patch`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it (`internal/store/types.go`)
over absolute ones, and if you must go absolute, check the `.worktrees/`
segment is present.

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

### Task 6: Consult lifecycle in the daemon

**Files:**
- Create: `internal/relay/consult.go`
- Create: `internal/relay/consult_test.go`
- Modify: `internal/relay/reconcile.go:100-163` (call site)
- Modify: `internal/ui/fetch.go:93`

**Interfaces:**
- Consumes: everything from Tasks 1-5.
- Produces:
  - `func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error)`
  - `func finishConsult(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, i int, state store.ConsultState, note string) (store.Binding, error)`
  - `const consultTimeout = 10 * time.Minute`, `const consultGrace = 60 * time.Second`

- [ ] **Step 1: Write the failing tests**

Create `internal/relay/consult_test.go`:

```go
package relay

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// consultAgent is the live pane a seeded consult occupies.
func consultAgent(status string) herdr.Agent {
	return herdr.Agent{
		Kind: "claude", Status: status, CWD: "/repo", PaneID: "w2:p9",
		Title: "webshop-reviewer-7f2a3c1d",
	}
}

// seedConsult puts one running consult on a bound binding and returns the
// runtime, a movable clock, and the consult record.
func seedConsult(t *testing.T, f *fakeHerdr) (Runtime, *fakeClock, store.Consult) {
	t.Helper()
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "review it")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	f.agents = append(f.agents, consultAgent(herdr.StatusWorking))
	clock := &fakeClock{now: baseTime}
	return withClock(rt, clock), clock, res.Consult
}

// tickConsults runs one reconcile pass over the consults only.
func tickConsults(t *testing.T, rt Runtime, f *fakeHerdr) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		out, err = reconcileConsults(context.Background(), rt, tx, b, f.agents)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("reconcileConsults: %v", err)
	}
	return out
}

func writeFindings(t *testing.T, c store.Consult) {
	t.Helper()
	if err := os.WriteFile(c.FindingsPath, []byte("looks fine"), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
}

func setConsultAgentStatus(f *fakeHerdr, status string) {
	for i := range f.agents {
		if f.agents[i].PaneID == "w2:p9" {
			f.agents[i].Status = status
		}
	}
}

func TestConsultWithFindingsAndIdleIsDelivered(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("nothing queued for the planner: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindFindings || pending.Direction != store.DirToPlanner {
		t.Errorf("entry = %s/%s, want to_planner/findings", pending.Direction, pending.Kind)
	}
	if !strings.Contains(pending.Payload, c.FindingsPath) {
		t.Errorf("payload does not name the findings path: %q", pending.Payload)
	}
	if pending.Path != c.FindingsPath {
		t.Errorf("Path = %q, want the findings path", pending.Path)
	}
}

func TestConsultIdleWithoutFindingsIsNudgedOnce(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusIdle)
	before := len(f.prompts)

	b := tickConsults(t, rt, f)
	if b.Consults[0].NudgedAt.IsZero() {
		t.Fatal("NudgedAt not stamped")
	}
	if len(f.prompts) != before+1 {
		t.Fatalf("got %d prompts, want 1 nudge", len(f.prompts)-before)
	}

	// A second tick inside the grace window must not nudge again.
	clock.Advance(consultGrace / 2)
	tickConsults(t, rt, f)
	if len(f.prompts) != before+1 {
		t.Errorf("nudged %d times; a consult gets exactly one", len(f.prompts)-before)
	}
}

func TestConsultGoesSilentAfterTheGraceWindow(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusIdle)

	tickConsults(t, rt, f) // nudge
	clock.Advance(consultGrace + 1)
	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("silence was not reported: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "wrote no findings") {
		t.Errorf("payload = %q", pending.Payload)
	}
	if pending.Path != "" {
		t.Errorf("Path = %q; a silent consult wrote no file to point at", pending.Path)
	}
}

func TestBlockedConsultIsReportedNotNegotiatedWith(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)
	setConsultAgentStatus(f, herdr.StatusBlocked)
	before := len(f.prompts)

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if len(f.prompts) != before {
		t.Error("relay prompted a blocked consult; a consult is one-shot and is not answered")
	}
	pending, _, _ := rt.Store.PendingForPlanner("webshop")
	if !strings.Contains(pending.Payload, "still open") {
		t.Errorf("payload must tell the human the pane survives: %q", pending.Payload)
	}
}

func TestConsultPaneGoneWithFindingsStillDelivers(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	// The pane wrote its findings and then died.
	f.agents = f.agents[:len(f.agents)-1]

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done: the findings file is the record, not the pane", b.Consults[0].State)
	}
}

func TestConsultPaneGoneWithoutFindingsIsSilent(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, _ := seedConsult(t, f)
	f.agents = f.agents[:len(f.agents)-1]

	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "gone") {
		t.Errorf("note = %q", b.Consults[0].Note)
	}
}

func TestWorkingConsultTimesOut(t *testing.T) {
	f := &fakeHerdr{}
	rt, clock, _ := seedConsult(t, f)
	// Status stays `working`, so the nudge path never runs.

	if b := tickConsults(t, rt, f); b.Consults[0].State != store.ConsultRunning {
		t.Fatalf("gave up early: %q", b.Consults[0].State)
	}

	clock.Advance(consultTimeout + 1)
	b := tickConsults(t, rt, f)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent after the timeout", b.Consults[0].State)
	}
}

// This is the mutation-test target named in the spec: delete the
// `State != ConsultRunning` guard at the top of reconcileConsults and this
// fails. Without the guard every tick re-queues findings already delivered.
func TestTerminalConsultsAreNeverRevisited(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)

	tickConsults(t, rt, f) // -> done, one findings entry

	countFindings := func() int {
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		n := 0
		for _, e := range entries {
			if e.Kind == store.KindFindings {
				n++
			}
		}
		return n
	}
	if countFindings() != 1 {
		t.Fatalf("got %d findings entries after one tick, want 1", countFindings())
	}

	promptsBefore := len(f.prompts)
	for i := 0; i < 5; i++ {
		tickConsults(t, rt, f)
	}

	if got := countFindings(); got != 1 {
		t.Errorf("got %d findings entries after 6 ticks, want 1: a terminal consult must never be re-queued", got)
	}
	if len(f.prompts) != promptsBefore {
		t.Errorf("a terminal consult was prompted %d times", len(f.prompts)-promptsBefore)
	}
}

func TestReconcileConsultsMakesNoHerdrCallsForTerminalRecords(t *testing.T) {
	f := &fakeHerdr{}
	rt, _, c := seedConsult(t, f)
	writeFindings(t, c)
	setConsultAgentStatus(f, herdr.StatusIdle)
	tickConsults(t, rt, f)

	listsBefore := f.listCalls
	tickConsults(t, rt, f)

	if f.listCalls != listsBefore {
		t.Errorf("reconcileConsults made %d ListAgents calls; it reads the snapshot it is given", f.listCalls-listsBefore)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/relay/ -run 'TestConsult|TestBlockedConsult|TestWorkingConsult|TestTerminalConsults|TestReconcileConsults' -v
```

Expected: FAIL to compile — `reconcileConsults`, `consultGrace`, `consultTimeout` undefined.

- [ ] **Step 3: Write the lifecycle**

Create `internal/relay/consult.go`:

```go
package relay

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

const (
	// consultTimeout is how long a consult may stay `working` before relay
	// gives up on it. A consult reads and writes one file; the alternative to a
	// deadline is a record that never becomes terminal and so is never reaped.
	consultTimeout = 10 * time.Minute

	// consultGrace is how long after its single nudge a consult has to write
	// findings before relay reports that it wrote none.
	//
	// A consult does NOT inherit the builder's screen-fingerprint quiescence or
	// scrape fallback. Those exist because a builder runs for hours and a quiet
	// builder is usually a live one waiting on its own sub-agents. A consult
	// runs for a minute or two, so idle plus a nudge plus a minute is enough
	// evidence.
	consultGrace = 60 * time.Second
)

const consultNudgePrompt = `You went idle without writing your findings.

Write them to: %s

Reply here with only that path.`

// reconcileConsults advances every running consult on one binding by one tick.
//
// It reads the agent snapshot it is given rather than fetching one, so a tick
// costs zero additional herdr calls no matter how many consults are attached.
//
// Preconditions:  the caller holds the state lock and passes its tx.
// Postconditions: each running consult is unchanged, nudged, or moved to done
//
//	or silent with exactly one findings entry queued for the
//	transition. Terminal records are never revisited.
func reconcileConsults(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, agents []herdr.Agent) (store.Binding, error) {
	now := rt.Now().UTC()

	for i := range b.Consults {
		// Terminal records stay until `relay reap` closes their pane. Without
		// this guard every tick re-queues findings that were already
		// delivered, which is one notification per poll, forever.
		if b.Consults[i].State != store.ConsultRunning {
			continue
		}

		agent, live := FindAgent(agents, b.Consults[i].Endpoint)
		if !live {
			// The findings file is the record, not the pane: a consult that
			// wrote and then died has still done its job.
			state, note := store.ConsultSilent, "consult pane is gone"
			if fileExists(b.Consults[i].FindingsPath) {
				state, note = store.ConsultDone, ""
			}
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, state, note); err != nil {
				return b, err
			}
			continue
		}

		b.Consults[i].Endpoint = refreshEndpoint(b.Consults[i].Endpoint, agent)

		idle := agent.Status == herdr.StatusIdle || agent.Status == herdr.StatusDone

		if idle && fileExists(b.Consults[i].FindingsPath) {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultDone, ""); err != nil {
				return b, err
			}
			continue
		}

		// A blocked consult is reported and abandoned, not negotiated with.
		// `relay answer` stays builder-only: a one-shot agent that needs a
		// conversation has already failed its contract. The pane stays open so
		// a human can answer the dialog and the planner can re-ask.
		if agent.Status == herdr.StatusBlocked {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"blocked on a prompt in pane "+agent.PaneID); err != nil {
				return b, err
			}
			continue
		}

		if idle {
			if b.Consults[i].NudgedAt.IsZero() {
				text := fmt.Sprintf(consultNudgePrompt, b.Consults[i].FindingsPath)
				if err := promptWithRetry(ctx, rt, agent.PaneID, text); err != nil {
					// A failed prompt is not evidence the consult stopped.
					continue
				}
				b.Consults[i].NudgedAt = now
				continue
			}
			if now.Sub(b.Consults[i].NudgedAt) >= consultGrace {
				var err error
				if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
					"went idle without writing findings"); err != nil {
					return b, err
				}
			}
			continue
		}

		if now.Sub(b.Consults[i].SpawnedAt) >= consultTimeout {
			var err error
			if b, err = finishConsult(ctx, rt, tx, b, i, store.ConsultSilent,
				"no findings after "+consultTimeout.String()); err != nil {
				return b, err
			}
		}
	}

	return b, nil
}

// finishConsult queues one planner-bound entry and marks the record terminal.
//
// It goes through Queue rather than appending directly, which is what makes
// consults inherit the anti-clobber rule, held, notifications and `relay pull`
// without a line of new delivery code.
func finishConsult(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, i int, state store.ConsultState, note string) (store.Binding, error) {
	c := b.Consults[i]

	entry := store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     c.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindFindings,
		Note:      note,
	}

	if state == store.ConsultDone {
		entry.Path = c.FindingsPath
		entry.Payload = fmt.Sprintf("Findings from %s consult %s: %s", c.Role, c.ID, c.FindingsPath)
	} else {
		// No Path: a silent consult wrote no file, and pointing at one that
		// does not exist would send the planner to read nothing.
		entry.Payload = fmt.Sprintf("Consult %s (%s) wrote no findings: %s. Pane %s is still open.",
			c.ID, c.Role, note, c.Endpoint.PaneID)
	}

	if err := Queue(ctx, rt, tx, b.Name, entry); err != nil {
		return b, err
	}

	b.Consults[i].State = state
	b.Consults[i].Note = note

	return b, nil
}

// fileExists is the entire completion gate for a consult. It is a fact, not an
// assessment: relay never reads findings to judge them.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

If `fileExists` already exists in the package, use the existing one and drop this copy.

- [ ] **Step 4: Call it from `Reconcile`**

In `internal/relay/reconcile.go`, insert immediately after the `StateDone` guard (`:107-109`) and **before** the builder lookup:

```go
	// Consults reconcile before the builder is located, because they are
	// orthogonal to it: a reviewer reading a diff has no stake in whether the
	// builder's pane still exists. Reconcile returns early both when the
	// builder is gone (below) and on the round-cap halt, and neither should
	// stop a consult from finishing.
	//
	// On those early-return paths deliverAndSettle is skipped, so queued
	// findings wait on disk and `relay pull` retrieves them -- the same
	// behaviour the halt comment below describes for a halted binding.
	b, err = reconcileConsults(ctx, rt, tx, b, agents)
	if err != nil {
		return b, err
	}
```

`err` is already the named return, so use `=` not `:=`.

- [ ] **Step 5: Surface findings in the TUI**

In `internal/ui/fetch.go:93`, add `KindFindings` to the filter:

```go
			if e.Direction == store.DirToPlanner &&
				(e.Kind == store.KindReport || e.Kind == store.KindQuestion || e.Kind == store.KindFindings) {
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make check
```

- [ ] **Step 7: Run the mutation test**

Delete the three-line guard at the top of the loop in `reconcileConsults`:

```go
		if b.Consults[i].State != store.ConsultRunning {
			continue
		}
```

```bash
go test ./internal/relay/ -run TestTerminalConsultsAreNeverRevisited -v
```

Expected: **FAIL**, reporting 6 findings entries instead of 1. Restore the guard and confirm it passes again. If it passes with the guard deleted, the test is pinning nothing — fix the test before continuing.

- [ ] **Step 8: Commit**

```bash
git add internal/relay/consult.go internal/relay/consult_test.go internal/relay/reconcile.go internal/ui/fetch.go
git commit -m "feat(relay): watch consults from the daemon tick

reconcileConsults reads the agent snapshot Reconcile already fetched, so N
consults cost zero extra herdr calls. The gate is whether the findings file
exists.

It runs before the builder is located: Reconcile returns early when the
builder is gone and again on the round-cap halt, and a consult reading a diff
has no stake in either.

The failure path stays dumb by design -- one nudge, then a report that nothing
was written. No screen fingerprinting, no scrape fallback, and a blocked
consult is reported rather than answered."
```
