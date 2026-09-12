# Headless builders, step 4: `bind --headless` (+ `add`, `fork`) -- an endpoint, no spawn (#99)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Issue:** #99. **Spec:** `docs/specs/2026-09-12-headless-builders-design.md`
-- read §1, §3.1, §3.6, §5.3, §6 and §7 step 4 before starting; section
numbers below refer to it. Steps 1-3 are on `main` (#105, #106, #107):
`store.Endpoint.Mode/PID/StartedAt/LogPath`, `store.ModeHeadless`,
`Endpoint.Headless()`, `harness.Launch.Print`, `relay.Runner`,
`Runtime.Runner`, `fakeRunner`. This plan uses the first group only.
**Depends on:** nothing open.

**Goal:** `relay bind --headless`, `relay add --headless` and
`relay fork --headless` record a builder endpoint with `Mode: headless`, the
agent name and the harness kind, and open no tab and start no agent. The
conflicting flag combinations are refused in the CLI before any runtime
exists.

**Architecture:** every spawn path -- `create`, `resume`, `Add`, `Fork`,
`switchBuilder` -- goes through `resolveBuilder`, so the branch lives there:
after the candidate is resolved and the agent name validated, a headless
request returns `store.Endpoint{AgentName, Kind: c.Harness, Mode:
ModeHeadless}` and skips `openTab`/`StartAgent`. `BindOptions`, `AddOptions`
and `ForkOptions` gain `Headless bool`; `Add` and `Fork` copy it into the
`BindOptions` they already build. `BindResolved` refuses `Headless` with
`BuilderPane` (adopt) and with `Resume` (mode change) as two sentinel errors,
before it lists agents. `cmd/relay` adds the flag to the three commands,
refuses the two conflicts before `newRuntime`, and prints `headless` where
it printed a pane id.

**Not in this step, deliberately:** `send` on a headless binding still
calls `Herdr.Prompt` and the daemon still treats a builder it cannot find
as gone -- steps 5 and 6. Until step 6 merges, **nobody binds `--headless`
live**; the planner owns that.

**Tech stack:** Go 1.22. Verification is `make check`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-t4` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-t4`, cut from `main`. |
| `~/.local/state/relay/headless-t4` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

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

Do **not** run `herdr` yourself.

## Global constraints

- Files touched: `internal/relay/bind.go`, `add.go`, `fork.go`, their tests,
  `cmd/relay/main.go`, `cmd/relay/main_test.go`. Nothing else. `switch.go`,
  `send.go`, `reconcile.go` are steps 5-6.
- The pane path is byte-for-byte unchanged: every existing test in
  `internal/relay` and `cmd/relay` passes **unmodified**.
- A headless endpoint satisfies spec §3.1's invariants: `PaneID == ""`,
  `SessionID == ""`, `PID == 0`, `LogPath == ""`.
- No test in `cmd/relay` may reach `newRuntime`; every `cmd/relay` test here
  fails in flag validation first. CI runners have no `herdr` binary.
- `go.mod`/`go.sum` do not change.
- One commit per task, on the worktree's branch.

---

### Task 1: `BindOptions.Headless`, the `resolveBuilder` branch, the two refusals

**Files:**
- Modify: `internal/relay/bind.go` (`BindOptions`, `BindResolved`, `resolveBuilder`; two sentinels)
- Modify: `internal/relay/bind_test.go` (tests appended)

**Interfaces:**
- Consumes: `store.ModeHeadless`, `store.Endpoint.Headless()` (step 1); `resolveCandidate`, `builderAgentName`, `Resolution` (existing).
- Produces:

```go
// BindOptions gains:
Headless bool
var ErrHeadlessAdopt  = errors.New("--headless spawns a process; it cannot adopt a pane (drop --builder <pane>)")
var ErrHeadlessResume = errors.New("--headless cannot change an existing binding's mode; unbind and bind again")
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/bind_test.go`:

```go
func TestBindHeadlessRecordsAnEndpointAndSpawnsNothing(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("headless must open no tab and start no agent: tabs=%d starts=%d", len(f.tabs), len(f.starts))
	}
	ep := b.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.AgentName != "webshop-builder" || ep.Kind != "opencode" {
		t.Errorf("AgentName/Kind = %q/%q, want webshop-builder/opencode", ep.AgentName, ep.Kind)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("candidate = %q / %q, want %q", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("round/state = %d/%s, want 1/active", b.Round, b.State)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh headless binding logs its pick and nothing else: %+v (%v)", entries, err)
	}
	// The stored binding reads back headless too.
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

func TestBindHeadlessRefusesAdoptAndResumeBeforeListingAgents(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", BuilderPane: "w2:p4", PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if !errors.Is(err, ErrHeadlessAdopt) {
		t.Errorf("headless + adopt: err = %v, want ErrHeadlessAdopt", err)
	}
	_, err = Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if !errors.Is(err, ErrHeadlessResume) {
		t.Errorf("headless + resume: err = %v, want ErrHeadlessResume", err)
	}
	if f.listCalls != 0 {
		t.Errorf("refusals must happen before herdr is asked anything: listCalls = %d", f.listCalls)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused bind must save nothing: %v", err)
	}
}

func TestBindHeadlessStillRefusesANameHerdrWouldRefuse(t *testing.T) {
	// The agent name is validated even though no herdr agent is started:
	// the name is what status, log and a later pane-mode rebind identify
	// the builder by, and the limit must not depend on the mode.
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33
	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err == nil || !strings.Contains(err.Error(), "builder agent name") {
		t.Fatalf("err = %v, want the agent-name refusal", err)
	}
}
```

Add `"strings"` to the test file's imports if it is not already there.
`errors`, `herdr`, `store` are.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestBindHeadless'`
Expected: compile error -- `unknown field Headless`, `undefined: ErrHeadlessAdopt`.

- [ ] **Step 3: The option and the sentinels**

In `internal/relay/bind.go`, after the `ErrBuilderUnverified` var, add:

```go
// ErrHeadlessAdopt: --headless describes a process relay will run; a pane
// the human already started is the opposite of that (headless spec §3.6).
var ErrHeadlessAdopt = errors.New("--headless spawns a process; it cannot adopt a pane (drop --builder <pane>)")

// ErrHeadlessResume: a binding's mode is fixed at creation. Changing it
// under a live round would leave the old shape's state (pane id, or pid and
// log) meaning nothing (headless spec §1, scope boundary).
var ErrHeadlessResume = errors.New("--headless cannot change an existing binding's mode; unbind and bind again")
```

In `BindOptions`, after `RoundTimeout`:

```go
	// Headless makes the builder a process relay runs per round instead of
	// a pane it watches (#99). Refused with BuilderPane and with Resume.
	Headless bool
```

- [ ] **Step 4: Refuse in `BindResolved`, before herdr is asked**

In `BindResolved`, after the `opts.CWD == ""` check and before
`rt.Herdr.ListAgents`, insert:

```go
	if opts.Headless && opts.BuilderPane != "" {
		return store.Binding{}, Resolution{}, ErrHeadlessAdopt
	}
	if opts.Headless && opts.Resume {
		return store.Binding{}, Resolution{}, ErrHeadlessResume
	}
```

- [ ] **Step 5: The branch in `resolveBuilder`**

In `resolveBuilder`, the spawn path currently reads (after `c := res.Candidate`):

```go
	role, _ := harness.RoleByName("builder")
	h, _ := harness.Lookup(c.Harness) // cannot miss: Load validated it
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)

	agentName, err := builderAgentName(name)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}

	paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, agentName)
```

Replace that block with:

```go
	agentName, err := builderAgentName(name)
	if err != nil {
		return store.Endpoint{}, Resolution{}, err
	}

	// Headless (#99): the builder is a process relay starts on each send,
	// not a pane. Nothing to open, nothing to start, nothing to strand; the
	// endpoint records the mode, the name and the kind, and Send fills in
	// the process fields per round (spec §5.3).
	if opts.Headless {
		return store.Endpoint{AgentName: agentName, Kind: c.Harness, Mode: store.ModeHeadless}, res, nil
	}

	role, _ := harness.RoleByName("builder")
	h, _ := harness.Lookup(c.Harness) // cannot miss: Load validated it
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)

	paneID, err := openTab(ctx, rt, opts.WorkspaceID, opts.CWD, agentName)
```

Moving `builderAgentName` above `Launch` changes nothing for the pane path:
both are pure and neither depends on the other. Update the function's doc
comment, first paragraph, to:

```go
// resolveBuilder adopts an existing builder pane, opens a tab and starts the
// candidate agent in its root pane, or -- with opts.Headless -- records a
// headless endpoint and spawns nothing (#99). Focus stays with the planner
// either way.
```

- [ ] **Step 6: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS -- the three new tests and every existing one, unmodified.
If `TestBindRefusesABuilderNameHerdrWouldRefuse` or any `TestBindSpawns*`
test fails, the pane path moved: stop and report.

- [ ] **Step 7: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/bind.go internal/relay/bind_test.go
git commit -m "feat(relay): bind --headless -- endpoint recorded, nothing spawned; adopt and resume refused (#99 step 4)"
```

---

### Task 2: `Add` and `Fork` carry `Headless`

**Files:**
- Modify: `internal/relay/add.go` (`AddOptions.Headless`; `bindOpts`)
- Modify: `internal/relay/fork.go` (`ForkOptions.Headless`; `bindOpts`)
- Modify: `internal/relay/add_test.go`, `internal/relay/fork_test.go` (tests appended)

**Interfaces:**
- Consumes: `BindOptions.Headless` (Task 1).
- Produces: `AddOptions.Headless bool`, `ForkOptions.Headless bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/relay/add_test.go`:

```go
func TestAddHeadlessCutsTheWorktreeAndSpawnsNothing(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)
	repo := addRepo(t)

	got, err := Add(context.Background(), rt, AddOptions{
		Name: "frontend", Candidate: testAgyRef, PlannerPane: "w2:p3", Repo: repo, Headless: true,
	})
	if err != nil {
		t.Fatalf("Add --headless: %v", err)
	}
	if len(fh.tabs) != 0 || len(fh.starts) != 0 {
		t.Fatalf("headless add must open no tab and start no agent: tabs=%d starts=%d", len(fh.tabs), len(fh.starts))
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("the worktree is still cut: addWorktreeCalls = %+v", fg.addWorktreeCalls)
	}
	ep := got.Binding.Builder
	if !ep.Headless() || ep.AgentName != "frontend-builder" || ep.Kind != "agy" {
		t.Errorf("builder = %+v, want headless frontend-builder/agy", ep)
	}
	if ep.PaneID != "" || ep.PID != 0 || ep.LogPath != "" {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if got.Binding.BuilderCandidate != testAgyRef || got.Binding.Round != 1 {
		t.Errorf("candidate/round = %q/%d", got.Binding.BuilderCandidate, got.Binding.Round)
	}
}
```

Append to `internal/relay/fork_test.go`:

```go
func TestForkHeadlessSpawnsNothing(t *testing.T) {
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)

	res, err := Fork(context.Background(), rt, ForkOptions{
		Source: "source", Round: 2, NewName: "alt", PlannerPane: "w2:p3", Headless: true,
	})
	if err != nil {
		t.Fatalf("Fork --headless: %v", err)
	}
	if len(fh.tabs) != 0 || len(fh.starts) != 0 {
		t.Fatalf("headless fork must open no tab and start no agent: tabs=%d starts=%d", len(fh.tabs), len(fh.starts))
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Fatalf("the worktree is still cut: %+v", fg.addWorktreeCalls)
	}
	ep := res.Binding.Builder
	if !ep.Headless() || ep.AgentName != "alt-builder" || ep.PaneID != "" || ep.PID != 0 {
		t.Errorf("builder = %+v, want a headless alt-builder", ep)
	}
	if res.Binding.Round != 3 || res.Binding.ForkedFrom != "source" {
		t.Errorf("fork bookkeeping: round=%d from=%q", res.Binding.Round, res.Binding.ForkedFrom)
	}
	// The source's pane builder is untouched: a fork inherits the candidate,
	// not the mode.
	src, _ := rt.Store.Load("source")
	if src.Builder.Headless() || src.Builder.PaneID != "w2:p4" {
		t.Errorf("source builder changed: %+v", src.Builder)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/relay -run 'TestAddHeadless|TestForkHeadless'`
Expected: compile error, `unknown field Headless`.

- [ ] **Step 3: Implement**

In `internal/relay/add.go`, in `AddOptions` after `CWD`:

```go
	// Headless makes the peer's builder a process relay runs per round
	// instead of a pane (#99). Passed through to resolveBuilder.
	Headless bool
```

and in the `bindOpts := BindOptions{...}` literal add `Headless: opts.Headless,`.

In `internal/relay/fork.go`, in `ForkOptions` after `CWD`:

```go
	// Headless makes the fork's builder a process relay runs per round
	// instead of a pane (#99). Not inherited from the source: the mode is a
	// property of this binding, chosen at its creation.
	Headless bool
```

and in the `bindOpts := BindOptions{...}` literal add `Headless: opts.Headless,`.

- [ ] **Step 4: Run the package tests**

Run: `go test -count=1 ./internal/relay`
Expected: PASS.

- [ ] **Step 5: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add internal/relay/add.go internal/relay/fork.go internal/relay/add_test.go internal/relay/fork_test.go
git commit -m "feat(relay): add/fork --headless plumbing (#99 step 4)"
```

---

### Task 3: the CLI flag and its refusals

**Files:**
- Modify: `cmd/relay/main.go` (`cmdBind`, `cmdAdd`, `cmdFork`; `builderWhere`)
- Modify: `cmd/relay/main_test.go` (tests appended)

**Interfaces:**
- Consumes: `BindOptions.Headless`, `AddOptions.Headless`, `ForkOptions.Headless`, `ErrHeadlessAdopt`, `ErrHeadlessResume`.
- Produces: `func builderWhere(ep store.Endpoint) string` in `cmd/relay/main.go` -- `"headless"` for a headless endpoint, else the pane id.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/relay/main_test.go`:

```go
// TestBindHeadlessConflictsFailBeforeNewRuntime pins headless spec §6: the
// two flag conflicts are usage errors, refused before relay talks to herdr.
// CI runners have no herdr binary, so reaching newRuntime would be a
// different failure with a different message.
func TestBindHeadlessConflictsFailBeforeNewRuntime(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"bind", "--headless", "--resume", "--name", "x"}, []string{"--headless", "--resume"}},
		{[]string{"bind", "--headless", "--builder", "w2:p4"}, []string{"--headless", "pane"}},
	}
	for _, c := range cases {
		err := run(c.args)
		if err == nil {
			t.Errorf("%v: want an error", c.args)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: error %q does not mention %q", c.args, err, w)
			}
		}
	}
}

