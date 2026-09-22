package planner

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNow is the pinned clock every planner test writes records at.
var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// testRegistry is a FileRegistry on a fresh temp root with a pinned clock.
func testRegistry(t *testing.T) *FileRegistry {
	t.Helper()
	return &FileRegistry{
		Root: filepath.Join(t.TempDir(), "planners"),
		Now:  func() time.Time { return testNow },
	}
}

// mustCreate creates a record and fails the test if it is refused.
func mustCreate(t *testing.T, reg *FileRegistry, rec Record) Record {
	t.Helper()
	got, err := reg.Create(rec)
	if err != nil {
		t.Fatalf("Create(%s): %v", rec.ID, err)
	}
	return got
}

// record is a valid record with the given id, name, session and host.
func record(id, name, kind, session string, host int) Record {
	return Record{
		ID:            id,
		Name:          name,
		HarnessKind:   kind,
		SessionID:     session,
		HostPID:       host,
		HostStartedAt: int64(host) * 10,
		CWD:           "/tmp/relay-planner-test",
		CreatedAt:     testNow,
		SeenAt:        testNow,
	}
}

func TestNewIDShape(t *testing.T) {
	// A pinned reader makes the id exactly predictable, which pins the
	// alphabet and the 12-character width, not merely the prefix.
	if got, err := NewID(bytes.NewReader(make([]byte, 8))); err != nil {
		t.Fatalf("NewID: %v", err)
	} else if got != "pl_aaaaaaaaaaaa" {
		t.Errorf("NewID(zero reader) = %q, want %q", got, "pl_aaaaaaaaaaaa")
	}

	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id, err := NewID(rand.Reader)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if err := ValidID(id); err != nil {
			t.Fatalf("NewID produced %q: %v", id, err)
		}
		if !strings.HasPrefix(id, "pl_") {
			t.Fatalf("NewID produced %q, want the pl_ prefix", id)
		}
		if len(id) != len("pl_")+idChars {
			t.Fatalf("NewID produced %q, want %d characters", id, len("pl_")+idChars)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}

	if _, err := NewID(bytes.NewReader(nil)); err == nil {
		t.Error("NewID with an empty reader: want an error")
	}
}

func TestValidName(t *testing.T) {
	valid := []string{
		"a",
		"architect-1",
		"claude-1",
		"a0",
		"a-",
		"a" + strings.Repeat("b", MaxNameLen-1), // 32 characters: the cap
	}
	for _, name := range valid {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"A",
		"Architect-1",
		"1a",
		"-a",
		"_a",
		"a_b",
		"a.b",
		"a b",
		"architect_1",
		"a" + strings.Repeat("b", MaxNameLen), // 33 characters
	}
	for _, name := range invalid {
		err := ValidName(name)
		if err == nil {
			t.Errorf("ValidName(%q) = nil, want an error", name)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalid", name, err)
		}
	}

	if err := ValidID("pl_aaaaaaaaaaaa"); err != nil {
		t.Errorf("ValidID(valid) = %v, want nil", err)
	}
	// Uppercase base32, the wrong length and the wrong prefix are all out.
	for _, id := range []string{"pl_AAAAAAAAAAAA", "pl_aaaaaaaaaaa", "pl_aaaaaaaaaaaaa", "aaaaaaaaaaaa", "pl_aaaaaaaaaab1"} {
		if err := ValidID(id); err == nil {
			t.Errorf("ValidID(%q) = nil, want an error", id)
		}
	}
}

func TestDefaultNamePicksSmallestFree(t *testing.T) {
	taken := func(names ...string) func(string) bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		return func(candidate string) bool { return set[candidate] }
	}

	if got := DefaultName("architect", "claude", taken("architect-1")); got != "architect-2" {
		t.Errorf("DefaultName with architect-1 taken = %q, want architect-2", got)
	}
	if got := DefaultName("architect", "claude", nil); got != "architect-1" {
		t.Errorf("DefaultName with nothing taken = %q, want architect-1", got)
	}
	// An agent that cannot be a name prefix falls back to the harness kind.
	for _, agent := range []string{"", "My Agent", "1agent", "agent_1", strings.Repeat("a", 31)} {
		if got := DefaultName(agent, "claude", nil); got != "claude-1" {
			t.Errorf("DefaultName(%q, claude) = %q, want claude-1", agent, got)
		}
	}
	if got := DefaultName("", "opencode", taken("opencode-1", "opencode-2")); got != "opencode-3" {
		t.Errorf("DefaultName with two taken = %q, want opencode-3", got)
	}
}

