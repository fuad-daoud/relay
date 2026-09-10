# Consults Task 4: relay ask

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add a neighbouring one -- other builders work those in
> their own worktrees and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 4 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Prerequisites -- check these FIRST

This task builds on work another builder already landed. Your worktree was cut
from a branch that should already contain Task 2 (consult schema), Task 3 (role-aware aliases). Verify before you start:

```bash
grep -q 'type Consult struct' internal/store/types.go   # Task 2 (consult schema)
grep -q 'func (s Spec) IsConsult' internal/alias/alias.go   # Task 3 (role-aware aliases)
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

### Task 4: `relay ask`

Spawn one consult and record it. No waiting, no delivery.

**Files:**
- Create: `internal/relay/ask.go`
- Create: `internal/relay/ask_test.go`
- Modify: `internal/relay/herdr.go:44-51` (`Runtime`)
- Modify: `cmd/relay/main.go` (subcommand + help)

**Interfaces:**
- Consumes: `store.Consult`, `store.ConsultRunning`, `store.DefaultConsultCap`, `store.KindAsk`, `store.DirToConsult`, `Store.AskPath`, `Store.FindingsPath` (Task 2); `alias.Spec.IsConsult`, `alias.Spec.Tree` (Task 3).
- Produces:
  - `type AskOptions struct { Role, File, Name, PlannerPane string; NewTab bool; WorkspaceID string }`
  - `type AskResult struct { Consult store.Consult; Binding string }`
  - `func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error)`
  - `ErrNotAConsultRole`, `ErrTreelessUnsupported`, `ErrConsultCap`
  - `Runtime.NewID func() string` — nil means a crypto/rand id

- [ ] **Step 1: Write the failing tests**

Create `internal/relay/ask_test.go`:

```go
package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// seedForAsk puts a binding in place with a consult role available and a fixed
// consult id, so filenames and agent names are assertable.
func seedForAsk(t *testing.T, f *fakeHerdr) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedBound(t, f)
	rt.Aliases = consultTable(t)
	rt.NewID = func() string { return "7f2a3c1d" }
	f.newPane = "w2:p9"
	return rt, b
}

func writeQuestion(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "q.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	return path
}

func TestAskSpawnsRecordsAndStagesTheQuestion(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "Review 003-diff.patch against the plan.")

	res, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if res.Consult.ID != "7f2a3c1d" || res.Consult.Role != "reviewer" {
		t.Errorf("consult = %+v", res.Consult)
	}
	if res.Consult.State != store.ConsultRunning {
		t.Errorf("state = %q, want running", res.Consult.State)
	}

	// The question is staged into state, the way Send stages a plan.
	body, err := os.ReadFile(res.Consult.AskPath)
	if err != nil {
		t.Fatalf("read staged question: %v", err)
	}
	if string(body) != "Review 003-diff.patch against the plan." {
		t.Errorf("staged question = %q", body)
	}

	if len(f.starts) != 1 {
		t.Fatalf("got %d starts, want 1", len(f.starts))
	}
	if f.starts[0].Name != "webshop-reviewer-7f2a3c1d" || f.starts[0].Kind != "claude" {
		t.Errorf("start = %+v", f.starts[0])
	}

	// The prompt names both paths and asks for only the findings path back.
	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	text := f.prompts[0].Text
	for _, want := range []string{res.Consult.AskPath, res.Consult.FindingsPath, "only that path"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt missing %q:\n%s", want, text)
		}
	}

	// The record is durable, so the daemon finds it after a restart.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 || got.Consults[0].ID != "7f2a3c1d" {
		t.Fatalf("consults = %+v", got.Consults)
	}
}

func TestAskDoesNotAdvanceTheRound(t *testing.T) {
	f := &fakeHerdr{}
	rt, before := seedForAsk(t, f)
	q := writeQuestion(t, "look at this")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Round != before.Round {
		t.Errorf("round moved %d -> %d; a consult is orthogonal to the builder's round", before.Round, after.Round)
	}
	if !after.RoundStartedAt.Equal(before.RoundStartedAt) {
		t.Error("RoundStartedAt was restamped by a consult")
	}
}

func TestAskRefusesABuilderAlias(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	_, err := Ask(context.Background(), rt, AskOptions{
		Role: "abuilder", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})

	if !errors.Is(err, ErrNotAConsultRole) {
		t.Fatalf("want ErrNotAConsultRole, got %v", err)
	}
	if f.splits != 0 {
		t.Error("a refused ask split a pane; validation must precede spawning")
	}
}

func TestAskRefusesAtTheConsultCap(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.ConsultCap = 1
	b.Consults = []store.Consult{{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	})
	if !errors.Is(err, ErrConsultCap) {
		t.Fatalf("want ErrConsultCap, got %v", err)
	}
}