func TestBuilderWhere(t *testing.T) {
	if got := builderWhere(store.Endpoint{PaneID: "w2:p4"}); got != "w2:p4" {
		t.Errorf("pane: %q", got)
	}
	if got := builderWhere(store.Endpoint{Mode: store.ModeHeadless, AgentName: "x-builder"}); got != "headless" {
		t.Errorf("headless: %q", got)
	}
}
```

`store` is imported by `main_test.go` already; if not, add
`"github.com/fuad-daoud/relay/internal/store"`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/relay -run 'TestBindHeadlessConflicts|TestBuilderWhere'`
Expected: `TestBuilderWhere` fails to compile (`undefined: builderWhere`);
fix that first by adding the helper (Step 3), then
`TestBindHeadlessConflicts...` fails at runtime with either `flag provided
but not defined: -headless` or a `newRuntime` error -- either is the
"before" state.

- [ ] **Step 3: `builderWhere`**

In `cmd/relay/main.go`, next to `isPaneID`:

```go
// builderWhere is how the bound/added lines name the builder's place: its
// pane id, or "headless" for a process relay runs itself (#99).
func builderWhere(ep store.Endpoint) string {
	if ep.Headless() {
		return "headless"
	}
	return ep.PaneID
}
```

- [ ] **Step 4: `cmdBind`**

