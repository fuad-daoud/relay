# Consults Task 7: relay reap

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 7 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 2 (consult schema). Verify before you start:

```bash
grep -q 'ConsultRunning' internal/store/types.go   # Task 2 (consult schema)
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

### Task 7: `relay reap`

The only place relay closes a pane.

**Files:**
- Create: `internal/relay/reap.go`
- Create: `internal/relay/reap_test.go`
- Modify: `internal/herdr/client.go` (`ClosePane`)
- Modify: `internal/relay/herdr.go` (the `Herdr` interface)
- Modify: `internal/relay/fake_test.go` (`fakeHerdr.ClosePane`)
- Modify: `cmd/relay/main.go` (subcommand + help)

**Interfaces:**
- Consumes: `store.Consult`, `store.ConsultRunning` (Task 2).
- Produces:
  - `func (c *Client) ClosePane(ctx context.Context, paneID string) error`
  - `Herdr` interface gains `ClosePane(ctx context.Context, paneID string) error`
  - `type ReapOptions struct { Name string; All, DryRun bool }`
  - `type ReapResult struct { Binding string; Closed, Failed []store.Consult }`
  - `func Reap(ctx context.Context, rt Runtime, opts ReapOptions) ([]ReapResult, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/relay/reap_test.go`:

```go
package relay

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func seedReapable(t *testing.T, rt Runtime) {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultDone, Endpoint: store.Endpoint{PaneID: "w2:p9"}},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultSilent, Endpoint: store.Endpoint{PaneID: "w2:pA"}},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultRunning, Endpoint: store.Endpoint{PaneID: "w2:pB"}},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestReapClosesTerminalConsultsAndKeepsRunningOnes(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop"})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res) != 1 || len(res[0].Closed) != 2 {
		t.Fatalf("closed %+v, want the two terminal consults", res)
	}

	if len(f.closed) != 2 {
		t.Fatalf("closed %d panes, want 2", len(f.closed))
	}
	for _, pane := range f.closed {
		if pane == "w2:pB" {
			t.Error("reap closed a RUNNING consult's pane")
		}
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Consults) != 1 || b.Consults[0].ID != "cccccccc" {
		t.Errorf("consults = %+v, want only the running one", b.Consults)
	}
}

func TestReapDryRunClosesNothing(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop", DryRun: true})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(res[0].Closed) != 2 {
		t.Errorf("dry run reported %d, want the 2 it would close", len(res[0].Closed))
	}
	if len(f.closed) != 0 {
		t.Errorf("dry run closed %d panes", len(f.closed))
	}

	b, _ := rt.Store.Load("webshop")
	if len(b.Consults) != 3 {
		t.Errorf("dry run dropped records: %+v", b.Consults)
	}
}

func TestReapKeepsTheRecordWhenTheCloseFails(t *testing.T) {
	f := &fakeHerdr{closeErr: errors.New("no such pane")}
	rt, _ := seedBound(t, f)
	seedReapable(t, rt)

	res, err := Reap(context.Background(), rt, ReapOptions{Name: "webshop"})
	if err != nil {
		t.Fatalf("Reap must not fail the whole sweep over one pane: %v", err)
	}
	if len(res[0].Failed) != 2 || len(res[0].Closed) != 0 {
		t.Fatalf("result = %+v", res[0])
	}

	b, _ := rt.Store.Load("webshop")
	if len(b.Consults) != 3 {
		t.Errorf("a failed close dropped the record, so a retry is impossible: %+v", b.Consults)
	}
}
```

Add to `fakeHerdr` in `internal/relay/fake_test.go`:

```go
	closed   []string
	closeErr error
```

```go
func (f *fakeHerdr) ClosePane(_ context.Context, paneID string) error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, paneID)
	return nil
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/relay/ -run TestReap -v
```

Expected: FAIL to compile — `Reap`, `ReapOptions` undefined.

- [ ] **Step 3: Add `ClosePane` to the client and the interface**

In `internal/herdr/client.go`, beside `SplitPane`:

```go
// ClosePane closes a pane. relay calls this from exactly one place, `relay
// reap`, and only for a consult pane relay spawned itself.
func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	_, err := c.run(ctx, "pane", "close", paneID)
	return err
}
```

Check the real flag shape with `herdr pane close --help` before writing it; adjust the argv if it differs. Per `CLAUDE.md`, if the correct invocation cannot be established, **stop and report** rather than guessing — a wrong argv here closes the wrong pane.

Add to the `Herdr` interface in `internal/relay/herdr.go`:

```go
	ClosePane(ctx context.Context, paneID string) error
