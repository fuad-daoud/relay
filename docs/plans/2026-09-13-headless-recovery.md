# Headless recovery: builders outlive the daemon's cap, rebind keeps the mode (#119, #120)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-13-headless-recovery-design.md`. Section numbers
below (§) refer to it.
**Issues:** #120 (Tasks 1-2), #119 (Task 3). Closes both.
**Depends on:** nothing open.

**Goal:** A headless builder no longer runs under `relay.service`'s 128 MB
cap, an OOM-killed builder no longer takes the daemon with it, and
`relay bind --resume --rebind` on a headless binding produces a headless
builder instead of a pane.

**Architecture:** Two settings in the unit template (`MemoryMax` gone,
`OOMPolicy=continue` added) and one line in the supervisor `sh` script
(`oom_score_adj` 500 before the builder runs) cover #120; no Go logic changes
there. For #119, `resume` in `bind.go` grows a headless branch that refuses a
pane, checks liveness through `rt.Runner` instead of herdr's agent list, and
sets `opts.Headless` from the stored binding before `resolveBuilder`. The pane
branch is untouched.

**Tech stack:** Go 1.22, POSIX `sh`. Verification is `make check` (runs
`-race`, `gofmt -l`, `go vet`, `go mod tidy` check, and every
`scripts/*_test.sh`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/headless-recovery` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/headless-recovery`, cut from `main`. |
| `~/.local/state/relay/headless-recovery` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

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
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
for t in scripts/*_test.sh; do echo "==> $t"; sh "$t"; done
```

Do **not** run `herdr`, `systemctl`, or reinstall the service. The
real-machine verification (§7 step 4) is the planner's, after merge. No test in
`cmd/relay` is added by this plan: no subcommand changes.

## Global constraints

- `dist/relay.service` ends with **no** `MemoryMax=` line and **one**
  `OOMPolicy=continue` line (§4.1). The macOS plist is not touched.
- `supervisorScript` stays plain `sh`: no bash-isms, no `exec`. The builder
  remains a child of the `sh` so the trailer is still written after an
  OOM kill of the builder (§4.2).
- In `resume`, the pane path (`ListAgents`, `FindAgent`, `DiagnoseBuilder`,
  `resolveBuilder` with the CLI's `opts`) is **not modified**. Only a new
  branch for `b.Builder.Headless()` is added ahead of it (§4.3).
- No new error variables. The headless branch reuses `ErrHeadlessAdopt`,
  `ErrRunnerUnavailable`, `ErrBuilderAlive` (§6).
- `resolveBuilder`, `startRound`, `send.go`, `switch.go`, `headless.go` are
  not modified.
- Tests in `internal/relay` use the existing `fakeHerdr` / `fakeRunner` /
  `newRuntime` fixtures in `bind_test.go` and `fake_test.go`. No new fakes.
- One commit per task, on the worktree's branch.

---

### Task 1: Unit template -- no cap, `OOMPolicy=continue`

**Files:**
- Modify: `dist/relay.service:12-15`
- Create: `scripts/relay-service-template_test.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: a `[Service]` section with `OOMPolicy=continue` and no
  `MemoryMax`; a script test `make check` picks up via its `scripts/*_test.sh`
  glob.

- [ ] **Step 1: Write the template test**

Create `scripts/relay-service-template_test.sh`, mode `0755`:

```sh
#!/bin/sh
set -eu

# Asserts the checked-in systemd unit template's [Service] keys (spec
# 2026-09-13-headless-recovery §4.1). Headless builders are children of the
# daemon and share its cgroup, so the unit must not cap memory, and an
# OOM-killed builder must not take the daemon down with it.
#
# Usage: relay-service-template_test.sh [path-to-template]
# Defaults to the repo's dist/relay.service.

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template=${1:-"$here/../dist/relay.service"}

fail=0
if [ ! -f "$template" ]; then
	echo "FAIL: no template at $template"; exit 1
fi
if grep -q '^MemoryMax=' "$template"; then
	echo "FAIL: $template caps memory (MemoryMax); headless builders share the unit's cgroup"; fail=1
fi
if [ "$(grep -c '^OOMPolicy=continue$' "$template")" -ne 1 ]; then
	echo "FAIL: $template must set OOMPolicy=continue exactly once"; fail=1
fi
if grep -q '^OOMPolicy=' "$template" && ! grep -q '^OOMPolicy=continue$' "$template"; then
	echo "FAIL: $template sets an OOMPolicy other than continue"; fail=1
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "ok: $template has no MemoryMax and OOMPolicy=continue"
```

- [ ] **Step 2: Run it to verify it fails on today's template**

Run: `sh scripts/relay-service-template_test.sh`
Expected: exit 1, `FAIL: ... caps memory (MemoryMax)` and
`FAIL: ... must set OOMPolicy=continue exactly once`.

- [ ] **Step 3: Edit the template**

In `dist/relay.service`, replace the three lines

```
# The daemon holds no long-lived state of its own; it rebuilds from
# ~/.local/state/relay and a live herdr query on every start.
MemoryMax=128M
```

with

```
# No MemoryMax: headless builders (relay add --headless) are children of
# the daemon and share this unit's cgroup. A cap sized for the daemon
# (once 128M) OOM-killed a single `claude -p`, and the default
# OOMPolicy=stop then took the daemon down with it (#120).
# An OOM-killed builder is an exited builder: the daemon sees it on the
# next tick and switches or halts as policy says. The daemon itself
# holds no long-lived state; it rebuilds from ~/.local/state/relay and a
# live herdr query on every start.
OOMPolicy=continue
```

The resulting `[Service]` section, in full:

```
[Service]
Type=simple
# The daemon execs `herdr` by PATH lookup, and a user unit inherits no
# login shell PATH, so it has to be named here.
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/.local/bin/relay daemon --interval 2s
Restart=on-failure
RestartSec=5
# No MemoryMax: headless builders (relay add --headless) are children of
# the daemon and share this unit's cgroup. A cap sized for the daemon
# (once 128M) OOM-killed a single `claude -p`, and the default
# OOMPolicy=stop then took the daemon down with it (#120).
# An OOM-killed builder is an exited builder: the daemon sees it on the
# next tick and switches or halts as policy says. The daemon itself
# holds no long-lived state; it rebuilds from ~/.local/state/relay and a
# live herdr query on every start.
OOMPolicy=continue
```

- [ ] **Step 4: Run the test against both revisions**

Run:
```bash
sh scripts/relay-service-template_test.sh
git show HEAD:dist/relay.service > /tmp/relay-old.service
sh scripts/relay-service-template_test.sh /tmp/relay-old.service; echo "old template exit: $?"
```
Expected: first command prints `ok: ...` and exits 0; the old template
prints both `FAIL` lines and `old template exit: 1`.

- [ ] **Step 5: Confirm `make check` runs it and the installer test still passes**

Run: `for t in scripts/*_test.sh; do echo "==> $t"; sh "$t"; done`
Expected: every script prints its `==>` line and none prints `FAIL`.
`scripts/plugin-install-service_test.sh` stubs its own template and is not
affected.

- [ ] **Step 6: Commit**

```bash
chmod 0755 scripts/relay-service-template_test.sh
git add dist/relay.service scripts/relay-service-template_test.sh
git commit -m "fix(service): drop MemoryMax, set OOMPolicy=continue so builders and daemon survive an OOM (#120)"
```

---

### Task 2: Supervisor raises its `oom_score_adj`

**Files:**
- Modify: `internal/proc/proc.go:32-35` (`supervisorScript`)
- Test: `internal/proc/proc_test.go` (new test after `TestStartRunsInDirWithExtraEnv`, ~line 95)

**Interfaces:**
- Consumes: `func start(t *testing.T, r *Runner, argv ...string) (relay.ProcHandle, string)` and
  `func waitGone(t *testing.T, r *Runner, h relay.ProcHandle, within time.Duration)`,
  both existing test helpers in `proc_test.go`; `ExitTrailer` (existing const).
- Produces: `const supervisorScript` whose first statement writes `500` to
  `/proc/self/oom_score_adj`.

- [ ] **Step 1: Write the failing test**

In `internal/proc/proc_test.go`, after `TestStartRunsInDirWithExtraEnv`, add:

```go
// The supervisor raises its own oom_score_adj before running the builder,
// and the builder inherits it, so under memory pressure the kernel takes a
// builder before `relay daemon` (spec 2026-09-13-headless-recovery §4.2).
func TestStartedProcessInheritsRaisedOOMScore(t *testing.T) {
	if _, err := os.Stat("/proc/self/oom_score_adj"); err != nil {
		t.Skip("no /proc/self/oom_score_adj on this platform")
	}
	r := New()
	h, log := start(t, r, "cat", "/proc/self/oom_score_adj")
	waitGone(t, r, h, 5*time.Second)

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got, want := string(data), "500\n"+ExitTrailer+"0\n"; got != want {
		t.Errorf("log = %q, want %q (builder must see oom_score_adj 500)", got, want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 ./internal/proc -run TestStartedProcessInheritsRaisedOOMScore -v`
Expected: FAIL, `log = "0\nrelay-exit:0\n", want "500\nrelay-exit:0\n"`.

- [ ] **Step 3: Add the line to the supervisor**

In `internal/proc/proc.go`, replace

```go
// supervisorScript runs the builder with stdin closed and, whatever happens
// to it, appends the trailer. Plain sh: no bash-isms. "$@" is the argv the
// runner passes after the script name.
const supervisorScript = `"$@" </dev/null; echo "` + ExitTrailer + `$?"`
```

with

```go
// supervisorScript runs the builder with stdin closed and, whatever happens
// to it, appends the trailer. Plain sh: no bash-isms. "$@" is the argv the
// runner passes after the script name.
//
// Before the builder starts, the supervisor raises its own oom_score_adj;
// the builder inherits it. Under memory pressure the kernel then prefers a
// builder over `relay daemon` (#120). The write fails silently where there
// is no /proc (macOS) or it is refused, and the builder runs as before.
// The builder stays a child of this sh (no exec) so an OOM kill of the
// builder still leaves a trailer.
const supervisorScript = `echo 500 > /proc/self/oom_score_adj 2>/dev/null || true; "$@" </dev/null; echo "` + ExitTrailer + `$?"`
```

- [ ] **Step 4: Run the package tests**

Run: `go test -race -count=1 ./internal/proc`
Expected: PASS. `TestStartCapturesBothStreamsAndTheExitTrailer` and
`TestStartRunsInDirWithExtraEnv` still pass: the new statement writes
nothing to the log.

- [ ] **Step 5: Mutation check**

Temporarily delete `echo 500 > /proc/self/oom_score_adj 2>/dev/null || true; `
from the constant and run
`go test -count=1 ./internal/proc -run TestStartedProcessInheritsRaisedOOMScore`.
Expected: FAIL with `log = "0\n...`. Restore the line, re-run, PASS. Note the
result in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/proc/proc.go internal/proc/proc_test.go
git commit -m "fix(proc): supervisor raises oom_score_adj so the kernel takes a builder before the daemon (#120)"
```

---

### Task 3: `resume` keeps a headless binding headless

**Files:**
- Modify: `internal/relay/bind.go:145-190` (`resume`, the `if rebinding {` block)
- Test: `internal/relay/bind_test.go` (three new tests after
  `TestBindHeadlessRefusesAdoptAndResumeBeforeListingAgents`, ~line 1538)

**Interfaces:**
- Consumes: `store.Endpoint.Headless() bool`; `handleOf(e store.Endpoint) ProcHandle`
  (`headless.go:28`); `Runner.Alive(ctx, ProcHandle) (bool, error)`;
  `ErrHeadlessAdopt`, `ErrRunnerUnavailable`, `ErrBuilderAlive` (existing);
  `resolveBuilder(ctx, rt, nil, opts, name, plannerPane)` (existing, unchanged).
- Produces: `resume` returns a `ModeHeadless` endpoint for a headless binding
  on `Resume+Rebind`, and refuses per §4.3.

- [ ] **Step 1: Write the three failing tests**

In `internal/relay/bind_test.go`, after
`TestBindHeadlessRefusesAdoptAndResumeBeforeListingAgents`, add:

```go
// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
func TestBindResumeRebindKeepsAHeadlessBindingHeadless(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, false) // the old process is gone
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("a headless rebind must open no tab and start no agent: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	ep := got.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("rebound endpoint must be a fresh headless endpoint with nothing running: %+v", ep)
	}
	if got.BuilderCandidate != testAgyRef || res.How != HowOrder {
		t.Errorf("candidate = %q (%+v), want the order's first, %s", got.BuilderCandidate, res, testAgyRef)
	}
	if got.State != store.StateActive || got.Round != 4 {
		t.Errorf("state/round = %s/%d, want active/4", got.State, got.Round)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// herdr's agent list cannot see a process, so a headless binding's liveness
// is the Runner's answer. A live process refuses the rebind (§4.3).
func TestBindResumeRebindRefusesALiveHeadlessProcess(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, true) // still running
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("a refused rebind must spawn nothing: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || stored.Builder.PID != 4321 || !stored.Builder.Headless() {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

// A pane cannot replace a process builder; the mode is fixed at creation.
func TestBindResumeRefusesAPaneForAHeadlessBinding(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{
		plannerAgent(),
		{Kind: "agy", Status: herdr.StatusIdle, PaneID: "w2:p9", CWD: "/repo", Session: herdr.Session{Value: "stray-sess"}},
	}}
	rt := newRuntime(t, f)
	rt.Runner = newFakeRunner()
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, BuilderPane: "w2:p9", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrHeadlessAdopt) {
		t.Fatalf("err = %v, want ErrHeadlessAdopt", err)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() || stored.Builder.PaneID != "" {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}
```

If `herdr.StatusIdle` does not exist under that name, use whichever
`herdr.Status*` constant the other tests in this file use for an idle agent;
the status is irrelevant to the assertion. If `orderOf` or `HowOrder` are not
the names used at `bind_test.go:1406-1420`, stop and report -- do not guess.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/relay -run 'TestBindResume(RebindKeepsAHeadlessBindingHeadless|RebindRefusesALiveHeadlessProcess|RefusesAPaneForAHeadlessBinding)' -v`
Expected: all three FAIL.
- `KeepsAHeadlessBindingHeadless`: `a headless rebind must open no tab ...` (a tab was created).
- `RefusesALiveHeadlessProcess`: `err = <nil>, want ErrBuilderAlive` (the herdr list never sees the process).
- `RefusesAPaneForAHeadlessBinding`: `err = <nil>, want ErrHeadlessAdopt` (the pane is adopted).

- [ ] **Step 3: Add the headless branch to `resume`**

In `internal/relay/bind.go`, inside `resume`, the `if rebinding {` block
currently reads (from the `b.State == store.StateDone` check to the
`resolveBuilder` call):

```go
		if b.State == store.StateDone {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return store.Binding{}, Resolution{}, fmt.Errorf("list agents: %w", err)
		}
		if _, alive := FindAgent(agents, b.Builder); alive {
			return store.Binding{}, Resolution{}, ErrBuilderAlive
		}
		// ... (existing comments)
		d := DiagnoseBuilder(b)
		if !d.Identified && d.RoundOpen && !opts.AssumeDead {
			return store.Binding{}, Resolution{}, fmt.Errorf(
				"%w: relay cannot tell a dead builder for %q from a moved pane. "+
					"Check %s is really gone, then re-run with --assume-dead",
				ErrBuilderUnverified, opts.Name, b.Builder.PaneID)
		}
		builder, res, err = resolveBuilder(ctx, rt, nil, opts, opts.Name, planner.PaneID)
```

Restructure it so the `StateDone` check stays first, then the two modes
branch, then the shared `resolveBuilder` call. Leave every existing comment
in the pane branch where it is. The result:

```go
		if b.State == store.StateDone {
			return store.Binding{}, Resolution{}, fmt.Errorf("binding %q is done: `relay bind` to start fresh", opts.Name)
		}
		if b.Builder.Headless() {
			// A binding's mode is fixed at creation (#119). The old builder
			// is a process, so herdr's agent list says nothing about it:
			// ask the Runner. A PID has no "moved pane" ambiguity, so the
			// DiagnoseBuilder guard below does not apply.
			if opts.BuilderPane != "" {
				return store.Binding{}, Resolution{}, ErrHeadlessAdopt
			}
			if rt.Runner == nil {
				return store.Binding{}, Resolution{}, ErrRunnerUnavailable
			}
			if b.Builder.PID != 0 {
				alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
				if err != nil {
					return store.Binding{}, Resolution{}, fmt.Errorf("check builder process: %w", err)
				}
				if alive {
					return store.Binding{}, Resolution{}, ErrBuilderAlive
				}
			}
			opts.Headless = true
		} else {
			agents, err := rt.Herdr.ListAgents(ctx)
			if err != nil {
				return store.Binding{}, Resolution{}, fmt.Errorf("list agents: %w", err)
			}
			if _, alive := FindAgent(agents, b.Builder); alive {
				return store.Binding{}, Resolution{}, ErrBuilderAlive
			}
			// ... (the existing comments, unchanged)
			d := DiagnoseBuilder(b)
			if !d.Identified && d.RoundOpen && !opts.AssumeDead {
				return store.Binding{}, Resolution{}, fmt.Errorf(
					"%w: relay cannot tell a dead builder for %q from a moved pane. "+
						"Check %s is really gone, then re-run with --assume-dead",
					ErrBuilderUnverified, opts.Name, b.Builder.PaneID)
			}
		}
		builder, res, err = resolveBuilder(ctx, rt, nil, opts, opts.Name, planner.PaneID)
```

`opts` is a value parameter of `resume`, so `opts.Headless = true` is local
to this call and does not leak to the caller. Then update the doc comment on
`resume`: under **Preconditions**, after the sentence ending "even if another
agent now occupies its former pane.", add:

```
//	A headless binding's builder is a process; it is alive when
//	the Runner says so, and a pane can never replace it
//	(ErrHeadlessAdopt).
```

and under **Postconditions**, after "Round, CWD, Name, RoundBaselineTree and the round log are untouched.", add:

```
//	The binding's mode is untouched too: a headless binding
//	rebinds to a headless endpoint, a pane binding to a pane.
```

Add `ErrHeadlessAdopt; ErrRunnerUnavailable` to the `// Errors:` line.

- [ ] **Step 4: Run the three tests, then the package**

Run: `go test -count=1 ./internal/relay -run 'TestBindResume(RebindKeepsAHeadlessBindingHeadless|RebindRefusesALiveHeadlessProcess|RefusesAPaneForAHeadlessBinding)' -v`
Expected: PASS, all three.

Run: `go test -race -count=1 ./internal/relay`
Expected: PASS. In particular `TestBindHeadlessRefusesAdoptAndResumeBeforeListingAgents`
(the up-front `--headless --resume` refusal, untouched) and every existing
pane rebind test still pass.

- [ ] **Step 5: Mutation checks**

Two, each restored before the next:

1. Delete `opts.Headless = true`. Run the three tests. Expected:
   `KeepsAHeadlessBindingHeadless` FAILS (`a headless rebind must open no tab`);
   the other two still pass. Restore.
2. Replace the `if b.Builder.PID != 0 { ... }` block with nothing (skip the
   liveness check). Expected: `RefusesALiveHeadlessProcess` FAILS
   (`err = <nil>, want ErrBuilderAlive`); the other two still pass. Restore.

Note both results in your report.

- [ ] **Step 6: Full check and commit**

Run: `make check` (or its constituents; see "Running commands").
Expected: green, including `gofmt -l .` empty.

```bash
git add internal/relay/bind.go internal/relay/bind_test.go
git commit -m "fix(bind): --resume --rebind keeps a headless binding headless and asks the Runner if it is alive (#119)"
```

---

### Task 4: Spec status line

**Files:**
- Modify: `docs/specs/2026-09-13-headless-recovery-design.md:14`

- [ ] **Step 1: Confirm the status line**

The spec header already reads
`**Status:** implemented by \`docs/plans/2026-09-13-headless-recovery.md\`.`
Nothing to change; if it does not, add that line after `**Amends:**`. No
commit unless you changed it.

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD` (expected files: `dist/relay.service`,
`scripts/relay-service-template_test.sh`, `internal/proc/proc.go`,
`internal/proc/proc_test.go`, `internal/relay/bind.go`,
`internal/relay/bind_test.go`, and nothing else), the `make check` output
tail, the three mutation results (Task 2 step 5, Task 3 step 5), and anything
you stopped on. Then create the empty `NNN-done` file the round prompt names,
and reply with only the report path.
