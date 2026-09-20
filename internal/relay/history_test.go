package relay

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
)

func TestHistoryLineColumns(t *testing.T) {
	commits := 2
	tree := "clean"
	cost := 0.42
	basisMeasured := "measured"
	basisEstimated := "estimated"
	basisUnknown := "unknown"
	candidate := "agy/antigravity/opus"

	base := db.RoundRow{
		BindingName:      "api-auth",
		Number:           3,
		StartedAt:        time.Date(2026, 9, 15, 14, 2, 0, 0, time.UTC),
		Outcome:          "reported",
		BuilderCandidate: &candidate,
		Commits:          &commits,
		Tree:             &tree,
		CostUSD:          &cost,
		CostBasis:        &basisMeasured,
		Archived:         true,
	}

	want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)"
	if got := HistoryLine(base, time.UTC); got != want {
		t.Errorf("HistoryLine(measured, archived) =\n%q\nwant\n%q", got, want)
	}

	t.Run("nil cost", func(t *testing.T) {
		r := base
		r.CostUSD = nil
		r.CostBasis = nil
		r.Archived = false
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  -"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(nil cost) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("estimated basis", func(t *testing.T) {
		r := base
		r.CostBasis = &basisEstimated
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  ~$0.42  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(estimated) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("unknown basis", func(t *testing.T) {
		r := base
		r.CostBasis = &basisUnknown
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  unknown  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(unknown basis) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("long name truncated", func(t *testing.T) {
		r := base
		r.BindingName = "a-very-long-binding-name-indeed"
		want := "2026-09-15 14:02  a-very-long…  r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(long name) =\n%q\nwant\n%q", got, want)
		}
	})
}

func TestFormatHistoryEmpty(t *testing.T) {
	if got := FormatHistory(nil, time.UTC); got != "no rounds\n" {
		t.Errorf("FormatHistory(nil) = %q, want %q", got, "no rounds\n")
	}
	if got := FormatHistory([]db.RoundRow{}, time.UTC); got != "no rounds\n" {
		t.Errorf("FormatHistory(empty) = %q, want %q", got, "no rounds\n")
	}
}

func TestHistoryOptionsFilterHere(t *testing.T) {
	g := &fakeGit{repoFactsOrigin: "git@github.com:o/r.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "https://github.com/o/r" {
		t.Errorf("Repo = %q, want the normalised origin", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
	if !f.Newest {
		t.Error("Newest = false, want true")
	}
}

func TestHistoryOptionsFilterHereNoRemote(t *testing.T) {
	g := &fakeGit{repoFactsCommonDir: "/work/repo/.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "/work/repo/.git" {
		t.Errorf("Repo = %q, want the common dir", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
}

func TestHistoryOptionsSinceUntil(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Since: "24h", Until: "2026-09-01"}

	f, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	wantSince := now.Add(-24 * time.Hour)
	if !f.Since.Equal(wantSince) {
		t.Errorf("Since = %v, want %v", f.Since, wantSince)
	}
	wantUntil := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !f.Until.Equal(wantUntil) {
		t.Errorf("Until = %v, want %v", f.Until, wantUntil)
	}
}
