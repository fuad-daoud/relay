package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// seedReadVerbStore builds a binding with one completed round: a plan entry
// and a report entry in its log, and a report file on disk. It is store-only
// -- a t.TempDir(), no harness and no network -- because the point of these
// tests is that the two shared read helpers print what the client verbs print
// while leaving the owner's state untouched.
func seedReadVerbStore(t *testing.T) (*store.Store, relay.Runtime, []store.LogEntry) {
	t.Helper()

	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{
		Name: "api", CWD: t.TempDir(), Round: 2, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Note: "round one"},
	} {
		if err := s.AppendLog("api", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	if err := os.WriteFile(s.ReportPath("api", 1), []byte("# report body\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	log, err := s.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	return s, relay.Runtime{Store: s, Now: time.Now}, log
}

// TestPrintLogAndPrintShowWriteNothing is `relay serve log`/`show`'s
// read-only contract (#216) at the helper level: with markViewed=false the
// same text a client verb prints comes out, no .viewed sidecar appears, and
// printShow with allowDB=false creates no database. markViewed=true is the
// control that proves the assertion can fail.
func TestPrintLogAndPrintShowWriteNothing(t *testing.T) {
	s, rt, log := seedReadVerbStore(t)

	stdout, _, err := captureOutput(t, func() error {
		return printLog(rt, "api", 0, 0, false, false, false)
	})
	if err != nil {
		t.Fatalf("printLog: %v", err)
	}
	for _, e := range log {
		if line := relay.LogLine(e); !strings.Contains(string(stdout), line) {
			t.Errorf("printLog output:\n%s\nlacks %q", stdout, line)
		}
	}
	if _, ok := s.ViewedAt("api"); ok {
		t.Error("printLog with markViewed=false must not stamp .viewed")
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return printShow(rt, relay.ShowOptions{Name: "api", Section: relay.ShowReport}, false, false, "alice/")
	})
	if err != nil {
		t.Fatalf("printShow: %v", err)
	}
	if !strings.Contains(string(stdout), "# report body") {
		t.Errorf("printShow stdout = %q, want the report text", stdout)
	}
	if !strings.Contains(string(stderr), "alice/api round 1 of 1 · report") {
		t.Errorf("printShow stderr = %q, want the owner-prefixed header", stderr)
	}
	if _, ok := s.ViewedAt("api"); ok {
		t.Error("printShow with markViewed=false must not stamp .viewed")
	}
	if _, err := os.Stat(s.DBPath()); !os.IsNotExist(err) {
		t.Errorf("printShow must not create a database: stat err %v", err)
	}

	// Control: markViewed=true does stamp, so the assertion above is not
	// passing for want of a sidecar either way.
	if _, _, err := captureOutput(t, func() error {
		return printLog(rt, "api", 0, 0, false, false, true)
	}); err != nil {
		t.Fatalf("printLog control: %v", err)
	}
	if _, ok := s.ViewedAt("api"); !ok {
		t.Error("printLog with markViewed=true must stamp .viewed (the control)")
	}
}

// TestPrintShowAllowDBFalseRefusesAnUnknownName pins the other half of
// allowDB=false: a binding that is not live is store.ErrNotFound, not a
// database read that would create relay.db as a side effect.
func TestPrintShowAllowDBFalseRefusesAnUnknownName(t *testing.T) {
	s, rt, _ := seedReadVerbStore(t)

	err := printShow(rt, relay.ShowOptions{Name: "nosuch", Section: relay.ShowReport}, false, false, "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("printShow(allowDB=false) on an unknown name = %v, want store.ErrNotFound", err)
	}
	if _, err := os.Stat(s.DBPath()); !os.IsNotExist(err) {
		t.Errorf("printShow must not create a database: stat err %v", err)
	}
}
