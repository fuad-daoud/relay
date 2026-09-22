//go:build e2e

package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
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
	// e2eEchoTimeout bounds waitEcho: the shim spends one real second per
	// prompt line, and the rendered builder prompt is twenty-two lines: the
	// origin line (#139), the working-tree halt rule (#192), the three
	// paths and the report block skeleton (#133).
	e2eEchoTimeout = 35 * time.Second
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
// After any prompt the shim reads as "done" (it flashes a working line, then
// erases it); relay treats done as idle, and the cases never assert "idle" on
// a prompted builder.
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

// screen reads a pane's recent-unwrapped scrollback, the source the planner
// delivery path still uses.
func (s *e2eSession) screen(t *testing.T, target string) string {
	t.Helper()
	const screenLines = 200
	text, err := s.herdr.ReadAgentSource(context.Background(), target, "recent-unwrapped", screenLines)
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

// The shim echoes every line it is given, one real second apart, and herdr
// reports it working until the last one.
const promptLastLine = "Reply here with only the report path."

// waitEcho waits until target has echoed lastLine -- the shim has consumed
// the whole prompt -- AND herdr's agent list no longer reports it working.
// herdr's status lags the screen by a few hundred milliseconds; a tick taken
// in that window sees `working` and never reaches the idle path.
func (s *e2eSession) waitEcho(t *testing.T, target, lastLine string) {
	t.Helper()
	want := "you said: " + lastLine
	deadline := time.Now().Add(e2eEchoTimeout)
	var last, status string
	for time.Now().Before(deadline) {
		last = s.screen(t, target)
		if strings.Contains(last, want) {
			status = ""
			for _, a := range s.agents(t) {
				if a.PaneID == target {
					status = a.Status
				}
			}
			if status != "" && status != herdr.StatusWorking {
				return
			}
		}
		time.Sleep(e2ePoll)
	}
	t.Fatalf("target %s: echo of %q seen=%v, last status %q, within %s; last screen:\n%s",
		target, want, strings.Contains(last, want), status, e2eEchoTimeout, last)
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

	t.Run("prompt_last_lines", func(t *testing.T) {
		if !strings.HasSuffix(builderPrompt, promptLastLine) {
			t.Errorf("promptLastLine %q is not the last line of builderPrompt", promptLastLine)
		}
	})

	clock := &fakeClock{now: baseTime}
	rt := e2eRuntime(t, clock)

	t.Run("marker_closes_round", func(t *testing.T) {
		b := s.bindHeadlessBuilder(t, rt, "marker", planner)
		sendAt(t, rt, clock, 0, "marker")

		if err := os.WriteFile(rt.Store.ReportPath("marker", 1), []byte("builder's words"), 0o644); err != nil {
			t.Fatal(err)
		}
		touch(t, rt.Store.DonePath("marker", 1))
		b, _ = rt.Store.Load("marker")

		b = reconcileAt(t, s, rt, clock, 5*time.Second, b)
		if b.Round != 2 {
			t.Fatalf("round = %d, want 2: the marker closes the round", b.Round)
		}
		if _, pending, _ := rt.Store.PendingForPlanner("marker"); pending {
			t.Errorf("report still pending: the planner shim is idle and unfocused, so deliverAndSettle must inject on the same tick")
		}
		s.waitScreen(t, planner, "you said: Builder finished round 1")
		if e := reportEntry(t, rt, "marker", 1); e.Note != "" {
			t.Errorf("note = %q, want empty on a marked close", e.Note)
		}
	})
}

const e2eCandidateJSON = `[{"harness":"agy","provider":"e2e","model":"shim","roles":["builder"]}]`
const e2eCandidate = "agy/e2e/shim"

// e2eRunner is a minimal real Runner for this test. It cannot use
// internal/proc: that package imports internal/relay, and this file is in
// package relay, so the import would cycle.
type e2eRunner struct {
	mu    sync.Mutex
	procs map[int]*os.Process
}

func newE2ERunner() *e2eRunner { return &e2eRunner{procs: map[int]*os.Process{}} }

func (r *e2eRunner) Start(_ context.Context, spec ProcSpec) (ProcHandle, error) {
	if len(spec.Argv) == 0 {
		return ProcHandle{}, errors.New("empty argv")
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	logf, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ProcHandle{}, err
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return ProcHandle{}, err
	}
	h := ProcHandle{PID: cmd.Process.Pid, StartedAt: time.Now()}
	r.mu.Lock()
	r.procs[h.PID] = cmd.Process
	r.mu.Unlock()
	go func() { _ = cmd.Wait(); logf.Close() }()
	return h, nil
}

func (r *e2eRunner) Alive(_ context.Context, h ProcHandle) (bool, error) {
	r.mu.Lock()
	p, ok := r.procs[h.PID]
	r.mu.Unlock()
	if !ok {
		return false, nil
	}
	return p.Signal(syscall.Signal(0)) == nil, nil
}

func (r *e2eRunner) ExitCode(_ context.Context, _ ProcHandle, _ string) (int, bool) {
	return 0, false
}

func (r *e2eRunner) Kill(_ context.Context, h ProcHandle) error {
	r.mu.Lock()
	p, ok := r.procs[h.PID]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return p.Kill()
}

func (r *e2eRunner) Rusage(_ context.Context, _ ProcHandle, _ string) (ProcRusage, bool) {
	return ProcRusage{}, false
}

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
		Herdr:            herdr.NewClient("herdr", 30*time.Second),
		Git:              git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Runner:           newE2ERunner(),
		Store:            store.New(filepath.Join(root, "state")),
		Candidates:       set,
		LedgerPath:       filepath.Join(root, "ledger.json"),
		AvailabilityPath: filepath.Join(root, "availability.json"),
		Now:              clock.Now,
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

// bindHeadlessBuilder binds a fresh headless builder through the real Bind:
// no pane, no agent start -- Send starts the shim as a process (#303).
func (s *e2eSession) bindHeadlessBuilder(t *testing.T, rt Runtime, name, plannerPane string) store.Binding {
	t.Helper()
	repo := t.TempDir()
	init := exec.Command("git", "init", "-q", repo)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: e2eCandidate, PlannerPane: plannerPane, CWD: repo, Headless: true,
	}); err != nil {
		t.Fatalf("Bind %s: %v", name, err)
	}
	loaded, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	return loaded
}

// reconcileAt sets the clock to baseTime+at and runs one Reconcile the way
// the daemon does: under the lock, on the binding as stored (Send and earlier
// ticks have written to it; the caller's copy may be stale), with a fresh
// real agent list (spec §3.5).
func reconcileAt(t *testing.T, s *e2eSession, rt Runtime, clock *fakeClock, at time.Duration, b store.Binding) store.Binding {
	t.Helper()
	clock.now = baseTime.Add(at)
	agents := s.agents(t)
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		fresh, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		out, err = Reconcile(context.Background(), rt, tx, fresh, agents)
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
	if _, err := Send(context.Background(), rt, name, writePlan(t, "do the thing"), SendOptions{}); err != nil {
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