Add the flag after `timeout`:

```go
	headless := fs.Bool("headless", false,
		"run the builder as a process per round instead of a pane; not with --resume or a pane id in --builder")
```

After the `*rebind && !*resume` check and before `newRuntime()`, add:

```go
	if *headless && *resume {
		return fmt.Errorf("relay bind --headless cannot be combined with --resume: a binding's mode is fixed at creation (unbind and bind again)")
	}
	if *headless && isPaneID(*builderAlias) {
		return fmt.Errorf("relay bind --headless spawns a process; it cannot adopt pane %s (drop --builder or name a candidate)", *builderAlias)
	}
```

In the `opts := relay.BindOptions{...}` literal add `Headless: *headless,`.

Change the "bound" line from

```go
	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, b.Builder.PaneID, b.BuilderCandidate, b.Round)
```

to

```go
	fmt.Printf("bound %s: planner %s -> builder %s (%s), round %d\n",
		b.Name, b.Planner.PaneID, builderWhere(b.Builder), b.BuilderCandidate, b.Round)
```

The pane output is unchanged by this: `builderWhere` returns the pane id.

- [ ] **Step 5: `cmdAdd` and `cmdFork`**

In `cmdAdd`, add after `cwd`:

```go
	headless := fs.Bool("headless", false, "run the builder as a process per round instead of a pane")
```

