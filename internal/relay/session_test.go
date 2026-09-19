package relay

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestHomeSessionLocatorGlobsAnySlug(t *testing.T) {
	home := t.TempDir()
	slugA := filepath.Join(home, ".claude", "projects", "-a-slug")
	slugB := filepath.Join(home, ".claude", "projects", "-b-slug")
	if err := os.MkdirAll(slugA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(slugB, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(slugB, "S.jsonl")
	newer := filepath.Join(slugA, "S.jsonl")
	if err := os.WriteFile(older, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	locate := HomeSessionLocator(home)

	if path, ok := locate("claude", "S"); !ok || path != newer {
		t.Errorf("locate(claude, S) = %q, %v; want the newer match %q, true", path, ok, newer)
	}
	if _, ok := locate("claude", "missing"); ok {
		t.Error("a missing session must not locate")
	}
	if _, ok := locate("opencode", "S"); ok {
		t.Error("a non-claude kind must not locate")
	}
	if _, ok := locate("claude", "../x"); ok {
		t.Error("a path separator in the id must be refused")
	}
	if _, ok := locate("claude", "*"); ok {
		t.Error("a glob metacharacter in the id must be refused")
	}
}

// sessionLocatorFor is a SessionLocator fixture that maps exactly (kind,
// sessionID) to path.
func sessionLocatorFor(kind, sessionID, path string) SessionLocator {
	return func(k, id string) (string, bool) {
		if k == kind && id == sessionID {
			return path, true
		}
		return "", false
	}
}

func TestArmSessionCursorUsesRecordSize(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "S.jsonl")
	if err := os.WriteFile(recordPath, make([]byte, 120), 0o644); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{Name: "webshop", Round: 3, Builder: store.Endpoint{Kind: "claude", SessionID: "S"}}

	rt := Runtime{Store: store.New(t.TempDir()), Sessions: sessionLocatorFor("claude", "S", recordPath)}
	got := armSessionCursor(rt, b)
	if got.Builder.StreamRound != 3 || got.Builder.StreamOffset != 120 {
		t.Errorf("StreamRound/StreamOffset = %d/%d, want 3/120", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if want := rt.Store.BuilderLogPath("webshop", 3); got.Builder.LogPath != want {
		t.Errorf("LogPath = %q, want %q", got.Builder.LogPath, want)
	}

	rtNotLocated := Runtime{Store: store.New(t.TempDir()), Sessions: func(string, string) (string, bool) { return "", false }}
	gotNotLocated := armSessionCursor(rtNotLocated, b)
	if gotNotLocated.Builder.StreamOffset != 0 {
		t.Errorf("offset = %d, want 0 when not located", gotNotLocated.Builder.StreamOffset)
	}
	if gotNotLocated.Builder.StreamRound != 3 || gotNotLocated.Builder.LogPath == "" {
		t.Errorf("StreamRound/LogPath must still be set when not located: %d %q", gotNotLocated.Builder.StreamRound, gotNotLocated.Builder.LogPath)
	}

	rtNilSessions := Runtime{Store: store.New(t.TempDir())}
	gotNil := armSessionCursor(rtNilSessions, b)
	if gotNil.Builder.StreamOffset != 0 {
		t.Errorf("offset = %d, want 0 with nil Sessions", gotNil.Builder.StreamOffset)
	}
	if gotNil.Builder.StreamRound != 3 || gotNil.Builder.LogPath == "" {
		t.Errorf("StreamRound/LogPath must still be set with nil Sessions: %d %q", gotNil.Builder.StreamRound, gotNil.Builder.LogPath)
	}
}

func TestDrainSessionAppendsRenderedRecords(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "S.jsonl")

	assistantLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`
	modeLine := `{"type":"mode","mode":"accept-edits"}` // housekeeping: renders as nothing
	userLine := `{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"ok"}]}}`
	thirdFull := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`
	prefix := thirdFull[:40] // a partial trailing line: no newline yet

	complete := assistantLine + "\n" + modeLine + "\n" + userLine + "\n"
	if err := os.WriteFile(recordPath, []byte(complete+prefix), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Store: store.New(t.TempDir()), Sessions: sessionLocatorFor("claude", "S", recordPath)}
	b := store.Binding{Name: "webshop", Round: 1, Builder: store.Endpoint{Kind: "claude", SessionID: "S", StreamRound: 1, StreamOffset: 0}}
	if err := os.MkdirAll(rt.Store.Dir("webshop"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := drainSession(rt, b)
	wantLog := "● Read a.go\n  ⎿ ok: ok\n"
	gotLog, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLog) != wantLog {
		t.Errorf("log = %q, want %q", gotLog, wantLog)
	}
	if want := int64(len(complete)); got.Builder.StreamOffset != want {
		t.Errorf("offset = %d, want %d (through the second newline; the partial line waits)", got.Builder.StreamOffset, want)
	}
	// A tick with no new bytes changes nothing.
	again := drainSession(rt, got)
	if !reflect.DeepEqual(again, got) {
		t.Errorf("a tick with no new bytes must not change the binding: got %+v, want %+v", again.Builder, got.Builder)
	}

	// The rest of the partial line arrives.
	f, err := os.OpenFile(recordPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(thirdFull[40:] + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got2 := drainSession(rt, got)
	wantLog2 := wantLog + "done\n"
	gotLog2, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLog2) != wantLog2 {
		t.Errorf("log after completing the partial line = %q, want %q", gotLog2, wantLog2)
	}
	if want := int64(len(complete + thirdFull + "\n")); got2.Builder.StreamOffset != want {
		t.Errorf("offset = %d, want %d", got2.Builder.StreamOffset, want)
	}
}

func TestDrainSessionPreconditions(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "S.jsonl")
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(recordPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	locate := sessionLocatorFor("claude", "S", recordPath)
	base := store.Binding{Name: "webshop", Round: 1, Builder: store.Endpoint{Kind: "claude", SessionID: "S", StreamRound: 1}}

	withSessionID := func(id string) store.Binding {
		c := base
		c.Builder.SessionID = id
		return c
	}
	withStreamRound := func(r int) store.Binding {
		c := base
		c.Builder.StreamRound = r
		return c
	}
	headless := func() store.Binding {
		c := base
		c.Builder.Mode = store.ModeHeadless
		return c
	}

	cases := map[string]struct {
		sessions SessionLocator
		b        store.Binding
	}{
		"nil Sessions":     {nil, base},
		"empty SessionID":  {locate, withSessionID("")},
		"StreamRound 0":    {locate, withStreamRound(0)},
		"headless builder": {locate, headless()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rt := Runtime{Store: store.New(t.TempDir()), Sessions: c.sessions}
			got := drainSession(rt, c.b)
			if !reflect.DeepEqual(got, c.b) {
				t.Errorf("binding changed: got %+v, want unchanged %+v", got.Builder, c.b.Builder)
			}
			if _, err := os.Stat(rt.Store.BuilderLogPath(c.b.Name, c.b.Round)); err == nil {
				t.Error("log file must not be created")
			}
		})
	}
}

func TestDrainSessionOffsetPastEOFResets(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "S.jsonl")
	content := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n")
	if err := os.WriteFile(recordPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: store.New(t.TempDir()), Sessions: sessionLocatorFor("claude", "S", recordPath)}
	b := store.Binding{Name: "webshop", Round: 1, Builder: store.Endpoint{Kind: "claude", SessionID: "S", StreamRound: 1, StreamOffset: 999}}
	if err := os.MkdirAll(rt.Store.Dir("webshop"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := drainSession(rt, b)
	wantLog := "hi\n"
	gotLog, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLog) != wantLog {
		t.Errorf("log = %q, want %q", gotLog, wantLog)
	}
	if want := int64(len(content)); got.Builder.StreamOffset != want {
		t.Errorf("offset = %d, want the file size %d", got.Builder.StreamOffset, want)
	}
}
