package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// stdinFile is a temp file holding payload, rewound and ready to be os.Stdin.
func stdinFile(t *testing.T, payload string) *os.File {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "hook-stdin-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	if _, err := f.WriteString(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind payload: %v", err)
	}
	return f
}

// runWithStdin runs one verb with payload on stdin, capturing both outputs.
func runWithStdin(t *testing.T, payload string, args ...string) (stdout, stderr []byte, err error) {
	t.Helper()

	orig := os.Stdin
	os.Stdin = stdinFile(t, payload)
	defer func() { os.Stdin = orig }()

	return captureOutput(t, func() error { return run(args) })
}

// hookEnvelope is the shape both `relevo planner init --hook` answers use.
type hookEnvelope struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// TestPlannerInitHookAlwaysExitsZero is the hook's contract (§4.4, §6.1): a
// hook failure must never block a Claude Code session, so malformed or
// incomplete stdin still exits 0 and answers on stdout with the note envelope.
func TestPlannerInitHookAlwaysExitsZero(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Never write to whatever the developer's own CLAUDE_ENV_FILE names.
	t.Setenv("CLAUDE_ENV_FILE", "")

	for _, payload := range []string{
		"not json at all",
		`{"hook_event_name":"SessionStart","session_id":"sess-1"}`, // no cwd
		`{"hook_event_name":"SessionStart","cwd":"/tmp/p"}`,        // no session
	} {
		stdout, stderr, err := runWithStdin(t, payload, "planner", "init", "--hook", "claude")
		if err != nil {
			t.Fatalf("run(%s) = %v, want exit 0", payload, err)
		}

		var env hookEnvelope
		if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
			t.Fatalf("stdout for %s is not the hook envelope: %v: %q", payload, err, stdout)
		}
		if env.HookSpecificOutput.HookEventName != "SessionStart" {
			t.Errorf("hookEventName = %q, want SessionStart", env.HookSpecificOutput.HookEventName)
		}
		if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "relevo planner init failed") {
			t.Errorf("additionalContext for %s = %q, want the failure note", payload, env.HookSpecificOutput.AdditionalContext)
		}
		if len(stderr) == 0 {
			t.Errorf("stderr for %s is empty; the error belongs there too", payload)
		}
	}
}

// TestPlannerInitHookWritesEnvFile is the hook's happy path: a good payload
// registers a planner, appends the export line to $CLAUDE_ENV_FILE, and tells
// the model which planner it is.
func TestPlannerInitHookWritesEnvFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	envFile := filepath.Join(t.TempDir(), "claude-env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("CLAUDE_CODE_AGENT", "architect")

	payload := `{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-abc","transcript_path":"/tmp/t.jsonl","cwd":"/tmp/planner-cwd"}`
	stdout, _, err := runWithStdin(t, payload, "planner", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read %s: %v", envFile, err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "export RELEVO_PLANNER=pl_") {
		t.Fatalf("env file holds %q, want an export RELEVO_PLANNER=<id> line", line)
	}
	id := strings.TrimPrefix(line, "export RELEVO_PLANNER=")

	// The record is on disk under the state root, and the record's name comes
	// from CLAUDE_CODE_AGENT.
	recordPath := filepath.Join(state, "relevo", "planners", id+".json")
	recRaw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read %s: %v", recordPath, err)
	}
	var rec planner.Record
	if err := json.Unmarshal(recRaw, &rec); err != nil {
		t.Fatalf("decode %s: %v", recordPath, err)
	}
	if rec.ID != id || rec.SessionID != "sess-abc" || rec.HarnessKind != "claude" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Name != "architect-1" {
		t.Errorf("Name = %q, want architect-1", rec.Name)
	}
	if rec.TranscriptLocator != "/tmp/t.jsonl" || rec.CWD != "/tmp/planner-cwd" {
		t.Errorf("record = %+v, want the hook payload's transcript and cwd", rec)
	}
	if rec.HostPID != os.Getppid() {
		t.Errorf("HostPID = %d, want the hook's parent pid %d", rec.HostPID, os.Getppid())
	}

	// And the model is told.
	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "You are relevo planner architect-1 ("+id+")") {
		t.Errorf("additionalContext = %q, want it to name architect-1 (%s)", env.HookSpecificOutput.AdditionalContext, id)
	}
}

// TestPlannerInitHookWithoutEnvFileSaysSo pins §3.4's rule for an unset
// $CLAUDE_ENV_FILE: `init --hook` still registers and exits 0, and its
// additionalContext says RELEVO_PLANNER could not be exported and that relevo
// resolves the session through its host process instead.
func TestPlannerInitHookWithoutEnvFileSaysSo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_CODE_AGENT", "architect")

	payload := `{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-noenv","cwd":"/tmp/planner-cwd"}`
	stdout, _, err := runWithStdin(t, payload, "planner", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "You are relevo planner architect-1 (pl_") {
		t.Errorf("additionalContext = %q, want it to name the planner", ctx)
	}
	if !strings.Contains(ctx, "RELEVO_PLANNER could not be exported ($CLAUDE_ENV_FILE is unset); relevo resolves this session through its host process.") {
		t.Errorf("additionalContext = %q, want the unset-env-file note", ctx)
	}

	// Registration still happened: one record is on disk under the state root.
	entries, err := os.ReadDir(filepath.Join(state, "relevo", "planners"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	records := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("registry holds %d records, want exactly 1", records)
	}
}

