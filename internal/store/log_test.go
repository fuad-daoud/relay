package store

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

func seedBinding(t *testing.T) (*Store, string) {
	t.Helper()
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, "webshop"
}

func TestAppendAndReadLog(t *testing.T) {
	s, name := seedBinding(t)

	first := LogEntry{TS: time.Now().UTC(), Round: 1, Direction: DirToBuilder, Kind: KindPlan, Path: "/x/001-plan.md", Confirmed: true}
	second := LogEntry{TS: time.Now().UTC(), Round: 1, Direction: DirToPlanner, Kind: KindReport, Path: "/x/001-report.md", Payload: "report ready"}

	for _, e := range []LogEntry{first, second} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Kind != KindPlan || got[1].Direction != DirToPlanner {
		t.Errorf("entries out of order or mistyped: %+v", got)
	}
}

func TestPendingOnEmptyLogIsNotAnError(t *testing.T) {
	s, name := seedBinding(t)
	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("found=%v err=%v, want false/nil", found, err)
	}
}

func TestReadLogOnNeverWrittenLogIsNilNil(t *testing.T) {
	s, name := seedBinding(t)

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestReadLogRefusesOversizedLogInsteadOfTruncating(t *testing.T) {
	s, name := seedBinding(t)

	entry := LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x", Confirmed: true}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var buf bytes.Buffer
	for i := 0; i < maxLogEntries+1; i++ {
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(s.logPath(name), buf.Bytes(), bindingFileMode); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	_, err = s.ReadLog(name)
	if err == nil {
		t.Fatal("ReadLog: got nil error, want a refusal for an oversized log")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want it to mention the log exceeding the bound", err.Error())
	}
}

func TestAppendLogTakesLockOnlyOnce(t *testing.T) {
	s, name := seedBinding(t)

	err := s.WithLock(func(tx *Tx) error {
		return tx.AppendLog(name, LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true})
	})
	if err != nil {
		t.Fatalf("WithLock append: %v", err)
	}

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
}

func TestKindExitIsDistinct(t *testing.T) {
	kinds := []Kind{KindPlan, KindReport, KindQuestion, KindAnswer, KindDiff, KindDrift, KindFork, KindPick, KindSwitch, KindAsk, KindFindings, KindExit}
	seen := map[Kind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
	if KindExit != "exit" {
		t.Errorf("KindExit = %q, want exit", KindExit)
	}
}

func TestLogEntryCommitFactsRoundTripAndAreOmittedWhenUnknown(t *testing.T) {
	data, err := json.Marshal(LogEntry{Kind: KindDiff})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"commits", "tree"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("expected %s to be omitted when unknown, got JSON: %s", key, data)
		}
	}

	in := LogEntry{Kind: KindDiff, Commits: 3, Tree: "clean"}
	data, err = json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out LogEntry
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Commits != 3 || out.Tree != "clean" {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

func TestAppendLogAssignsSeq(t *testing.T) {
	s, name := seedBinding(t)

	for i := 1; i <= 3; i++ {
		if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPlan}); err != nil {
			t.Fatalf("AppendLog %d: %v", i, err)
		}
	}
	// A caller-supplied Seq is overwritten: appendLog owns the numbering.
	if err := s.AppendLog(name, LogEntry{Seq: 99, Round: 1, Direction: DirToBuilder, Kind: KindPlan}); err != nil {
		t.Fatalf("AppendLog 4: %v", err)
	}

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("entry %d: Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestReadLogFillsSeqForPreSeqFile(t *testing.T) {
	s, name := seedBinding(t)

	// Three raw lines with no seq key: a file written before Seq existed.
	raw := `{"ts":"2026-09-10T10:00:00.000Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true}
{"ts":"2026-09-10T10:00:01.000Z","round":1,"direction":"to_planner","kind":"report","confirmed":true}
{"ts":"2026-09-10T10:01:00.000Z","round":2,"direction":"to_builder","kind":"plan","confirmed":true}
`
	if err := os.WriteFile(s.logPath(name), []byte(raw), bindingFileMode); err != nil {
		t.Fatalf("seed pre-Seq log: %v", err)
	}

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("pre-Seq entry %d: Seq = %d, want %d", i, e.Seq, i+1)
		}
	}

	if err := s.AppendLog(name, LogEntry{Round: 2, Direction: DirToPlanner, Kind: KindReport}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	got, err = s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog after append: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("entry %d after append: Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestReadLogAfter(t *testing.T) {
	s, name := seedBinding(t)

	for i := 1; i <= 3; i++ {
		if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPlan}); err != nil {
			t.Fatalf("AppendLog %d: %v", i, err)
		}
	}

	got, err := s.ReadLogAfter(name, 2)
	if err != nil {
		t.Fatalf("ReadLogAfter(2): %v", err)
	}
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("ReadLogAfter(2) = %+v, want just Seq 3", got)
	}

	none, err := s.ReadLogAfter(name, 3)
	if err != nil {
		t.Fatalf("ReadLogAfter(3): %v", err)
	}
	if none != nil {
		t.Errorf("ReadLogAfter(3) = %+v, want nil", none)
	}

	all, err := s.ReadLogAfter(name, 0)
	if err != nil {
		t.Fatalf("ReadLogAfter(0): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ReadLogAfter(0) returned %d entries, want 3", len(all))
	}
}

