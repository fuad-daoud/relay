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
