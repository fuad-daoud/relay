package db

import (
	"errors"
	"testing"
	"time"
)

// TestUpsertPlannerByID pins §3.5's id-first write: relay's planner records
// carry their own id, so ingest upserts by that id -- inserting it first, then
// updating the columns a move can change.
func TestUpsertPlannerByID(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)

	const id = "pl_aaaaaaaaaaaa"
	got, err := d.UpsertPlanner(Planner{
		ID:                id,
		HarnessKind:       "claude",
		SessionID:         "sess-1",
		TranscriptLocator: ptr("/tmp/t1.jsonl"),
		FirstSeen:         t1,
		LastSeen:          t1,
	})
	if err != nil {
		t.Fatalf("UpsertPlanner (insert): %v", err)
	}
	if got != id {
		t.Fatalf("UpsertPlanner returned %q, want the record's own id %q", got, id)
	}

	// The same id, a moved session, no new transcript locator: the row keeps
	// its id and its locator and takes the new session and last_seen.
	got, err = d.UpsertPlanner(Planner{
		ID:          id,
		HarnessKind: "claude",
		SessionID:   "sess-2",
		LastSeen:    t2,
	})
	if err != nil {
		t.Fatalf("UpsertPlanner (update): %v", err)
	}
	if got != id {
		t.Fatalf("UpsertPlanner (update) returned %q, want %q", got, id)
	}

	p, ok, err := d.PlannerBySession("claude", "sess-2")
	if err != nil {
		t.Fatalf("PlannerBySession: %v", err)
	}
	if !ok {
		t.Fatal("PlannerBySession found no row for the moved session")
	}
	if p.ID != id || p.HarnessKind != "claude" {
		t.Errorf("row = %+v, want id %s kind claude", p, id)
	}
	if p.TranscriptLocator == nil || *p.TranscriptLocator != "/tmp/t1.jsonl" {
		t.Errorf("transcript_locator = %v, want the existing /tmp/t1.jsonl kept", p.TranscriptLocator)
	}
	if !p.LastSeen.Equal(t2) {
		t.Errorf("last_seen = %v, want %v", p.LastSeen, t2)
	}
	if !p.FirstSeen.Equal(t1) {
		t.Errorf("first_seen = %v, want %v (unchanged)", p.FirstSeen, t1)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM planner`).Scan(&count); err != nil {
		t.Fatalf("count planner: %v", err)
	}
	if count != 1 {
		t.Errorf("planner has %d rows, want 1", count)
	}
}

// TestUpsertPlannerByIDConflictingNaturalKey pins the guard §3.5 relies on: the
// natural-key unique index stays, so one (harness_kind, session_id) can never
// belong to two ids.
func TestUpsertPlannerByIDConflictingNaturalKey(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	// A row that predates the record ids: UpsertPlanner mints it.
	existing, err := d.UpsertPlanner(Planner{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t1, LastSeen: t1})
	if err != nil {
		t.Fatalf("UpsertPlanner: %v", err)
	}

	_, err = d.UpsertPlanner(Planner{
		ID:          "pl_aaaaaaaaaaaa",
		HarnessKind: "claude",
		SessionID:   "sess-1",
		LastSeen:    t1,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpsertPlanner by id onto a taken natural key = %v, want ErrInvalid", err)
	}

	p, ok, err := d.PlannerBySession("claude", "sess-1")
	if err != nil {
		t.Fatalf("PlannerBySession: %v", err)
	}
	if !ok || p.ID != existing {
		t.Errorf("row = %+v (ok %v), want the original id %s", p, ok, existing)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM planner`).Scan(&count); err != nil {
		t.Fatalf("count planner: %v", err)
	}
	if count != 1 {
		t.Errorf("planner has %d rows, want 1", count)
	}
}

func TestPlannerBySession(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)

	id, err := d.UpsertPlanner(Planner{
		HarnessKind:       "claude",
		SessionID:         "sess-1",
		TranscriptLocator: ptr("/tmp/t.jsonl"),
		FirstSeen:         t1,
		LastSeen:          t2,
	})
	if err != nil {
		t.Fatalf("UpsertPlanner: %v", err)
	}

	p, ok, err := d.PlannerBySession("claude", "sess-1")
	if err != nil {
		t.Fatalf("PlannerBySession: %v", err)
	}
	if !ok {
		t.Fatal("PlannerBySession reported no row")
	}
	if p.ID != id || p.HarnessKind != "claude" || p.SessionID != "sess-1" {
		t.Errorf("row = %+v", p)
	}
	if p.TranscriptLocator == nil || *p.TranscriptLocator != "/tmp/t.jsonl" {
		t.Errorf("transcript_locator = %v", p.TranscriptLocator)
	}
	if !p.FirstSeen.Equal(t1) || !p.LastSeen.Equal(t2) {
		t.Errorf("times = %v..%v, want %v..%v", p.FirstSeen, p.LastSeen, t1, t2)
	}

	// The kind is part of the key.
	if _, ok, err := d.PlannerBySession("opencode", "sess-1"); err != nil || ok {
		t.Errorf("PlannerBySession(other kind) = ok %v, err %v; want no row", ok, err)
	}
	// A miss is not an error.
	if _, ok, err := d.PlannerBySession("claude", "nope"); err != nil || ok {
		t.Errorf("PlannerBySession(unknown) = ok %v, err %v; want no row and no error", ok, err)
	}
}