func TestLogEntryUsageRoundTrip(t *testing.T) {
	s, name := seedBinding(t)
	with := LogEntry{
		TS: time.Unix(1, 0).UTC(), Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "p",
		Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 4200,
			Tokens: usage.Tokens{In: 1, CacheRead: 2, CacheWrite: 3, Out: 4},
			Cost:   usage.Cost{USD: 0.5, Basis: usage.Measured}, Samples: 1},
	}
	without := LogEntry{TS: time.Unix(2, 0).UTC(), Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "q"}
	if err := s.AppendLog(name, with); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(name, without); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Usage == nil || *got[0].Usage != *with.Usage {
		t.Errorf("usage round trip: %+v", got[0].Usage)
	}
	if got[1].Usage != nil {
		t.Errorf("an entry without usage must read back nil, got %+v", got[1].Usage)
	}
	raw, _ := json.Marshal(without)
	if strings.Contains(string(raw), "usage") {
		t.Errorf("an entry without usage must marshal byte-identically to before: %s", raw)
	}
}

func TestPendingForPlannerReturnsArrivalOrder(t *testing.T) {
	s, name := seedBinding(t)

	// A report, a blocked-dialog question and a drift note can already be
	// pending together today; consults only make it routine. Arrival order is
	// the only order relevo can defend without judging content.
	for _, e := range []LogEntry{
		{Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "report r3"},
		{Round: 3, Direction: DirToPlanner, Kind: KindQuestion, Payload: "question r3"},
		{Round: 3, Direction: DirToPlanner, Kind: KindDrift, Payload: "drift r3"},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	want := []string{"report r3", "question r3", "drift r3"}
	for i, w := range want {
		var got LogEntry
		var idx int
		var ok bool
		err := s.WithLock(func(tx *Tx) error {
			var err error
			got, idx, ok, err = tx.PendingForPlanner(name)
			return err
		})
		if err != nil || !ok {
			t.Fatalf("delivery %d: PendingForPlanner ok=%v err=%v", i, ok, err)
		}
		if got.Payload != w {
			t.Fatalf("delivery %d = %q, want %q", i, got.Payload, w)
		}
		if err := s.ConfirmIndex(name, idx, ""); err != nil {
			t.Fatalf("ConfirmIndex: %v", err)
		}
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("queue not drained: found=%v err=%v", found, err)
	}
}

func TestPendingForPlannerThrough(t *testing.T) {
	s, name := seedBinding(t)

	for _, e := range []LogEntry{
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "report r1"},
		{Round: 1, Direction: DirToBuilder, Kind: KindPlan, Payload: "plan r1", Confirmed: true},
		{Round: 2, Direction: DirToPlanner, Kind: KindReport, Payload: "report r2"},
		{Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "report r3"},
		{Round: 1, Direction: DirToPlanner, Kind: KindQuestion, Payload: "question r1", Confirmed: true},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	through := func(round int) []PendingEntry {
		t.Helper()
		var got []PendingEntry
		if err := s.WithLock(func(tx *Tx) error {
			var err error
			got, err = tx.PendingForPlannerThrough(name, round)
			return err
		}); err != nil {
			t.Fatalf("PendingForPlannerThrough(%d): %v", round, err)
		}
		return got
	}

	got := through(2)
	if len(got) != 2 {
		t.Fatalf("Through(2) returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Entry.Payload != "report r1" || got[0].Idx != 0 {
		t.Errorf("Through(2)[0] = (idx %d, %q), want (0, report r1)", got[0].Idx, got[0].Entry.Payload)
	}
	if got[1].Entry.Payload != "report r2" || got[1].Idx != 2 {
		t.Errorf("Through(2)[1] = (idx %d, %q), want (2, report r2)", got[1].Idx, got[1].Entry.Payload)
	}

	got = through(0)
	if len(got) != 3 {
		t.Fatalf("Through(0) returned %d entries, want 3: %+v", len(got), got)
	}
	for i, want := range []string{"report r1", "report r2", "report r3"} {
		if got[i].Entry.Payload != want {
			t.Errorf("Through(0)[%d] = %q, want %q", i, got[i].Entry.Payload, want)
		}
	}
}

func TestConfirmIndexConfirmsOnlyTheNamedEntry(t *testing.T) {
	s, name := seedBinding(t)

	for _, e := range []LogEntry{
		{Round: 1, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true},
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "first"},
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "second"},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	// Confirm the OLDER of the two pending entries. Naming index 2 would pin
	// nothing: index 2 is also the newest unconfirmed entry, so an
	// implementation that ignored idx and confirmed the newest would pass.
	if err := s.ConfirmIndex(name, 1, ""); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	entries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !entries[1].Confirmed {
		t.Error("index 1 not confirmed; ConfirmIndex must confirm the index it was given")
	}
	if entries[2].Confirmed {
		t.Error("index 2 confirmed; ConfirmIndex must confirm ONLY the index it was given, never the newest")
	}
	if entries[1].DeliveredAt == nil {
		t.Error("DeliveredAt not stamped on the confirmed entry")
	}
}

func TestConfirmIndexClearsPending(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmIndex(name, 0, ""); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("still pending after confirm: found=%v err=%v", found, err)
	}
}