// TestPlannerVerbsListRenameForget exercises the explicit registration and the
// three read/manage verbs, including forget's guard: a binding that is not DONE
// still names the record, so it must be refused.
func TestPlannerVerbsListRenameForget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"planner", "init", "--kind", "opencode", "--session", "ses_abc123"})
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) != 2 {
		t.Fatalf("init printed %d lines, want 2:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "planner opencode-1 (pl_") || !strings.HasSuffix(lines[0], " created") {
		t.Errorf("first line = %q, want \"planner opencode-1 (<id>) created\"", lines[0])
	}
	if !strings.HasPrefix(lines[1], "export RELEVO_PLANNER=pl_") {
		t.Fatalf("second line = %q, want the export line", lines[1])
	}
	id := strings.TrimPrefix(lines[1], "export RELEVO_PLANNER=")

	if stdout, _, err = captureOutput(t, func() error {
		return run([]string{"planner", "rename", id, "reviewer-2"})
	}); err != nil {
		t.Fatalf("rename: %v", err)
	} else if !strings.Contains(string(stdout), "reviewer-2") {
		t.Errorf("rename printed %q, want the new name", stdout)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"planner", "list", "--json"}) })
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var records []planner.Record
	if err := json.Unmarshal(stdout, &records); err != nil {
		t.Fatalf("list --json is not a records array: %v: %q", err, stdout)
	}
	if len(records) != 1 || records[0].Name != "reviewer-2" || records[0].ID != id {
		t.Fatalf("records = %+v, want one named reviewer-2", records)
	}

	// A tabular list names the planner and its session.
	stdout, _, err = captureOutput(t, func() error { return run([]string{"planner", "list"}) })
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(string(stdout), "reviewer-2") || !strings.Contains(string(stdout), "ses_abc123") {
		t.Errorf("list printed %q, want the name and the session", stdout)
	}

	// A live binding that names the planner refuses the forget.
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	b := store.Binding{Name: "webshop", CWD: t.TempDir(), Round: 1, State: store.StateActive, PlannerID: id}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, _, err := captureOutput(t, func() error { return run([]string{"planner", "forget", id}) }); !errors.Is(err, planner.ErrInUse) {
		t.Fatalf("forget with a live binding = %v, want ErrInUse", err)
	}

	// Once the binding is DONE, the record is free.
	b, err = s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.State = store.StateDone
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"planner", "forget", id}) })
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(string(stdout), "forgot") {
		t.Errorf("forget printed %q, want a confirmation line", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "planners", id+".json")); !os.IsNotExist(err) {
		t.Errorf("record file still there after forget: %v", err)
	}
}

