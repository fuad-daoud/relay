package usage

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeExec struct {
	out  []byte
	err  error
	bin  string
	args []string
}

func (f *fakeExec) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.bin, f.args = bin, args
	return f.out, f.err
}

func TestOpencodeStreamOneSamplePerStep(t *testing.T) {
	got := opencodeStream(mustOpen(t, "testdata/opencode-stream.jsonl"), "openrouter", "z-ai/glm-5.3-flash")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 step_finish parts", len(got))
	}
	// Out = output + reasoning.
	if got[0].Tokens != (Tokens{In: 1000, CacheRead: 10, CacheWrite: 20, Out: 106}) {
		t.Errorf("first = %+v", got[0].Tokens)
	}
	if !got[0].HasCost || got[0].USD != 0.001 {
		t.Errorf("first cost = %v/%v, want measured 0.001", got[0].USD, got[0].HasCost)
	}
	if got[1].Model != "z-ai/glm-5.3-flash" || got[1].Provider != "openrouter" {
		t.Errorf("model/provider = %q/%q, want the candidate's (the stream has none)", got[1].Model, got[1].Provider)
	}
}

func TestOpencodeQueryShape(t *testing.T) {
	start := time.UnixMilli(1789150600000).UTC()
	end := time.UnixMilli(1789150700000).UTC()
	q := OpencodeQuery("/wt/o'brien", start, end)
	for _, want := range []string{
		"select m.data from message m join session s on s.id = m.session_id",
		"s.directory = '/wt/o''brien'",
		"m.time_created between 1789150600000 and 1789150700000",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q\n  lacks %q", q, want)
		}
	}
}

func TestOpencodeRows(t *testing.T) {
	raw, err := os.ReadFile("testdata/opencode-db.json")
	if err != nil {
		t.Fatal(err)
	}
	got := opencodeRows(raw)
	// The user row is skipped; both assistant rows count (a zero row is a
	// record, not an absence).
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2: %+v", len(got), got)
	}
	if got[0].Tokens != (Tokens{In: 8825, Out: 71}) || !got[0].HasCost || got[0].USD != 0.00135925 {
		t.Errorf("first = %+v", got[0])
	}
	if got[0].Model != "z-ai/glm-5.3-flash" || got[0].Provider != "openrouter" {
		t.Errorf("model/provider = %q/%q", got[0].Model, got[0].Provider)
	}
}

func TestOpencodeDBRunsSqlite3ReadOnly(t *testing.T) {
	raw, _ := os.ReadFile("testdata/opencode-db.json")
	fe := &fakeExec{out: raw}
	db := mustExisting(t)
	got, note := opencodeDB(context.Background(), fe, db, "/wt", time.UnixMilli(0), time.UnixMilli(1))
	if note != "" || len(got) != 2 {
		t.Fatalf("got %d samples, note %q", len(got), note)
	}
	if fe.bin != "sqlite3" {
		t.Errorf("bin = %q", fe.bin)
	}
	joined := strings.Join(fe.args, " ")
	if !strings.Contains(joined, "-readonly") || !strings.Contains(joined, "-json") || !strings.Contains(joined, db) {
		t.Errorf("args = %v, want -readonly -json <db> <query>", fe.args)
	}
}

func TestOpencodeDBNotes(t *testing.T) {
	db := mustExisting(t)
	if _, note := opencodeDB(context.Background(), nil, db, "/wt", time.Time{}, time.Now()); note != "sqlite3 not on PATH" {
		t.Errorf("nil exec: note = %q", note)
	}
	if _, note := opencodeDB(context.Background(), &fakeExec{}, "/nonexistent/opencode.db", "/wt", time.Time{}, time.Now()); note != "no opencode store" {
		t.Errorf("missing db: note = %q", note)
	}
	fe := &fakeExec{err: errors.New("Error: unable to open database\nsecond line")}
	if _, note := opencodeDB(context.Background(), fe, db, "/wt", time.Time{}, time.Now()); note != "sqlite3: Error: unable to open database" {
		t.Errorf("exec error: note = %q, want the first stderr line", note)
	}
}

func mustExisting(t *testing.T) string {
	t.Helper()
	_, name := mustTemp(t, "")
	return name
}