```

- [ ] **Step 4: Write `Reap`**

Create `internal/relay/reap.go`:

```go
package relay

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relay/internal/store"
)

// ErrUnknownConsult reports a reap naming a consult no binding holds.
var ErrUnknownConsult = errors.New("no such consult")

// ReapOptions selects what to reap.
type ReapOptions struct {
	Name   string // one binding; ignored when All is set
	All    bool
	DryRun bool
}

// ReapResult is what one binding's reap did.
type ReapResult struct {
	Binding string
	Closed  []store.Consult // pane closed, record dropped
	Failed  []store.Consult // close failed, record kept for a retry
}

// Reap closes the panes of terminal consults and drops their records.
//
// This is the only place relay closes a pane, it closes only panes relay
// spawned itself, and it runs because a human or a planner asked -- never
// because relay judged an outcome. docs/design.md's "Relay never kills a pane"
// is amended to name this one command.
//
// Running consults are never touched. A failed close keeps the record so a
// retry is possible and never fails the sweep: one unreachable pane must not
// strand the rest.
func Reap(ctx context.Context, rt Runtime, opts ReapOptions) ([]ReapResult, error) {
	var names []string

	if opts.All {
		bindings, err := rt.Store.List()
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			names = append(names, b.Name)
		}
	} else {
		if opts.Name == "" {
			return nil, fmt.Errorf("relay reap needs a binding name or --all")
		}
		names = []string{opts.Name}
	}

	var out []ReapResult
	for _, name := range names {
		res := ReapResult{Binding: name}

		err := rt.Store.WithLock(func(tx *store.Tx) error {
			b, err := tx.Load(name)
			if err != nil {
				return err
			}

			keep := make([]store.Consult, 0, len(b.Consults))
			for _, c := range b.Consults {
				if c.State == store.ConsultRunning {
					keep = append(keep, c)
					continue
				}
				if opts.DryRun {
					res.Closed = append(res.Closed, c)
					keep = append(keep, c)
					continue
				}
				if err := rt.Herdr.ClosePane(ctx, c.Endpoint.PaneID); err != nil {
					res.Failed = append(res.Failed, c)
					keep = append(keep, c)
					continue
				}
				res.Closed = append(res.Closed, c)
			}

			if opts.DryRun {
				return nil
			}

			b.Consults = keep
			return tx.Save(b)
		})
		if err != nil {
			return out, err
		}

		out = append(out, res)
	}

	return out, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
make check
```

- [ ] **Step 6: Wire the CLI**

Add `case "reap":` to the switch in `cmd/relay/main.go`, with `--all` and `--dry-run` flags and a positional binding name through `resolveBinding`. Print one line per closed and failed consult:

```go
	for _, r := range results {
		for _, c := range r.Closed {
			verb := "closed"
			if dryRun {
				verb = "would close"
			}
			fmt.Printf("%s %s consult %s (pane %s) on %s\n", verb, c.Role, c.ID, c.Endpoint.PaneID, r.Binding)
		}
		for _, c := range r.Failed {
			fmt.Printf("could not close pane %s for consult %s on %s; record kept\n", c.Endpoint.PaneID, c.ID, r.Binding)
		}
	}
```

Add a `reap` line to the help text.

- [ ] **Step 7: Commit**

```bash
git add internal/relay/reap.go internal/relay/reap_test.go internal/relay/herdr.go internal/relay/fake_test.go internal/herdr/client.go cmd/relay/main.go
git commit -m "feat(relay): add relay reap, the one place relay closes a pane

Auto-closing on success was rejected: it would make relay's first pane kill
conditional on an outcome check, and a consult that wrote findings and then
crashed would lose the terminal that explains why.

Running consults are never touched, and a failed close keeps the record so a
retry is possible rather than stranding the pane silently."
```