// TestAnnotatePlannerChat is #386's CLI surface: annotatePlannerChat fills a
// row's chat label and link from the planner record it names, resolves a
// repeated id once, and leaves a row naming an unknown planner empty. It writes
// a synthetic claude transcript and names claude records only, so the test
// spawns nothing and reaches no network.
func TestAnnotatePlannerChat(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	reg := &planner.FileRegistry{Root: filepath.Join(root, "planners")}

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{\"type\":\"custom-title\",\"customTitle\":\"my chat\"}\n" +
		"{\"type\":\"bridge-session\",\"bridgeSessionId\":\"cse_01ABCDEF\"}\n"
	if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	const knownID = "pl_aaaaaaaaaaaa"
	if _, err := reg.Create(planner.Record{
		ID: knownID, Name: "alpha", HarnessKind: "claude",
		SessionID: "sess-alpha", CWD: t.TempDir(), TranscriptLocator: transcript,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rt := relevo.Runtime{Planners: reg}
	rep := relevo.Report{Bindings: []relevo.BindingStatus{
		{Name: "one", PlannerID: knownID},
		{Name: "two", PlannerID: knownID},
		{Name: "three", PlannerID: "pl_zzzzzzzzzzzz"},
	}}

	annotatePlannerChat(rt, &rep, chatlabel.Resolver{})

	const wantText = "my chat"
	const wantLink = "https://claude.ai/code/session_01ABCDEF"
	for i := 0; i < 2; i++ {
		if rep.Bindings[i].PlannerChatLabel != wantText || rep.Bindings[i].PlannerChatLink != wantLink {
			t.Errorf("row %d carries label %q / link %q, want %q / %q",
				i, rep.Bindings[i].PlannerChatLabel, rep.Bindings[i].PlannerChatLink, wantText, wantLink)
		}
	}
	if rep.Bindings[2].PlannerChatLabel != "" || rep.Bindings[2].PlannerChatLink != "" {
		t.Errorf("the unknown planner's row carries label %q / link %q, want neither",
			rep.Bindings[2].PlannerChatLabel, rep.Bindings[2].PlannerChatLink)
	}
}

// TestListAlignsLongValues is the list polish: the old fixed-width format
// shifted every later column when a 36-character session id or a long cwd
// overflowed its cell. Under tabwriter every row -- header included -- starts
// its `seen` column at the same byte offset.
func TestListAlignsLongValues(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	reg := &planner.FileRegistry{Root: filepath.Join(root, "planners")}

	long := filepath.Join(t.TempDir(), strings.Repeat("long-cwd-segment-", 6))
	longer := filepath.Join(t.TempDir(), strings.Repeat("an-even-longer-cwd-segment-", 6))
	for _, rec := range []planner.Record{
		{
			ID: "pl_aaaaaaaaaaaa", Name: "alpha", HarnessKind: "claude",
			SessionID: "f26cad68-8a43-4de9-80c6-7b13d88aafd0", // 36 characters
			CWD:       long,
		},
		{
			ID: "pl_bbbbbbbbbbbb", Name: "beta", HarnessKind: "claude",
			SessionID: "f26cad68-8a43-4de9-80c6-7b13d88aafd1", // 36 characters
			CWD:       longer,
		},
	} {
		if _, err := reg.Create(rec); err != nil {
			t.Fatalf("Create(%s): %v", rec.ID, err)
		}
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"planner", "list"}) })
	if err != nil {
		t.Fatalf("planner list: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("planner list printed %d lines, want a header and two rows:\n%s", len(lines), stdout)
	}

	// seen is the last column, so its cell is the line's last space-separated
	// token: the byte after the final space. Every line must agree on it.
	want := -1
	for i, line := range lines {
		at := strings.LastIndex(line, " ") + 1
		if at <= 0 {
			t.Fatalf("line %d has no seen column:\n%s", i, line)
		}
		if want < 0 {
			want = at
			continue
		}
		if at != want {
			t.Errorf("line %d's seen column starts at byte %d, want %d:\n%s", i, at, want, line)
		}
	}
}

// TestListShowsChat is the #386 chat column: a claude planner's row names the
// chat the way its own harness does, a planner with nothing readable shows "-",
// and --json carries the same two fields. It writes a synthetic transcript and
// uses claude records only, so the test spawns nothing and reaches no network.
func TestListShowsChat(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	reg := &planner.FileRegistry{Root: filepath.Join(root, "planners")}

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{\"type\":\"custom-title\",\"customTitle\":\"my chat\"}\n" +
		"{\"type\":\"bridge-session\",\"bridgeSessionId\":\"cse_01ABCDEF\"}\n"
	if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	cwd := t.TempDir()
	for _, rec := range []planner.Record{
		{
			ID: "pl_aaaaaaaaaaaa", Name: "alpha", HarnessKind: "claude",
			SessionID: "sess-alpha", CWD: cwd, TranscriptLocator: transcript,
		},
		{
			ID: "pl_bbbbbbbbbbbb", Name: "beta", HarnessKind: "claude",
			SessionID: "sess-beta", CWD: cwd,
		},
	} {
		if _, err := reg.Create(rec); err != nil {
			t.Fatalf("Create(%s): %v", rec.Name, err)
		}
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"planner", "list"}) })
	if err != nil {
		t.Fatalf("planner list: %v", err)
	}

	rows := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("planner list printed %d lines, want a header and two rows:\n%s", len(rows), stdout)
	}
	if !strings.Contains(rows[0], "chat") {
		t.Errorf("header %q does not name the chat column", rows[0])
	}

	rowFor := func(name string) string {
		for _, row := range rows[1:] {
			if strings.HasPrefix(row, name) {
				return row
			}
		}
		t.Fatalf("no row for %s in:\n%s", name, stdout)
		return ""
	}

	wantChat := "my chat · https://claude.ai/code/session_01ABCDEF"
	if got := rowFor("alpha"); !strings.Contains(got, wantChat) {
		t.Errorf("alpha's row %q does not contain %q", got, wantChat)
	}
	beta := rowFor("beta")
	if fields := strings.Fields(beta); len(fields) < 2 || fields[1] != "-" {
		t.Errorf("beta's chat cell is not \"-\" in row %q", beta)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"planner", "list", "--json"}) })
	if err != nil {
		t.Fatalf("planner list --json: %v", err)
	}
	var views []map[string]any
	if err := json.Unmarshal(stdout, &views); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout)
	}
	if len(views) != 2 {
		t.Fatalf("--json printed %d records, want 2", len(views))
	}
	if got := views[0]["chat_label"]; got != "my chat" {
		t.Errorf("alpha chat_label = %v, want %q", got, "my chat")
	}
	if got := views[0]["chat_link"]; got != "https://claude.ai/code/session_01ABCDEF" {
		t.Errorf("alpha chat_link = %v, want the claude.ai url", got)
	}
	if _, ok := views[1]["chat_label"]; ok {
		t.Errorf("beta should carry no chat_label, got %v", views[1]["chat_label"])
	}
	if _, ok := views[1]["chat_link"]; ok {
		t.Errorf("beta should carry no chat_link, got %v", views[1]["chat_link"])
	}
}
