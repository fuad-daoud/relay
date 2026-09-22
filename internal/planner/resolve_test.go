package planner

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// envFunc turns a map into the func(string) string Detect and Resolve read.
func envFunc(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// procStartAt always reports one start time, whatever the pid.
func procStartAt(started int64) func(int) (int64, error) {
	return func(int) (int64, error) { return started, nil }
}

// procStartFails reports "no host match", which §4.3 says is not an error.
func procStartFails() func(int) (int64, error) {
	return func(int) (int64, error) { return 0, errors.New("ps: no such process") }
}

// TestResolveOrder is the whole precedence rule in one table: flag > env >
// host > session, a host whose ProcStart fails falling through to session, and
// a flag matching nothing being ErrUnknownPlanner.
func TestResolveOrder(t *testing.T) {
	reg := testRegistry(t)
	alpha := mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101))
	beta := mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-b", 102))

	claudeAt := func(pid int, session string) map[string]string {
		return map[string]string{
			"CLAUDECODE":             "1",
			"CLAUDE_PID":             strconv.Itoa(pid),
			"CLAUDE_CODE_SESSION_ID": session,
		}
	}

	tests := []struct {
		name    string
		in      ResolveInput
		wantID  string
		wantRes Resolution
	}{
		{
			name: "flag beats env, host and session",
			in: ResolveInput{
				Flag:      "beta",
				Env:       envFunc(withEnv(claudeAt(101, "sess-a"), "RELAY_PLANNER", "alpha")),
				PPID:      101,
				ProcStart: procStartAt(1010),
			},
			wantID:  beta.ID,
			wantRes: ResolutionFlag,
		},
		{
			name: "flag by id",
			in: ResolveInput{
				Flag:      alpha.ID,
				Env:       envFunc(map[string]string{}),
				PPID:      999,
				ProcStart: procStartAt(0),
			},
			wantID:  alpha.ID,
			wantRes: ResolutionFlag,
		},
		{
			name: "env beats host and session",
			in: ResolveInput{
				Env:       envFunc(withEnv(claudeAt(101, "sess-a"), "RELAY_PLANNER", "beta")),
				PPID:      101,
				ProcStart: procStartAt(1010),
			},
			wantID:  beta.ID,
			wantRes: ResolutionEnv,
		},
		{
			name: "env by id",
			in: ResolveInput{
				Env:       envFunc(withEnv(claudeAt(0, ""), "RELAY_PLANNER", alpha.ID)),
				PPID:      999,
				ProcStart: procStartAt(0),
			},
			wantID:  alpha.ID,
			wantRes: ResolutionEnv,
		},
		{
			name: "host beats session",
			in: ResolveInput{
				Env:       envFunc(claudeAt(101, "sess-b")),
				PPID:      999,
				ProcStart: procStartAt(1010),
			},
			wantID:  alpha.ID,
			wantRes: ResolutionHost,
		},
		{
			name: "session when the host matches nothing",
			in: ResolveInput{
				Env:       envFunc(claudeAt(777, "sess-b")),
				PPID:      777,
				ProcStart: procStartAt(7770),
			},
			wantID:  beta.ID,
			wantRes: ResolutionSession,
		},
		{
			name: "a host ProcStart error falls through to session",
			in: ResolveInput{
				Env:       envFunc(claudeAt(101, "sess-b")),
				PPID:      101,
				ProcStart: procStartFails(),
			},
			wantID:  beta.ID,
			wantRes: ResolutionSession,
		},
		{
			name: "the parent pid is the host when CLAUDE_PID is unset",
			in: ResolveInput{
				Env:       envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-b"}),
				PPID:      102,
				ProcStart: procStartAt(1020),
			},
			wantID:  beta.ID,
			wantRes: ResolutionHost,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, res, err := Resolve(reg, tc.in)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if rec.ID != tc.wantID {
				t.Errorf("resolved %s (%s), want %s", rec.ID, rec.Name, tc.wantID)
			}
			if res != tc.wantRes {
				t.Errorf("resolution = %q, want %q", res, tc.wantRes)
			}
		})
	}

	// A flag matching nothing is ErrUnknownPlanner, naming the value.
	_, _, err := Resolve(reg, ResolveInput{
		Flag:      "nope",
		Env:       envFunc(claudeAt(101, "sess-a")),
		PPID:      101,
		ProcStart: procStartAt(1010),
	})
	var unknown ErrUnknownPlanner
	if !errors.As(err, &unknown) || unknown.Ref != "nope" {
		t.Errorf("unknown flag = %v, want ErrUnknownPlanner{Ref: nope}", err)
	}
	if errors.Is(err, ErrNoPlanner) {
		t.Errorf("unknown flag reported ErrNoPlanner; an unknown ref is the other error")
	}

	// The same for an environment value.
	_, _, err = Resolve(reg, ResolveInput{
		Env:       envFunc(withEnv(claudeAt(0, ""), "RELAY_PLANNER", "nope")),
		PPID:      999,
		ProcStart: procStartAt(0),
	})
	if !errors.As(err, &unknown) || unknown.Ref != "nope" {
		t.Errorf("unknown env = %v, want ErrUnknownPlanner{Ref: nope}", err)
	}

	// Nothing at all is ErrNoPlanner, whose text is the fix.
	if _, _, err := Resolve(reg, ResolveInput{Env: envFunc(map[string]string{}), PPID: 999}); !errors.Is(err, ErrNoPlanner) {
		t.Errorf("no detection = %v, want ErrNoPlanner", err)
	}

	// Resolution touches what it found.
	rec, err := reg.Get(beta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.SeenAt.Before(testNow) {
		t.Errorf("SeenAt = %v, want it stamped at or after the pinned now %v", rec.SeenAt, testNow)
	}
}