func TestConfirmIndexRejectsAnIndexOutsideTheLog(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmIndex(name, 7, ""); err == nil {
		t.Fatal("ConfirmIndex(7) on a 1-entry log returned nil; an out-of-range index is a caller bug, not a no-op")
	}
}

func TestConfirmIndexKeepsSeq(t *testing.T) {
	s, name := seedBinding(t)

	raw := `{"ts":"2026-09-10T10:00:00.000Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true}
{"ts":"2026-09-10T10:00:01.000Z","round":1,"direction":"to_planner","kind":"report","confirmed":false}
{"ts":"2026-09-10T10:01:00.000Z","round":2,"direction":"to_builder","kind":"plan","confirmed":true}
`
	if err := os.WriteFile(s.logPath(name), []byte(raw), bindingFileMode); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	if err := s.ConfirmIndex(name, 1, ""); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	got, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("confirmed entry %d: Seq = %d, want %d", i, e.Seq, i+1)
		}
	}

	// The rewrite writes Seq through: the second entry's entry_json now
	// carries "seq":2 (D2: the log is a DB row, not a file).
	stored := bindingEvents(t, s, name)
	if len(stored) != 3 {
		t.Fatalf("stored events = %d, want 3", len(stored))
	}
	if !strings.Contains(stored[1].JSON, `"seq":2`) {
		t.Errorf("second entry_json = %s, want it to contain %q", stored[1].JSON, `"seq":2`)
	}
}

// TestConfirmIndexPreservesUnknownKeys pins #372 §4.3: confirmIndex patches
// the one line it changes from a map, so a key a newer relevo wrote survives,
// and every line it does not change keeps its exact bytes.
func TestConfirmIndexPreservesUnknownKeys(t *testing.T) {
	s, name := seedBinding(t)

	raw := `{"seq":1,"ts":"2026-09-10T10:00:00.000Z","round":1,"direction":"to_builder","kind":"plan","confirmed":true,"future_key":1}
{"seq":2,"ts":"2026-09-10T10:00:01.000Z","round":1,"direction":"to_planner","kind":"report","confirmed":false,"future_key":1}
{"seq":3,"ts":"2026-09-10T10:01:00.000Z","round":2,"direction":"to_builder","kind":"plan","confirmed":true,"future_key":1}
`
	if err := os.WriteFile(s.logPath(name), []byte(raw), bindingFileMode); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	before := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")

	// Confirm the middle line, so both a changed line and unchanged lines on
	// either side are exercised.
	if err := s.ConfirmIndex(name, 1, "channel"); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	stored := bindingEvents(t, s, name)
	if len(stored) != len(before) {
		t.Fatalf("stored events = %d, want %d", len(stored), len(before))
	}
	if stored[0].JSON != before[0] {
		t.Errorf("unchanged entry 0 changed:\n got %s\nwant %s", stored[0].JSON, before[0])
	}
	if stored[2].JSON != before[2] {
		t.Errorf("unchanged entry 2 changed:\n got %s\nwant %s", stored[2].JSON, before[2])
	}
	for i, ev := range stored {
		if !strings.Contains(ev.JSON, `"future_key":1`) {
			t.Errorf("entry %d lost future_key: %s", i, ev.JSON)
		}
	}
	if !strings.Contains(stored[1].JSON, `"confirmed":true`) {
		t.Errorf("confirmed entry = %s, want it confirmed", stored[1].JSON)
	}
	if !strings.Contains(stored[1].JSON, `"route":"channel"`) {
		t.Errorf("confirmed entry = %s, want the route recorded", stored[1].JSON)
	}
}
