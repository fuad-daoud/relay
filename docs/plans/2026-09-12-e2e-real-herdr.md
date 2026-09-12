# End-to-end test on real herdr with a scripted builder (#114 step 2)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-12-e2e-real-herdr-design.md`. Section numbers
below (§) refer to it.
**Issue:** #114, recommended step 2. Does not close #114.
**Depends on:** #117 (merged).

**Goal:** `make e2e` runs one relay round -- `Bind`, `Send`, `Reconcile`,
delivery -- against a private, detached herdr session with two shell-script
agents, and pins the nudge → quiescence → `unmarked`/scrape fallback on a real
pane. `make check` and CI are untouched.

**Architecture:** One tag-gated file, `internal/relay/e2e_test.go`
(`//go:build e2e`), in package `relay`. An `e2eSession` fixture launches
`herdr --session relay-e2e-<pid>` detached with `SHELL=/bin/sh` and the shim
directory first on PATH, polls its socket, and points the real
`herdr.Client` at it through `t.Setenv("HERDR_SOCKET_PATH", …)`. The runtime
is real everywhere except `Now` (a `fakeClock`) and the store root (a temp
dir). `TestE2E` has six subtests sharing one session and one planner shim;
each binds its own builder.

**Tech stack:** Go 1.22, herdr 0.9.x on the machine running `make e2e`.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/e2e-herdr` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/e2e-herdr`, cut from `main`. |
| `~/.local/state/relay/e2e-herdr` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Two verifications, both required at the end of every task:

```bash
make check                         # the default build must stay green (the e2e file is invisible to it)
go vet -tags e2e ./internal/relay  # the tagged file must vet clean
make e2e                           # from Task 1 on; before Task 5 run its expansion directly:
go test -tags e2e -count=1 -run TestE2E ./internal/relay -v
```

If `make` is intercepted on this machine, run the constituents directly and
say so in your report.

**herdr use is allowed in exactly one way:** through `make e2e` / the `go
test -tags e2e` command above, which creates and destroys its own session
`relay-e2e-<pid>`. Do not run any other `herdr` command, do not touch the
default session, and do not leave a `relay-e2e-*` session behind: if a test
run dies mid-way, run `herdr session list`, and for any `relay-e2e-*` row run
`HERDR_SOCKET_PATH=<its socket> herdr server stop` then `herdr session delete
<name>`, and say so in your report.

## Global constraints

- The e2e file compiles **only** with `-tags e2e`. First line: `//go:build e2e`.
- It never reads or writes `~/.config/relay`, `~/.local/state/relay`, or the
  default herdr session. Only `~/.config/herdr/sessions/relay-e2e-<pid>` and
  temp dirs.
- Bootstrap failures **skip** (`t.Skip`); everything after bootstrap fails.
- Every real-time wait is bounded: 10s for the session to answer, 5s for a
  screen to contain a string. No unbounded polling, no `time.Sleep` longer
  than the 200ms poll interval.
- No production code changes. If a case cannot be made to pass without
  changing `internal/relay` non-test code, stop and report.
- One commit per task.

---

### Task 1: Shim, session fixture, smoke subtest

**Files:**
- Create: `internal/relay/testdata/e2e-shim.sh`
- Create: `internal/relay/e2e_test.go`

**Interfaces:**
- Consumes: `herdr.NewClient(bin string, timeout time.Duration) *herdr.Client`; its `ListAgents`, `CreateTab`, `StartAgent`, `ReadAgentSource`, `Prompt`; `scrapeLines` (const 200, `reconcile.go`).
- Produces (all in `e2e_test.go`, package `relay`):
  - `type e2eSession struct { name, dir, socket, shimDir string; herdr *herdr.Client }`
  - `func startSession(t *testing.T) *e2eSession`
  - `func (s *e2eSession) startShim(t *testing.T, name, kind, cwd string) string` → pane id
  - `func (s *e2eSession) screen(t *testing.T, target string) string`
  - `func (s *e2eSession) waitScreen(t *testing.T, target, substr string)`
  - `func TestE2E(t *testing.T)` with the first subtest `smoke`.

- [ ] **Step 1: The shim**