func TestAskCountsOnlyRunningConsultsAgainstTheCap(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.ConsultCap = 1
	// A terminal consult is waiting to be reaped, not occupying a slot.
	b.Consults = []store.Consult{{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultDone}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask refused over a reapable consult: %v", err)
	}
}

func TestAskRecordsAReapableConsultWhenTheSpawnFails(t *testing.T) {
	// The pane exists by the time Prompt fails. Returning the error and walking
	// away would strand it, which is what resolveBuilder does today and what
	// CLAUDE.md warns costs ~800 MB indefinitely.
	f := &fakeHerdr{promptErr: errors.New("herdr exploded")}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err == nil {
		t.Fatal("Ask returned nil after a prompt failure")
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1 reapable record", len(got.Consults))
	}
	if got.Consults[0].State != store.ConsultSilent {
		t.Errorf("state = %q, want silent so the pane is reapable", got.Consults[0].State)
	}
	if got.Consults[0].Endpoint.PaneID != "w2:p9" {
		t.Errorf("pane = %q; the record must name the pane so reap can close it", got.Consults[0].Endpoint.PaneID)
	}
}

func TestAskLogsTheQuestionOutbound(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedForAsk(t, f)
	q := writeQuestion(t, "x")

	if _, err := Ask(context.Background(), rt, AskOptions{
		Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Direction != store.DirToConsult || last.Kind != store.KindAsk {
		t.Errorf("entry = %s/%s, want to_consult/ask", last.Direction, last.Kind)
	}
	if !last.Confirmed {
		t.Error("an outbound entry must be confirmed, or the pending scan will try to deliver it to the planner")
	}
	// The pending scan must be untouched by an outbound consult entry.
	if _, found, err := rt.Store.PendingForPlanner("webshop"); err != nil || found {
		t.Errorf("ask entry showed up as pending for the planner: found=%v err=%v", found, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/relay/ -run TestAsk -v
```

Expected: FAIL to compile — `Ask`, `AskOptions`, `Runtime.NewID` undefined.

- [ ] **Step 3: Add `NewID` to `Runtime`**

In `internal/relay/herdr.go`, add to the `Runtime` struct:

```go
	// NewID mints a consult id. Nil means a crypto/rand id, so no production
	// call site has to set it and tests can make ids deterministic.
	NewID func() string
```

- [ ] **Step 4: Write `Ask`**

Create `internal/relay/ask.go`:

```go
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// ErrNotAConsultRole reports an ask naming an alias that is a builder. A
// builder is persistent and writes; handing one a prompt with no plan in it
// would start a round that never was.
var ErrNotAConsultRole = errors.New("that alias is a builder, not a consult role")

// ErrTreelessUnsupported reports an ask for a role declaring tree "none".
// The field is forward-declared for a future treeless explorer; no treeless
// role ships yet, and silently running one in the binding's tree would put an
// agent somewhere its role did not ask for.
var ErrTreelessUnsupported = errors.New("treeless consult roles are not implemented")

// ErrConsultCap reports an ask that would exceed the binding's running-consult
// cap. An idle harness pane holds roughly 800 MB.
var ErrConsultCap = errors.New("binding is at its consult cap")

// consultPrompt is what relay types into a freshly spawned consult.
//
// The last line is an instruction to the model, not a constraint relay
// enforces: relay cannot observe writes.
const consultPrompt = `Read: %s

Write your findings to: %s

Reply here with only that path. Do not modify any file in this repository.`

// AskOptions describes one consult request.
type AskOptions struct {
	Role        string // consult role to spawn; required
	File        string // the question file; required
	Name        string // binding name, already resolved by the caller
	PlannerPane string // $HERDR_PANE_ID; required
	NewTab      bool
	WorkspaceID string
}

// AskResult is what an ask produced, so the CLI can tell the planner where the
// findings will appear without re-deriving the path.
type AskResult struct {
	Consult store.Consult
	Binding string
}

// Ask spawns one read-only, one-shot consult beside a binding's builder and
// returns immediately. The daemon watches for its findings.
//
// Preconditions:  opts.PlannerPane names a live agent pane; opts.File is
//
//	readable; opts.Role resolves to a consult spec whose Tree is
//	not "none"; the binding exists and is neither broken nor done;
//	running consults are below the binding's cap.
//
// Postconditions: on success a ConsultRunning record exists on the binding, the
//
//	question is staged at AskPath, one DirToConsult/KindAsk entry
//	is logged, and a pane is running the role. On a VALIDATION
//	failure nothing was spawned. On a SPAWN failure after the pane
//	exists, a ConsultSilent record is written so `relay reap` can
//	close the pane rather than stranding it.
//
// Errors: ErrNotAConsultRole, ErrTreelessUnsupported, ErrConsultCap,
//
//	alias.ErrUnknownAlias, store.ErrNotFound, or a wrapped herdr failure.
func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error) {
	if opts.PlannerPane == "" {
		return AskResult{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}

	// Read the caller's file before taking the lock; it is the one input that
	// does not depend on binding state.
	body, err := os.ReadFile(opts.File)
	if err != nil {
		return AskResult{}, fmt.Errorf("read question %s: %w", opts.File, err)
	}

	spec, err := rt.Aliases.Lookup(opts.Role)
	if err != nil {
		return AskResult{}, err
	}
	if !spec.IsConsult() {
		return AskResult{}, fmt.Errorf("%q is a builder alias: %w", opts.Role, ErrNotAConsultRole)
	}
	if spec.Tree == "none" {
		return AskResult{}, fmt.Errorf("role %q declares tree \"none\": %w", opts.Role, ErrTreelessUnsupported)
	}

	newID := rt.NewID
	if newID == nil {
		newID = randomConsultID
	}

	var out store.Consult

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if b.State == store.StateBroken || b.State == store.StateDone {
			return fmt.Errorf("binding %q is %s; a consult needs a live binding to attach to", b.Name, b.State)
		}
		if runningConsults(b) >= consultCap(b) {
			return fmt.Errorf("binding %q has %d running consults (cap %d), `relay reap` to free a slot: %w",
				b.Name, runningConsults(b), consultCap(b), ErrConsultCap)
		}

		id := newID()
		c := store.Consult{
			ID:           id,
			Role:         spec.Name,
			Round:        b.Round,
			AskPath:      rt.Store.AskPath(b.Name, b.Round, id),
			FindingsPath: rt.Store.FindingsPath(b.Name, b.Round, id),
			State:        store.ConsultRunning,
			SpawnedAt:    rt.Now().UTC(),
		}

		if err := os.WriteFile(c.AskPath, body, 0o644); err != nil {
			return fmt.Errorf("stage question at %s: %w", c.AskPath, err)
		}

		agentName := b.Name + "-" + spec.Name + "-" + id
		pane, err := consultPane(ctx, rt, opts, b.CWD, agentName)
		if err != nil {
			return err
		}
		c.Endpoint = store.Endpoint{AgentName: agentName, PaneID: pane, Kind: spec.Kind}

		// From here the pane exists. Every failure records the consult as
		// silent so `relay reap` can close it; returning the bare error would
		// strand a pane with nothing pointing at it.
		strand := func(reason string, cause error) error {
			c.State, c.Note = store.ConsultSilent, reason+": "+brief(cause)
			b.Consults = append(b.Consults, c)
			if err := tx.Save(b); err != nil {
				return err
			}
			out = c
			return cause
		}

		if err := rt.Herdr.StartAgent(ctx, agentName, spec.Kind, pane, spec.Args); err != nil {
			return strand("start failed", fmt.Errorf("start consult %q: %w", agentName, err))
		}

		// Best effort, exactly as resolveBuilder does it: the lookup races the
		// agent's registration, and failing here would strand a live pane over
		// an id Reconcile backfills on a later tick.
		if agents, err := rt.Herdr.ListAgents(ctx); err == nil {
			if started, ok := FindAgent(agents, store.Endpoint{PaneID: pane}); ok {
				c.Endpoint.SessionID = started.Session.Value
			}
		}

		text := spec.Preamble
		if text != "" {
			text += "\n\n"
		}
		text += fmt.Sprintf(consultPrompt, c.AskPath, c.FindingsPath)

		if err := promptWithRetry(ctx, rt, pane, text); err != nil {
			return strand("prompt failed", fmt.Errorf("prompt consult: %w", err))
		}

		entry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     c.Round,
			Direction: store.DirToConsult,
			Kind:      store.KindAsk,
			Path:      c.AskPath,
			Note:      spec.Name + " " + id,
			Confirmed: true,
		}
		if err := tx.AppendLog(b.Name, entry); err != nil {
			return err
		}

		b.Consults = append(b.Consults, c)
		if err := tx.Save(b); err != nil {
			return err
		}

		out = c
		return nil
	})
	if err != nil {
		return AskResult{Consult: out, Binding: opts.Name}, err
	}

	return AskResult{Consult: out, Binding: opts.Name}, nil
}

// consultPane makes somewhere for the consult to live: its own tab when asked,
// otherwise a sibling pane beside the planner. Focus stays with the planner
// either way. It mirrors builderPane; they are kept separate because a consult
// takes its cwd from the binding rather than from bind options.
func consultPane(ctx context.Context, rt Runtime, opts AskOptions, cwd, agentName string) (string, error) {
	if opts.NewTab {
		pane, err := rt.Herdr.CreateTab(ctx, opts.WorkspaceID, cwd, agentName)
		if err != nil {
			return "", fmt.Errorf("create tab for consult: %w", err)
		}
		return pane, nil
	}

	pane, err := rt.Herdr.SplitPane(ctx, opts.PlannerPane, splitDirection, cwd)
	if err != nil {
		return "", fmt.Errorf("split pane for consult: %w", err)
	}
	return pane, nil
}

// runningConsults counts only the consults still working. A terminal record is
// waiting to be reaped and is not occupying a pane slot the cap cares about --
// it is occupying a pane, but one the human has been told to reap.
func runningConsults(b store.Binding) int {
	n := 0
	for _, c := range b.Consults {
		if c.State == store.ConsultRunning {
			n++
		}
	}
	return n
}

func consultCap(b store.Binding) int {
	if b.ConsultCap > 0 {
		return b.ConsultCap
	}
	return store.DefaultConsultCap
}

// randomConsultID returns 8 hex characters. A failed CSPRNG read is not a
// reason to refuse a consult: the id only has to be unique within one binding,
// and the clock is sufficient for that.
func randomConsultID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/relay/ -run TestAsk -v
```

Expected: PASS, all eight.

- [ ] **Step 6: Wire the CLI**

In `cmd/relay/main.go`, add `case "ask":` to the subcommand switch (`:162-204`), after `case "send":`, and write the handler beside the `send` one. Follow the existing shape exactly:

```go
func runAsk(ctx context.Context, rt relay.Runtime, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	role := fs.String("role", "", "consult role to spawn")
	file := fs.String("file", "", "file containing the question")
	nameFlag := fs.String("name", "", "binding name")
	newTab := fs.Bool("new-tab", false, "open the consult in its own tab")
	workspace := fs.String("workspace", "", "workspace for --new-tab")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *role == "" {
		return fmt.Errorf("relay ask needs --role ROLE")
	}
	if *file == "" {
		return fmt.Errorf("relay ask needs --file PATH")
	}

	name, err := resolveBinding(rt, *nameFlag, fs.Args())
	if err != nil {
		return err
	}

	res, err := relay.Ask(ctx, rt, relay.AskOptions{
		Role:        *role,
		File:        *file,
		Name:        name,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		NewTab:      *newTab,
		WorkspaceID: *workspace,
	})
	if err != nil {
		return err
	}

	fmt.Printf("asked %s consult %s on %s (pane %s)\nfindings will appear at: %s\n",
		res.Consult.Role, res.Consult.ID, res.Binding, res.Consult.Endpoint.PaneID, res.Consult.FindingsPath)
	return nil
}
```

`parseFlags` (`main.go:128`) is the positional-aware parser: it re-parses iteratively so flags are found wherever they appear, which is what makes `relay ask webshop --role reviewer` work. Add an `ask` line to the help text next to `send`.

- [ ] **Step 6b: Fix a stale help line you now own**

`cmd/relay/main.go:47` still reads:

```
  pull      print the newest pending payload to stdout, without typing anywhere
```

Delivery became oldest-first in Task 1, so this is factually wrong. It was left
for this task because Task 1's commit scope excluded `main.go` and you are
adding the `ask` line immediately above it. Change `newest` to `oldest`:

```
  pull      print the oldest pending payload to stdout, without typing anywhere
```

Then confirm nothing else in the tree still claims newest-first:

```bash
grep -rn "newest pending\|newest undelivered" --include='*.go' .
```

The only acceptable remaining hit is inside `confirmIndex`'s rationale comment
in `internal/store/log.go`, which describes the function pair this one
*replaced* and is deliberate history. Report anything else; do not guess at it.

- [ ] **Step 7: Verify and commit**

```bash
make check
go build -o /tmp/relay ./cmd/relay && /tmp/relay help | grep ask
```

```bash
git add internal/relay/ask.go internal/relay/ask_test.go internal/relay/herdr.go cmd/relay/main.go
git commit -m "feat(relay): add relay ask, spawning a one-shot consult

Ask validates everything before it spawns, so a refusal strands nothing. Once
the pane exists every failure path records the consult as silent instead of
returning bare, so relay reap can close a pane relay created rather than
leaving it holding memory with nothing pointing at it.

A consult does not advance the round: Round on the record is a label for
filenames and audit.

Also corrects the pull help line, which still described the newest-first
delivery order Task 1 replaced."
```