add `Headless: *headless,` to the `relay.AddOptions{...}` literal, and
change the "added" line from

```go
	fmt.Printf("added %s: builder %s in pane %s\n",
		res.Binding.Name, res.Binding.BuilderCandidate, res.Binding.Builder.PaneID)
```

to

```go
	if res.Binding.Builder.Headless() {
		fmt.Printf("added %s: builder %s (headless)\n", res.Binding.Name, res.Binding.BuilderCandidate)
	} else {
		fmt.Printf("added %s: builder %s in pane %s\n",
			res.Binding.Name, res.Binding.BuilderCandidate, res.Binding.Builder.PaneID)
	}
```

In `cmdFork`, add after `cwd`:

```go
	headless := fs.Bool("headless", false, "run the fork's builder as a process per round instead of a pane")
```

and `Headless: *headless,` to the `relay.ForkOptions{...}` literal. The
fork's printed lines name no pane and do not change.

- [ ] **Step 6: Run the package tests**

Run: `go test -count=1 ./cmd/relay`
Expected: PASS -- the two new tests and every existing one. Both conflict
cases must fail with the `relay bind --headless ...` messages from Step 4,
not with anything mentioning herdr or a runtime.

- [ ] **Step 7: Verify and commit**

Run: `make check`
Expected: green.

```bash
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "feat(cli): --headless on bind/add/fork; conflicts refused before the runtime exists (#99 step 4)"
```

---

## Report

Say which tasks landed, the `make check` result (or its constituents if
`make` was intercepted), and `git diff --stat main..HEAD`. Confirm no test
outside the ones this plan adds was edited. If any step was impossible as
written, say which and why -- do not work around it.