Create `internal/relay/testdata/e2e-shim.sh` (mode 0755 in git: `chmod +x` before adding):

```sh
#!/bin/sh
# relay e2e agent shim (spec 2026-09-12-e2e-real-herdr §3.2). herdr's
# detection manifest reports a known-kind process as idle by default, so this
# only has to exist, echo what it is told, and never write a file. Argv --
# --agent, --dangerously-skip-permissions, anything -- is ignored.
echo "shim ready"
while IFS= read -r line; do
  echo "you said: $line"
  echo "I implemented the guard clause but could not write the file."
done
```

- [ ] **Step 2: The fixture and the smoke subtest**

Create `internal/relay/e2e_test.go`:

```go
//go:build e2e

package relay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
)

// e2eSession is a private, detached herdr session the test owns (spec §3.1).
// The user's default session is never touched.
type e2eSession struct {
	name    string
	dir     string
	socket  string
	shimDir string
	herdr   *herdr.Client
}

const (
	e2eBootTimeout   = 10 * time.Second
	e2eScreenTimeout = 5 * time.Second
	e2ePoll          = 200 * time.Millisecond
)

// startSession launches `herdr --session relay-e2e-<pid>` with no tty. The
// client half exits at once ("Not a tty"); the server it spawned lives on and
// answers on its own socket. Bootstrap problems skip, never fail (spec §4.1).
func startSession(t *testing.T) *e2eSession {
	t.Helper()
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not installed")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir: " + err.Error())
	}

	s := &e2eSession{name: fmt.Sprintf("relay-e2e-%d", os.Getpid())}
	s.dir = filepath.Join(home, ".config", "herdr", "sessions", s.name)
	s.socket = filepath.Join(s.dir, "herdr.sock")
	s.shimDir = filepath.Join(t.TempDir(), "shim")
	s.herdr = herdr.NewClient("herdr", 30*time.Second)

	shim, err := os.ReadFile(filepath.Join("testdata", "e2e-shim.sh"))
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	if err := os.MkdirAll(s.shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agy", "claude"} {
		if err := os.WriteFile(filepath.Join(s.shimDir, kind), shim, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// SHELL=/bin/sh is load-bearing: a login bash re-sources /etc/profile and
	// drops the shim dir from PATH, so `agent start --kind agy` would launch
	// the real Antigravity CLI (spec §4.1).
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") || strings.HasPrefix(kv, "SHELL=") || strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "SHELL=/bin/sh", "PATH="+s.shimDir+":"+os.Getenv("PATH"))

	launchLog, err := os.Create(filepath.Join(t.TempDir(), "herdr-launch.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer launchLog.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	cmd := exec.Command("herdr", "--session", s.name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, launchLog, launchLog
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Skip("could not launch herdr: " + err.Error())
	}
	go cmd.Wait() // the client exits on "Not a tty"; reap it, ignore the result

	t.Setenv("HERDR_SOCKET_PATH", s.socket)
	t.Cleanup(func() { s.stop(t) })

	deadline := time.Now().Add(e2eBootTimeout)
	var last error
	for time.Now().Before(deadline) {
		if _, last = s.herdr.ListAgents(context.Background()); last == nil {
			return s
		}
		time.Sleep(e2ePoll)
	}
	t.Skipf("herdr session %s did not answer within %s: %v", s.name, e2eBootTimeout, last)
	return nil
}

// stop tears the session down unless RELAY_E2E_KEEP=1. On a failed test it
// prints the server log tail first (spec §6).
func (s *e2eSession) stop(t *testing.T) {
	if os.Getenv("RELAY_E2E_KEEP") == "1" {
		t.Logf("RELAY_E2E_KEEP=1: session kept; HERDR_SOCKET_PATH=%s", s.socket)
		return
	}
	if t.Failed() {
		if log, err := os.ReadFile(filepath.Join(s.dir, "herdr-server.log")); err == nil {
			lines := strings.Split(strings.TrimRight(string(log), "\n"), "\n")
			if len(lines) > 40 {
				lines = lines[len(lines)-40:]
			}
			t.Logf("herdr-server.log tail:\n%s", strings.Join(lines, "\n"))
		}
	}
	run := func(args ...string) {
		c := exec.Command("herdr", args...)
		c.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+s.socket)
		if out, err := c.CombinedOutput(); err != nil {
			t.Logf("herdr %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("server", "stop")
	time.Sleep(500 * time.Millisecond)
	run("session", "delete", s.name)
}

// startShim opens a tab and starts the shim as agent `name` of `kind` in it
// (spec §4.2). Returns the pane id.
func (s *e2eSession) startShim(t *testing.T, name, kind, cwd string) string {
	t.Helper()
	ctx := context.Background()
	pane, err := s.herdr.CreateTab(ctx, "", cwd, name)
	if err != nil {
		t.Fatalf("tab create: %v", err)
	}
	if err := s.herdr.StartAgent(ctx, name, kind, pane, nil); err != nil {
		t.Fatalf("agent start %s --kind %s --pane %s: %v", name, kind, pane, err)
	}
	s.waitScreen(t, pane, "shim ready")
	return pane
}

// screen is exactly what scrapeReport and screenFingerprint read.
func (s *e2eSession) screen(t *testing.T, target string) string {
	t.Helper()
	text, err := s.herdr.ReadAgentSource(context.Background(), target, "recent-unwrapped", scrapeLines)
	if err != nil {
		t.Fatalf("agent read %s: %v", target, err)
	}
	return text
}

// waitScreen polls until the target's screen contains substr, bounded at
// e2eScreenTimeout, failing with the last screen it saw.
func (s *e2eSession) waitScreen(t *testing.T, target, substr string) {
	t.Helper()
	deadline := time.Now().Add(e2eScreenTimeout)
	var last string
	for time.Now().Before(deadline) {
		last = s.screen(t, target)
		if strings.Contains(last, substr) {
			return
		}
		time.Sleep(e2ePoll)
	}
	t.Fatalf("screen of %s never contained %q within %s; last screen:\n%s", target, substr, e2eScreenTimeout, last)
}

// e2eAgents is the current `herdr agent list`, fatal on error.
func (s *e2eSession) agents(t *testing.T) []herdr.Agent {
	t.Helper()
	agents, err := s.herdr.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("agent list: %v", err)
	}
	return agents
}

func TestE2E(t *testing.T) {
	s := startSession(t)
	plannerCWD := t.TempDir()
	planner := s.startShim(t, "planner", "claude", plannerCWD)

	t.Run("smoke", func(t *testing.T) {
		var found *herdr.Agent
		for _, a := range s.agents(t) {
			if a.PaneID == planner {
				a := a
				found = &a
			}
		}
		if found == nil {
			t.Fatalf("planner pane %s not in agent list", planner)
		}
		if found.Kind != "claude" || found.Status != herdr.StatusIdle {
			t.Errorf("planner = kind %q status %q, want claude/idle (the shim is accepted as a known agent)", found.Kind, found.Status)
		}
		if found.Focused {
			t.Errorf("planner must be unfocused so delivery injects instead of holding")
		}
	})
}
```

