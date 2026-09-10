package store

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
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

func TestPendingForPlannerReturnsArrivalOrder(t *testing.T) {
	s, name := seedBinding(t)

	// A report, a blocked-dialog question and a drift note can already be
	// pending together today; consults only make it routine. Arrival order is
	// the only order relay can defend without judging content.
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
		if err := s.ConfirmIndex(name, idx); err != nil {
			t.Fatalf("ConfirmIndex: %v", err)
		}
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("queue not drained: found=%v err=%v", found, err)
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

	if err := s.ConfirmIndex(name, 2); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	entries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if entries[1].Confirmed {
		t.Error("index 1 confirmed; ConfirmIndex must confirm only the index it was given")
	}
	if !entries[2].Confirmed {
		t.Error("index 2 not confirmed")
	}
	if entries[2].DeliveredAt == nil {
		t.Error("DeliveredAt not stamped on the confirmed entry")
	}
}

func TestConfirmIndexClearsPending(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmIndex(name, 0); err != nil {
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

	if err := s.ConfirmIndex(name, 7); err == nil {
		t.Fatal("ConfirmIndex(7) on a 1-entry log returned nil; an out-of-range index is a caller bug, not a no-op")
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