func TestHookOutputExactJSON(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	want := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"You are relay planner architect-1 (pl_aaaaaaaaaaaa). RELAY_PLANNER is set in your shell; pass --planner architect-1 only to act as another planner."}}` + "\n"
	if got := string(HookOutput(rec)); got != want {
		t.Errorf("HookOutput:\n got %s\nwant %s", got, want)
	}

	wantNote := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"boom"}}` + "\n"
	if got := string(HookNote("boom")); got != wantNote {
		t.Errorf("HookNote:\n got %s\nwant %s", got, wantNote)
	}

	if got := EnvLine("pl_aaaaaaaaaaaa"); got != "export RELAY_PLANNER=pl_aaaaaaaaaaaa\n" {
		t.Errorf("EnvLine = %q", got)
	}
}

func TestParseHookInputRequiresSessionAndCWD(t *testing.T) {
	full := `{"hook_event_name":"SessionStart","source":"resume","session_id":"sess-1","transcript_path":"/tmp/t.jsonl","cwd":"/tmp/p","unknown":"ignored"}`
	in, err := ParseHookInput(strings.NewReader(full))
	if err != nil {
		t.Fatalf("ParseHookInput: %v", err)
	}
	if in.SessionID != "sess-1" || in.CWD != "/tmp/p" || in.TranscriptPath != "/tmp/t.jsonl" {
		t.Errorf("ParseHookInput = %+v", in)
	}
	if in.Source != SourceResume {
		t.Errorf("Source = %q, want %q", in.Source, SourceResume)
	}

	for _, payload := range []string{
		`not json`,
		`{"session_id":"sess-1"}`,
		`{"cwd":"/tmp/p"}`,
		`{"session_id":"","cwd":"/tmp/p"}`,
	} {
		if _, err := ParseHookInput(strings.NewReader(payload)); err == nil {
			t.Errorf("ParseHookInput(%s): want an error", payload)
		}
	}

	// An unknown source reads as startup (§3.4).
	in, err = ParseHookInput(strings.NewReader(`{"session_id":"s","cwd":"/tmp/p","source":"something-new"}`))
	if err != nil {
		t.Fatalf("ParseHookInput: %v", err)
	}
	if in.Source != SourceStartup {
		t.Errorf("Source = %q, want %q", in.Source, SourceStartup)
	}
}

func TestRecordValidate(t *testing.T) {
	if err := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0).Validate(); err != nil {
		t.Fatalf("valid record: %v", err)
	}

	// host_started_at must be 0 whenever host_pid is 0.
	bad := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0)
	bad.HostStartedAt = 42
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("host_started_at with host_pid 0: %v, want ErrInvalid", err)
	}

	// The opencode session rule.
	oc := record("pl_aaaaaaaaaaaa", "oc-1", "opencode", "not-a-session", 0)
	if err := oc.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("opencode session: %v, want ErrInvalid", err)
	}
	oc.SessionID = "ses_Abc123"
	if err := oc.Validate(); err != nil {
		t.Errorf("valid opencode session: %v", err)
	}

	// cwd must be absolute.
	rel := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0)
	rel.CWD = "relative/path"
	if err := rel.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("relative cwd: %v, want ErrInvalid", err)
	}
}

func TestErrUnknownPlannerNamesTheRef(t *testing.T) {
	var target ErrUnknownPlanner
	err := error(ErrUnknownPlanner{Ref: "beta"})
	if !errors.As(err, &target) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if target.Ref != "beta" {
		t.Errorf("Ref = %q, want beta", target.Ref)
	}
	if !strings.Contains(err.Error(), "beta") {
		t.Errorf("Error() = %q, want it to name beta", err.Error())
	}
}

// TestListReadsEveryRecordOnDisk pins the file layout and the sort order: one
// <id>.json per record, by name.
func TestListReadsEveryRecordOnDisk(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "zeta", "claude", "s-b", 0))
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "s-a", 0))

	records, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}
	if records[0].Name != "alpha" || records[1].Name != "zeta" {
		t.Errorf("List order = %q, %q; want alpha, zeta", records[0].Name, records[1].Name)
	}

	raw, err := os.ReadFile(filepath.Join(reg.Root, "pl_aaaaaaaaaaaa.json"))
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var onDisk Record
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode record file: %v", err)
	}
	if onDisk.Name != "alpha" || onDisk.SessionID != "s-a" {
		t.Errorf("record file = %+v", onDisk)
	}
}