If `herdr.Agent` does not have exactly the fields `PaneID`, `Kind`, `Status`,
`Focused` (check `internal/herdr/types.go`), use the names it has for the
same four facts; do not add fields.

- [ ] **Step 3: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: `--- PASS: TestE2E/smoke`. Then `herdr session list` must show **no** `relay-e2e-*` row (cleanup ran). If a row remains, stop and report; do not proceed with a leaked session.

Run: `make check`
Expected: green and unchanged -- the tagged file is not compiled.

- [ ] **Step 4: Mutation check (report the result, then revert)**

Temporarily delete the `"SHELL=/bin/sh", ` element from the `env = append(...)` line and run the e2e command again.
Expected: `smoke` fails or `startShim` times out (the real agent or a login shell without the shim). Revert the line. Note the observed failure in your report.

- [ ] **Step 5: Commit**

```bash
chmod +x internal/relay/testdata/e2e-shim.sh
git add internal/relay/testdata/e2e-shim.sh internal/relay/e2e_test.go
git commit -m "test(e2e): detached herdr session fixture with scripted agents; smoke (#114 step 2)"
```

---

### Task 2: Runtime, `bindBuilder`, `reconcileAt`, and the marker round

**Files:**
- Modify: `internal/relay/e2e_test.go`

**Interfaces:**
- Consumes: `Bind`, `Send`, `Reconcile`, `store.New`, `candidate.Load`, `git.NewClient`, `fakeClock` (`fake_test.go`), `touch` (`reconcile_test.go`), `baseTime` (`bind_test.go`).
- Produces:
  - `func e2eRuntime(t *testing.T, clock *fakeClock) Runtime`
  - `func (s *e2eSession) bindBuilder(t *testing.T, rt Runtime, name, plannerPane string) store.Binding`
  - `func reconcileAt(t *testing.T, s *e2eSession, rt Runtime, clock *fakeClock, at time.Duration, b store.Binding) store.Binding` -- `at` is the offset from `baseTime`.
  - `func writePlan(t *testing.T, body string) string` already exists in the package (used by `sentBinding`); reuse it.