// TestResolveNeverCreates pins §4.3's "Resolve never registers": an empty
// registry resolves nothing, says so with ErrNoPlanner, and leaves no file
// behind.
func TestResolveNeverCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planners")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	reg := &FileRegistry{Root: dir, Now: func() time.Time { return testNow }}

	in := ResolveInput{
		Env:       envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-a"}),
		PPID:      101,
		ProcStart: procStartAt(1010),
		Now:       testNow,
	}
	if _, _, err := Resolve(reg, in); !errors.Is(err, ErrNoPlanner) {
		t.Fatalf("Resolve on an empty registry = %v, want ErrNoPlanner", err)
	}

	// Even a flag that matches nothing must not register the value.
	if _, _, err := Resolve(reg, ResolveInput{Flag: "ghost", Env: in.Env, PPID: in.PPID, Now: testNow}); !errors.Is(err, ErrUnknownPlanner{Ref: "ghost"}) {
		t.Fatalf("Resolve with an unknown flag = %v, want ErrUnknownPlanner", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("resolve left %v in the registry; it must create nothing", names)
	}
}

// TestDetectOnlyClaude pins §4.2: only claude is detected, and its host pid is
// CLAUDE_PID when that parses, else the caller's parent.
func TestDetectOnlyClaude(t *testing.T) {
	if _, ok := Detect(envFunc(map[string]string{"OPENCODE_TERMINAL": "1"}), 42); ok {
		t.Error("Detect reported opencode; only claude is detectable")
	}
	if _, ok := Detect(nil, 42); ok {
		t.Error("Detect(nil) reported a harness")
	}

	ident, ok := Detect(envFunc(map[string]string{
		"CLAUDECODE":             "1",
		"CLAUDE_PID":             "4242",
		"CLAUDE_CODE_SESSION_ID": "sess-a",
	}), 42)
	if !ok {
		t.Fatal("Detect did not report claude")
	}
	if ident.Kind != "claude" || ident.SessionID != "sess-a" || ident.HostPID != 4242 {
		t.Errorf("Detect = %+v, want claude/sess-a/4242", ident)
	}

	// Inside `relay mcp` and the hook CLAUDE_PID is unset: the parent pid is
	// the Claude process.
	ident, ok = Detect(envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-a"}), 42)
	if !ok || ident.HostPID != 42 {
		t.Errorf("Detect without CLAUDE_PID = %+v (ok %v), want HostPID 42", ident, ok)
	}

	// A CLAUDE_PID that is not a positive number is ignored for the same
	// reason.
	for _, pid := range []string{"", "0", "-3", "not-a-pid"} {
		ident, ok = Detect(envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_PID": pid}), 42)
		if !ok || ident.HostPID != 42 {
			t.Errorf("Detect with CLAUDE_PID=%q = %+v (ok %v), want HostPID 42", pid, ident, ok)
		}
	}
}

func withEnv(m map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}
