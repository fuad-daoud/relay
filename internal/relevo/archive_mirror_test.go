package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// TestTabEntriesTotalsSurviveArchiving pins the P3d §4.4 rule and the step's
// own test: the entries TabEntries gathers -- and the totals TabRows sums
// from them -- are the same for a binding before and after archiving it.
func TestTabEntriesTotalsSurviveArchiving(t *testing.T) {
	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{Name: "webshop", CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for r := 1; r <= 2; r++ {
		if err := s.AppendLog("webshop", store.LogEntry{
			TS: tabNow, Round: r, Direction: store.DirToPlanner, Kind: store.KindReport,
			Usage: &usage.Usage{Harness: "h", Provider: "anthropic", Model: "claude-sonnet-5",
				Tokens: usage.Tokens{In: 1000 * int64(r)}, Samples: 1},
		}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	rt := Runtime{Store: s, Now: func() time.Time { return tabNow }}
	warn := func(string) {}

	before, err := TabEntries(rt, time.Time{}, warn)
	if err != nil {
		t.Fatalf("TabEntries before: %v", err)
	}
	rowsBefore, totalBefore, err := TabRows(before, "binding", time.Time{})
	if err != nil {
		t.Fatalf("TabRows before: %v", err)
	}

	if _, err := s.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	after, err := TabEntries(rt, time.Time{}, warn)
	if err != nil {
		t.Fatalf("TabEntries after: %v", err)
	}
	rowsAfter, totalAfter, err := TabRows(after, "binding", time.Time{})
	if err != nil {
		t.Fatalf("TabRows after: %v", err)
	}

	if len(before) != len(after) {
		t.Errorf("entry count changed across archiving: before %d, after %d", len(before), len(after))
	}
	if len(rowsBefore) != 1 || len(rowsAfter) != 1 {
		t.Fatalf("rows before=%+v after=%+v, want one group each", rowsBefore, rowsAfter)
	}
	if rowsBefore[0] != rowsAfter[0] {
		t.Errorf("row changed across archiving: before %+v, after %+v", rowsBefore[0], rowsAfter[0])
	}
	if totalBefore != totalAfter {
		t.Errorf("total changed across archiving: before %+v, after %+v", totalBefore, totalAfter)
	}
}

// TestMirrorArchivedFeedsOnce pins P3d §4.5: the one-time archived feed
// ingests each archived record into the mirror, keyed by kv
// "ingested.archive.<recordID>", and a second pass changes nothing.
func TestMirrorArchivedFeedsOnce(t *testing.T) {
	rt := newRuntime(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	rt.DB = d

	if err := rt.Store.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 2, State: store.StateDone}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	mirrorArchived(context.Background(), rt)

	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("Binding(webshop): found=%v err=%v", found, err)
	}
	if row.ArchivedAt == nil {
		t.Error("the mirror binding has no ArchivedAt, want the archive stamp")
	}
	key := "ingested.archive." + archived[0].RecordID
	if _, ok, err := d.KVGet(key); err != nil || !ok {
		t.Errorf("kv %s = ok %v, err %v; want it set", key, ok, err)
	}

	before, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	mirrorArchived(context.Background(), rt)
	after, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for tbl, n := range before.Rows {
		if after.Rows[tbl] != n {
			t.Errorf("a second pass changed Rows[%s]: before %d, after %d", tbl, n, after.Rows[tbl])
		}
	}
}
