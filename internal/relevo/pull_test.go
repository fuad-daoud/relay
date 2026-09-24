package relevo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPullPendingReturnsAndMarksDelivered: pullPending hands back the oldest
// pending payload and confirms it with the route it was given -- the helper
// `relevo wait` calls with route "wait" (P4a round 2 §4.1).
func TestPullPendingReturnsAndMarksDelivered(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	payload, found, err := pullPending(context.Background(), rt, "webshop", "wait")
	if err != nil {
		t.Fatalf("pullPending: %v", err)
	}
	if !found || payload == "" {
		t.Fatalf("pullPending found=%v payload=%q, want the queued report", found, payload)
	}
	if _, still, err := rt.Store.PendingForPlanner("webshop"); err != nil || still {
		t.Errorf("pullPending must confirm what it returns (still pending=%v err=%v)", still, err)
	}
}

// TestPullPendingWithNothingPending: nothing queued reads as nothing to print.
func TestPullPendingWithNothingPending(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")
	if _, _, err := pullPending(context.Background(), rt, "webshop", "wait"); err != nil {
		t.Fatalf("first pullPending: %v", err)
	}

	payload, found, err := pullPending(context.Background(), rt, "webshop", "wait")
	if err != nil {
		t.Fatalf("second pullPending: %v", err)
	}
	if found || payload != "" {
		t.Errorf("second pullPending = (%q, %v), want nothing pending", payload, found)
	}
}

// TestPullPendingMarksDeliveredRoute: pullPending marks the entry delivered
// with the route the caller passed -- "wait" from Wait (§4.1).
func TestPullPendingMarksDeliveredRoute(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	if _, _, err := pullPending(context.Background(), rt, "webshop", "wait"); err != nil {
		t.Fatalf("pullPending: %v", err)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed {
		t.Error("the entry must be confirmed")
	}
	if last.Route != "wait" {
		t.Errorf("Route = %q, want wait", last.Route)
	}
}

// seedPendingReport saves an active binding and one unconfirmed planner-bound
// entry with the caller's Path and Kind, so pullPending's expansion (PushText)
// is exercised against a file the test owns. seedPending's own entry points at
// the literal /tmp/report.md, which may exist on a developer machine.
//
// It is modelled on seedPending (same binding, same critical section) but
// appends the entry directly rather than through Queue: Queue prepends the
// origin line to Payload (deliver.go's WithOrigin), and these tests need the
// stored Payload to be exactly "round 1 report".
func seedPendingReport(t *testing.T, rt Runtime, name, path string, kind store.Kind) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:      name,
		CWD:       "/repo/" + name,
		Round:     1,
		State:     store.StateActive,
		Planner:   store.Endpoint{Kind: "claude", SessionID: "sess"},
		PlannerID: "pl_aaaaaaaabbbb",
		Builder:   store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToPlanner, Kind: kind,
			Payload: "round 1 report", Path: path, Confirmed: false,
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// TestPullPendingPrintsReportText: pullPending returns the pointer payload
// followed by a blank line and the report file's own text, so the planner
// needs no second read.
func TestPullPendingPrintsReportText(t *testing.T) {
	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "001-report.md")
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := pullPending(context.Background(), rt, "webshop", "wait")
	if err != nil {
		t.Fatalf("pullPending: %v", err)
	}
	if !found {
		t.Fatal("pullPending found nothing, want the queued report")
	}
	if !strings.HasPrefix(text, "round 1 report\n\n") {
		t.Errorf("pullPending text = %q, want it to start with the payload and a blank line", text)
	}
	if !strings.Contains(text, "line two") {
		t.Errorf("pullPending text = %q, want it to carry the report's own text", text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}
}

// TestPullPendingCapsReportText: a report larger than MaxPushBytes is cut back
// to the cap and names the `relevo show` command that prints the full text
// (§4.2).
func TestPullPendingCapsReportText(t *testing.T) {
	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "001-report.md")
	// Exactly MaxPushBytes + 4096 bytes of newline-terminated lines.
	if err := os.WriteFile(path, []byte(strings.Repeat("x\n", (MaxPushBytes+4096)/2)), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := pullPending(context.Background(), rt, "webshop", "wait")
	if err != nil {
		t.Fatalf("pullPending: %v", err)
	}
	if !found {
		t.Fatal("pullPending found nothing, want the queued report")
	}

	want := fmt.Sprintf("[truncated at %d KiB -- full text: relevo show webshop --round 1 --report]", MaxPushBytes/1024)
	if !strings.Contains(text, want) {
		t.Errorf("pullPending text does not carry the truncation tail %q:\n%s", want, text)
	}
	if len(text) >= MaxPushBytes+len("round 1 report")+200 {
		t.Errorf("len(text) = %d, want less than %d", len(text), MaxPushBytes+len("round 1 report")+200)
	}
}

// TestPullPendingUnreadableReportFallsBackToPayload: a missing report file is
// not an error and the entry stays delivered -- the payload still names the
// show command.
func TestPullPendingUnreadableReportFallsBackToPayload(t *testing.T) {
	rt := routeRuntime(t)
	path := filepath.Join(t.TempDir(), "missing-report.md")
	seedPendingReport(t, rt, "webshop", path, store.KindReport)

	text, found, err := pullPending(context.Background(), rt, "webshop", "wait")
	if err != nil {
		t.Fatalf("pullPending: %v", err)
	}
	if !found {
		t.Fatal("pullPending found nothing, want the queued report")
	}
	if text != "round 1 report" {
		t.Errorf("pullPending text = %q, want the bare payload", text)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}
}