- [ ] **Step 1: Add the helpers**

Add to the import block: `"github.com/fuad-daoud/relay/internal/candidate"`, `"github.com/fuad-daoud/relay/internal/git"`, `"github.com/fuad-daoud/relay/internal/store"`.

Append to `e2e_test.go`:

```go
const e2eCandidateJSON = `[{"harness":"agy","provider":"e2e","model":"shim","roles":["builder"]}]`
const e2eCandidate = "agy/e2e/shim"

// e2eRuntime is real everywhere but the clock and the state root (spec §3.4).
func e2eRuntime(t *testing.T, clock *fakeClock) Runtime {
	t.Helper()
	root := t.TempDir()
	candPath := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(candPath, []byte(e2eCandidateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}
	return Runtime{
		Herdr:       herdr.NewClient("herdr", 30*time.Second),
		Git:         git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Store:       store.New(filepath.Join(root, "state")),
		Candidates:  set,
		LedgerPath:  filepath.Join(root, "ledger.json"),
		HistoryPath: filepath.Join(root, "history.json"),
		Now:         clock.Now,
	}
}

// bindBuilder binds a fresh builder through the real Bind: a real tab, a real
// `agent start` of the shim (spec §4.3).
func (s *e2eSession) bindBuilder(t *testing.T, rt Runtime, name, plannerPane string) store.Binding {
	t.Helper()
	repo := t.TempDir()
	init := exec.Command("git", "init", "-q", repo)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: e2eCandidate, PlannerPane: plannerPane, CWD: repo,
	})
	if err != nil {
		t.Fatalf("Bind %s: %v", name, err)
	}
	s.waitScreen(t, b.Builder.PaneID, "shim ready")
	loaded, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	return loaded
}

// reconcileAt sets the clock to baseTime+at and runs one Reconcile the way
// the daemon does: under the lock, with a fresh real agent list (spec §3.5).
func reconcileAt(t *testing.T, s *e2eSession, rt Runtime, clock *fakeClock, at time.Duration, b store.Binding) store.Binding {
	t.Helper()
	clock.now = baseTime.Add(at)
	agents := s.agents(t)
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b, agents)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("Reconcile at T+%s: %v", at, err)
	}
	return out
}

// sendAt runs Send with the clock at baseTime+at.
func sendAt(t *testing.T, rt Runtime, clock *fakeClock, at time.Duration, name string) {
	t.Helper()
	clock.now = baseTime.Add(at)
	if _, err := Send(context.Background(), rt, name, writePlan(t, "do the thing")); err != nil {
		t.Fatalf("Send %s: %v", name, err)
	}
}

// reportEntry is the round's queued-or-delivered report entry.
func reportEntry(t *testing.T, rt Runtime, name string, round int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			return e
		}
	}
	t.Fatalf("no report entry for %s round %d", name, round)
	return store.LogEntry{}
}
```

`Bind` returns `(store.Binding, error)` and `(*store.Tx).Save(store.Binding) error` exists (`store.go:280`); `daemon.go` saves the same way after `Reconcile`.

- [ ] **Step 2: Add the marker subtest**

Inside `TestE2E`, after the `smoke` subtest, add:

```go
	clock := &fakeClock{now: baseTime}
	rt := e2eRuntime(t, clock)

	t.Run("marker_closes_round", func(t *testing.T) {
		b := s.bindBuilder(t, rt, "marker", planner)
		sendAt(t, rt, clock, 0, "marker")
		s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")

		if err := os.WriteFile(rt.Store.ReportPath("marker", 1), []byte("builder's words"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("marker", 1))
		b, _ = rt.Store.Load("marker")

		b = reconcileAt(t, s, rt, clock, 5*time.Second, b)
		if b.Round != 2 {
			t.Fatalf("round = %d, want 2: the marker closes the round on a real pane", b.Round)
		}
		if _, pending, _ := rt.Store.PendingForPlanner("marker"); pending {
			t.Errorf("report still pending: the planner shim is idle and unfocused, so deliverAndSettle must inject on the same tick")
		}
		s.waitScreen(t, planner, "you said: Builder finished round 1")
		if e := reportEntry(t, rt, "marker", 1); e.Note != "" {
			t.Errorf("note = %q, want empty on a marked close", e.Note)
		}
	})
```

- [ ] **Step 3: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: `smoke` and `marker_closes_round` PASS; no `relay-e2e-*` session left.

- [ ] **Step 4: Mutation check (report, then revert)**

Comment out the `touch(...)` line and rerun.
Expected: `round = 1, want 2`. Revert.

- [ ] **Step 5: Verify the default build and commit**

Run: `make check` -- green.

```bash
git add internal/relay/e2e_test.go
git commit -m "test(e2e): real Bind/Send/Reconcile/deliver round closes on the marker (#114 step 2)"
```

---

### Task 3: Nudge, then `unmarked`

**Files:**
- Modify: `internal/relay/e2e_test.go`

**Interfaces:**
- Consumes: Task 2's helpers; `nudgeNote` (`reconcile.go`); `startGrace`, `nudgeGrace`.
- Produces: subtests `idle_without_marker_nudges_once` and `still_screen_closes_unmarked` (spec §5.2, §5.3) sharing the binding `nudge`.

- [ ] **Step 1: Add the two subtests**

Inside `TestE2E`, after `marker_closes_round`:

```go
	var nudged store.Binding // shared by the next two subtests (spec §5.2 -> §5.3)

	t.Run("idle_without_marker_nudges_once", func(t *testing.T) {
		b := s.bindBuilder(t, rt, "nudge", planner)
		sendAt(t, rt, clock, 0, "nudge")
		s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")

		b = reconcileAt(t, s, rt, clock, 5*time.Second, b) // inside startGrace
		if b.Round != 1 {
			t.Fatalf("round = %d, want 1", b.Round)
		}
		if strings.Contains(s.screen(t, b.Builder.PaneID), "You went idle") {
			t.Fatal("nudged inside startGrace")
		}

		b = reconcileAt(t, s, rt, clock, startGrace+5*time.Second, b)
		s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")
		screen := s.screen(t, b.Builder.PaneID)
		for _, want := range []string{rt.Store.ReportPath("nudge", 1), rt.Store.DonePath("nudge", 1)} {
			if !strings.Contains(screen, want) {
				t.Errorf("nudge must name %s; screen:\n%s", want, screen)
			}
		}
		entries, err := rt.Store.ReadLog("nudge")
		if err != nil {
			t.Fatal(err)
		}
		nudges := 0
		for _, e := range entries {
			if e.Round == 1 && e.Note == nudgeNote {
				nudges++
			}
		}
		if nudges != 1 {
			t.Errorf("nudge entries = %d, want exactly 1", nudges)
		}
		if b.BuilderScreen == "" || !b.BuilderScreenAt.Equal(baseTime.Add(startGrace+5*time.Second)) {
			t.Errorf("fingerprint must be taken at nudge time: screen=%q at=%s", b.BuilderScreen, b.BuilderScreenAt)
		}
		nudged = b
	})

	t.Run("still_screen_closes_unmarked", func(t *testing.T) {
		if nudged.Name == "" {
			t.Skip("depends on idle_without_marker_nudges_once")
		}
		b := nudged
		if err := os.WriteFile(rt.Store.ReportPath("nudge", 1), []byte("the builder's own words"), 0o644); err != nil {
			t.Fatal(err)
		}

		b = reconcileAt(t, s, rt, clock, startGrace+6*time.Second, b)
		if b.Round != 1 {
			t.Fatalf("round = %d, want 1: one second is inside nudgeGrace", b.Round)
		}

		b = reconcileAt(t, s, rt, clock, startGrace+6*time.Second+nudgeGrace+10*time.Second, b)
		if b.Round != 2 {
			t.Fatalf("round = %d, want 2 after a still screen for nudgeGrace", b.Round)
		}
		e := reportEntry(t, rt, "nudge", 1)
		if e.Note != "unmarked" {
			t.Errorf("note = %q, want unmarked", e.Note)
		}
		if !strings.Contains(e.Payload, "never confirmed completion (no 001-done)") {
			t.Errorf("payload = %q", e.Payload)
		}
		body, err := os.ReadFile(rt.Store.ReportPath("nudge", 1))
		if err != nil || string(body) != "the builder's own words" {
			t.Errorf("report must be delivered as written, got %q err=%v", body, err)
		}
	})
```

