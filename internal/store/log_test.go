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
	if err := s.Save(newBinding("upjo", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, "upjo"
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

func TestPendingForPlannerFindsNewestUnconfirmed(t *testing.T) {
	s, name := seedBinding(t)

	entries := []LogEntry{
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "old", Confirmed: true},
		{Round: 2, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true},
		{Round: 2, Direction: DirToPlanner, Kind: KindReport, Payload: "new"},
	}
	for _, e := range entries {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	got, found, err := s.PendingForPlanner(name)
	if err != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, err)
	}
	if got.Payload != "new" {
		t.Errorf("payload = %q, want %q", got.Payload, "new")
	}
}

func TestConfirmLatestClearsPending(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmLatest(name); err != nil {
		t.Fatalf("ConfirmLatest: %v", err)
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("still pending after confirm: found=%v err=%v", found, err)
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

func TestConfirmLatestOnlyConfirmsNewestUnconfirmed(t *testing.T) {
	s, name := seedBinding(t)

	entries := []LogEntry{
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "older"},
		{Round: 2, Direction: DirToPlanner, Kind: KindReport, Payload: "newer"},
	}
	for _, e := range entries {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	if err := s.ConfirmLatest(name); err != nil {
		t.Fatalf("ConfirmLatest: %v", err)
	}

	got, found, err := s.PendingForPlanner(name)
	if err != nil {
		t.Fatalf("PendingForPlanner: %v", err)
	}
	if !found {
		t.Fatal("found=false, want the older entry still pending")
	}
	if got.Payload != "older" {
		t.Errorf("payload = %q, want %q (only the newest should be confirmed)", got.Payload, "older")
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