- [ ] **Step 2: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: all four subtests PASS; no session left.

- [ ] **Step 3: Mutation checks (report each, then revert)**

1. In `idle_without_marker_nudges_once`, change the second tick's offset from `startGrace+5*time.Second` to `20*time.Second`. Expected: `waitScreen(... "You went idle ...")` fails -- start grace held. Revert.
2. In `still_screen_closes_unmarked`, change the final tick's offset to `startGrace+6*time.Second+30*time.Second`. Expected: `round = 1, want 2` -- nudge grace held. Revert.

- [ ] **Step 4: Verify the default build and commit**

Run: `make check` -- green.

```bash
git add internal/relay/e2e_test.go
git commit -m "test(e2e): idle builder is nudged once, then closes unmarked on a still real screen (#114 step 2)"
```

---

### Task 4: Scrape, and a moving screen resets the grace

**Files:**
- Modify: `internal/relay/e2e_test.go`

**Interfaces:**
- Consumes: Task 2's helpers; `s.herdr.Prompt`.
- Produces: subtests `still_screen_scrapes` and `screen_movement_resets_grace` (spec §5.4, §5.5).

- [ ] **Step 1: Add the two subtests**

Inside `TestE2E`, after `still_screen_closes_unmarked`:

```go
	t.Run("still_screen_scrapes", func(t *testing.T) {
		b := s.bindBuilder(t, rt, "scrape", planner)
		sendAt(t, rt, clock, 0, "scrape")
		s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")

		b = reconcileAt(t, s, rt, clock, startGrace+5*time.Second, b) // nudge
		s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")
		b = reconcileAt(t, s, rt, clock, startGrace+6*time.Second, b) // fingerprint after the echo
		b = reconcileAt(t, s, rt, clock, startGrace+6*time.Second+nudgeGrace+10*time.Second, b)
		if b.Round != 2 {
			t.Fatalf("round = %d, want 2 after the scrape", b.Round)
		}
		if e := reportEntry(t, rt, "scrape", 1); e.Note != "scraped" {
			t.Errorf("note = %q, want scraped", e.Note)
		}
		body, err := os.ReadFile(rt.Store.ReportPath("scrape", 1))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(body), "<!-- SCRAPED") {
			t.Errorf("scraped report must be labelled, got %q", body)
		}
		if !strings.Contains(string(body), "I implemented the guard clause") {
			t.Errorf("scrape must contain the real pane's text, got %q", body)
		}
	})

	t.Run("screen_movement_resets_grace", func(t *testing.T) {
		b := s.bindBuilder(t, rt, "moving", planner)
		sendAt(t, rt, clock, 0, "moving")
		s.waitScreen(t, b.Builder.PaneID, "Round 1 from the planner")

		b = reconcileAt(t, s, rt, clock, startGrace+5*time.Second, b) // nudge
		s.waitScreen(t, b.Builder.PaneID, "You went idle without finishing")
		b = reconcileAt(t, s, rt, clock, startGrace+6*time.Second, b) // fingerprint after the echo

		// Something else talks to the builder: the screen moves.
		if err := s.herdr.Prompt(context.Background(), b.Builder.PaneID, "keep talking"); err != nil {
			t.Fatalf("prompt: %v", err)
		}
		s.waitScreen(t, b.Builder.PaneID, "you said: keep talking")

		moved := startGrace + 6*time.Second + nudgeGrace - 5*time.Second // would be quiescent had the screen not moved
		b = reconcileAt(t, s, rt, clock, moved, b)
		if b.Round != 1 {
			t.Fatalf("round = %d, want 1: a screen that moved since the fingerprint is not quiescent", b.Round)
		}
		b = reconcileAt(t, s, rt, clock, moved+nudgeGrace+5*time.Second, b)
		if b.Round != 2 {
			t.Fatalf("round = %d, want 2: still for nudgeGrace after the move", b.Round)
		}
		if e := reportEntry(t, rt, "moving", 1); e.Note != "scraped" {
			t.Errorf("note = %q, want scraped", e.Note)
		}
	})
```

- [ ] **Step 2: Run it**

Run: `go vet -tags e2e ./internal/relay && go test -tags e2e -count=1 -run TestE2E ./internal/relay -v`
Expected: all six subtests PASS; no session left. Note the wall-clock time of the whole run in your report.

- [ ] **Step 3: Mutation check (report, then revert)**

In `screen_movement_resets_grace`, comment out the `s.herdr.Prompt(...)` call and its `waitScreen`. Expected: the `moved` tick returns `round = 2` (nothing moved, so it is quiescent) and the test fails with `want 1`. Revert.

- [ ] **Step 4: Verify the default build and commit**

Run: `make check` -- green.

```bash
git add internal/relay/e2e_test.go
git commit -m "test(e2e): a still real screen scrapes; a moving one resets the nudge grace (#114 step 2)"
```

---

### Task 5: `make e2e`, docs

**Files:**
- Modify: `Makefile` (add a target after `check`)
- Modify: `README.md` "Contributing" section
- Modify: `CLAUDE.md` "Verifying a builder's work"

**Interfaces:** none.

- [ ] **Step 1: Makefile**

After the `check:` recipe, add:

```make
# e2e runs one relay round against a private, detached herdr session with
# scripted agents (docs/specs/2026-09-12-e2e-real-herdr-design.md). Local
# only: it needs herdr on PATH and skips otherwise. Not part of check.
e2e:
	go vet -tags e2e ./internal/relay
	go test -tags e2e -count=1 -run TestE2E ./internal/relay -v
```

Use a tab, not spaces, for the recipe lines.

- [ ] **Step 2: README**

In the "Contributing" section (`## Contributing`, near line 916), append a paragraph:

```
`make e2e` runs one relay round -- bind, send, reconcile, delivery -- against
a private herdr session it creates and deletes, with two shell scripts
standing in for the agents. It needs `herdr` on PATH and skips otherwise; it
is not part of `make check` and does not run in CI. `RELAY_E2E_KEEP=1` leaves
the session up for a look.
```

- [ ] **Step 3: CLAUDE.md**

In "Verifying a builder's work", after the `make check` sentence, add:

```
`make e2e` additionally runs one round against a real, private herdr session
with scripted agents; it is local-only and not part of `make check`. Run it
after any change to `reconcile.go`'s nudge, fingerprint or scrape path.
```

- [ ] **Step 4: Run everything and commit**

Run: `make check && make e2e`
Expected: both green; `herdr session list` shows no `relay-e2e-*` row.

```bash
git add Makefile README.md CLAUDE.md
git commit -m "build: make e2e runs the real-herdr round; docs (#114 step 2)"
```

---

## Report

Write `NNN-report.md` with: the commit list (`git log --oneline main..HEAD`),
`git diff --stat main..HEAD`, the tail of `make check` and of `make e2e`
(including the per-subtest PASS lines and the total time), the observed
failure text from each mutation check, and anything you stopped on. Confirm
`herdr session list` shows no `relay-e2e-*` session. Then create the empty
`NNN-done` file the round prompt names, and reply with only the report path.
