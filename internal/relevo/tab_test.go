package relevo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

var tabNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func tabEntry(binding string, age time.Duration, kind store.Kind, provider, model string, usd float64, basis usage.Basis) TabEntry {
	return TabEntry{Binding: binding, Entry: store.LogEntry{
		TS: tabNow.Add(-age), Round: 1, Direction: store.DirToPlanner, Kind: kind,
		Usage: &usage.Usage{Harness: "h", Provider: provider, Model: model, Tokens: usage.Tokens{In: 1000, CacheRead: 3000, Out: 100},
			Cost: usage.Cost{USD: usd, Basis: basis}, Samples: 1},
	}}
}

func tabFixture() []TabEntry {
	return []TabEntry{
		tabEntry("api", 1*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.50, usage.Measured),
		tabEntry("api", 2*time.Hour, store.KindFindings, "anthropic", "claude-opus-5", 0.10, usage.Estimated),
		tabEntry("api", 10*24*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.20, usage.Measured),
		tabEntry("web", 3*time.Hour, store.KindReport, "google", "gemini-3.8-flash-high", 0, usage.Unknown),
		{Binding: "web", Entry: store.LogEntry{TS: tabNow, Kind: store.KindReport}},                      // no usage: ignored
		{Binding: "web", Entry: store.LogEntry{TS: tabNow, Kind: store.KindDiff, Usage: &usage.Usage{}}}, // wrong kind: ignored
	}
}

func TestParseSince(t *testing.T) {
	if got, err := ParseSince("", tabNow); err != nil || !got.IsZero() {
		t.Errorf("empty: %v, %v", got, err)
	}
	if got, _ := ParseSince("24h", tabNow); !got.Equal(tabNow.Add(-24 * time.Hour)) {
		t.Errorf("24h = %v", got)
	}
	if got, _ := ParseSince("7d", tabNow); !got.Equal(tabNow.Add(-7 * 24 * time.Hour)) {
		t.Errorf("7d = %v", got)
	}
	if got, _ := ParseSince("2026-09-01", tabNow); !got.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", got)
	}
	for _, bad := range []string{"7", "7w", "yesterday", "2026-9-1"} {
		if _, err := ParseSince(bad, tabNow); !errors.Is(err, ErrBadSince) {
			t.Errorf("%q: err = %v, want ErrBadSince", bad, err)
		}
	}
}

// TestParseSinceMovedKeepsRelevoWrapper pins that relevo.ParseSince still
// exists after its body moved to internal/histq, that the two agree, and
// that ErrBadSince is the same sentinel histq returns, so a caller's
// errors.Is(err, relevo.ErrBadSince) keeps working.
func TestParseSinceMovedKeepsRelevoWrapper(t *testing.T) {
	for _, s := range []string{"", "24h", "7d", "2026-09-01"} {
		got, err := ParseSince(s, tabNow)
		if err != nil {
			t.Fatalf("relevo.ParseSince(%q): %v", s, err)
		}
		want, err := histq.ParseSince(s, tabNow)
		if err != nil {
			t.Fatalf("histq.ParseSince(%q): %v", s, err)
		}
		if !got.Equal(want) {
			t.Errorf("relevo.ParseSince(%q) = %v, want %v (histq.ParseSince)", s, got, want)
		}
	}

	_, err := ParseSince("yesterday", tabNow)
	if !errors.Is(err, ErrBadSince) {
		t.Errorf("relevo.ParseSince error = %v, want ErrBadSince", err)
	}
	if !errors.Is(err, histq.ErrBadSince) {
		t.Errorf("relevo.ParseSince error = %v, want histq.ErrBadSince", err)
	}
	if ErrBadSince != histq.ErrBadSince {
		t.Error("relevo.ErrBadSince is not histq.ErrBadSince; errors.Is across the move would break")
	}
}

func TestTabRowsByBinding(t *testing.T) {
	rows, total, err := TabRows(tabFixture(), "binding", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Group != "api" || rows[1].Group != "web" {
		t.Fatalf("rows = %+v", rows)
	}
	api := rows[0].Spend
	if api.Rounds != 2 || api.Consults != 1 || api.Measured < 0.699 || api.Measured > 0.701 || api.Estimated != 0.10 {
		t.Errorf("api = %+v", api)
	}
	if rows[1].Spend.Unknown != 1 || rows[1].Spend.Rounds != 1 {
		t.Errorf("web = %+v", rows[1].Spend)
	}
	if total.Rounds != 3 || total.Consults != 1 || total.Unknown != 1 {
		t.Errorf("total = %+v", total)
	}
}

func TestTabRowsSince(t *testing.T) {
	rows, total, _ := TabRows(tabFixture(), "binding", tabNow.Add(-7*24*time.Hour))
	if rows[0].Spend.Rounds != 1 || rows[0].Spend.Measured != 0.50 {
		t.Errorf("the 10-day-old round must be cut: %+v", rows[0].Spend)
	}
	if total.Rounds != 2 {
		t.Errorf("total = %+v", total)
	}
}

func TestTabRowsByModelAndProvider(t *testing.T) {
	rows, _, _ := TabRows(tabFixture(), "model", time.Time{})
	want := []string{"anthropic/claude-opus-5", "anthropic/claude-sonnet-5", "google/gemini-3.8-flash-high"}
	for i, w := range want {
		if i >= len(rows) || rows[i].Group != w {
			t.Fatalf("model rows = %+v, want %v", rows, want)
		}
	}
	rows, _, _ = TabRows(tabFixture(), "provider", time.Time{})
	if len(rows) != 2 || rows[0].Group != "anthropic" || rows[1].Group != "google" {
		t.Errorf("provider rows = %+v", rows)
	}
	if _, _, err := TabRows(nil, "day", time.Time{}); !errors.Is(err, ErrBadBy) {
		t.Errorf("bad --by: %v", err)
	}
}

// TestTabRowsByOwner groups by the entry's Owner label: the server-side
// `relevo serve tab --by owner` grouping (#216). The client refuses the flag
// (cmdTab); this pins relevo's half, and the ErrBadBy text naming owner.
func TestTabRowsByOwner(t *testing.T) {
	e1 := tabEntry("api", 1*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.50, usage.Measured)
	e1.Owner = "alice"
	e2 := tabEntry("api", 2*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.25, usage.Measured)
	e2.Owner = "alice"
	e3 := tabEntry("web", 3*time.Hour, store.KindReport, "google", "gemini-3.8-flash-high", 0, usage.Unknown)
	e3.Owner = "bob"

	rows, total, err := TabRows([]TabEntry{e1, e2, e3}, "owner", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Group != "alice" || rows[1].Group != "bob" {
		t.Fatalf("owner rows = %+v, want alice then bob", rows)
	}
	if rows[0].Spend.Rounds != 2 || rows[0].Spend.Measured != 0.75 {
		t.Errorf("alice = %+v", rows[0].Spend)
	}
	if total.Rounds != 3 {
		t.Errorf("total = %+v", total)
	}
	if _, _, err := TabRows(nil, "day", time.Time{}); !errors.Is(err, ErrBadBy) || !strings.Contains(err.Error(), "owner") {
		t.Errorf("bad --by: %v, want ErrBadBy naming owner", err)
	}
}

func TestTabRowsUnknownModelGroup(t *testing.T) {
	e := TabEntry{Binding: "x", Entry: store.LogEntry{TS: tabNow, Kind: store.KindReport, Usage: &usage.Usage{Cost: usage.Cost{Basis: usage.Unknown}}}}
	rows, _, _ := TabRows([]TabEntry{e}, "model", time.Time{})
	if len(rows) != 1 || rows[0].Group != "unknown" {
		t.Errorf("empty provider/model must group as unknown: %+v", rows)
	}
}

// TestModelSansEffort pins the cut: a "#" effort suffix is removed, a
// ":effort" suffix stays, and an empty model is empty.
func TestModelSansEffort(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"m#high", "m"},
		{"gpt-5.6-terra:high", "gpt-5.6-terra:high"},
		{"a/b#x#y", "a/b"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := modelSansEffort(tt.model); got != tt.want {
			t.Errorf("modelSansEffort(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// TestTabRowsOneKeyPerModel pins bug 2's fix: the same model with and
// without its "#" effort suffix is one row, with both entries summed.
func TestTabRowsOneKeyPerModel(t *testing.T) {
	entries := []TabEntry{
		tabEntry("api", 1*time.Hour, store.KindReport, "cline-pass", "cline-pass/deepseek-v4.1-flash#high", 0.50, usage.Measured),
		tabEntry("api", 2*time.Hour, store.KindReport, "cline-pass", "cline-pass/deepseek-v4.1-flash", 0.25, usage.Measured),
	}

	rows, total, err := TabRows(entries, "model", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one", rows)
	}
	if rows[0].Group != "cline-pass/cline-pass/deepseek-v4.1-flash" {
		t.Errorf("group = %q, want the effort suffix cut", rows[0].Group)
	}
	if rows[0].Spend.Rounds != 2 || rows[0].Spend.Measured != 0.75 {
		t.Errorf("spend = %+v, want both entries summed", rows[0].Spend)
	}
	if total.Rounds != 2 || total.Measured != 0.75 {
		t.Errorf("total = %+v, want both entries summed", total)
	}
}

func TestRenderTab(t *testing.T) {
	rows, total, _ := TabRows(tabFixture(), "binding", time.Time{})
	out := RenderTab(TabReport{By: "binding", Rows: rows, Total: total})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 2 rows + total, got:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "group") || !strings.Contains(lines[0], "measured") || !strings.Contains(lines[0], "unknown") {
		t.Errorf("header = %q", lines[0])
	}
	api, web, tot := lines[1], lines[2], lines[3]
	if !strings.Contains(api, "$0.70") || !strings.Contains(api, "~$0.10") {
		t.Errorf("api row = %q", api)
	}
	if strings.Contains(web, "$") {
		t.Errorf("an all-unknown group must show no dollars: %q", web)
	}
	if !strings.HasPrefix(tot, "total") || !strings.Contains(tot, "$0.70") {
		t.Errorf("total row = %q", tot)
	}
	if !strings.Contains(api, "3k") || !strings.Contains(api, "9k") { // 3 entries × in 1000, cache read 3000
		t.Errorf("api token cells = %q", api)
	}
	empty := RenderTab(TabReport{By: "binding"})
	if !strings.Contains(empty, "no rounds with usage") {
		t.Errorf("empty = %q", empty)
	}
}

// TestRenderTabColumns pins the four token columns (#234): in, cache,
// write and out, each the group's summed field in short form, no
// percentage.
func TestRenderTabColumns(t *testing.T) {
	e := TabEntry{Binding: "web", Entry: store.LogEntry{TS: tabNow, Round: 1,
		Direction: store.DirToPlanner, Kind: store.KindReport,
		Usage: &usage.Usage{Harness: "h", Provider: "p", Model: "m",
			Tokens: usage.Tokens{In: 2100, CacheRead: 166_000, CacheWrite: 14_000, Out: 12_000}, Samples: 1}}}
	rows, total, err := TabRows([]TabEntry{e}, "binding", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderTab(TabReport{By: "binding", Rows: rows, Total: total})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "write") {
		t.Errorf("header = %q, want the write column", lines[0])
	}
	row := lines[1]
	cells := []string{"2k", "166k", "14k", "12k"}
	last := -1
	for _, c := range cells {
		i := strings.Index(row, c)
		if i < 0 || i <= last {
			t.Errorf("row = %q, want %v in that order", row, cells)
			break
		}
		last = i
	}
	if strings.Contains(row, "%") {
		t.Errorf("row = %q, want no percentage cell", row)
	}
}

// TestRenderTabStepColumns pins the two step columns (#323, #324), right
// after rounds: the summed step count and calls per step. A group that
// recorded no steps leaves both cells empty; the other expectations --
// rounds, the token cells, money -- are untouched.
func TestRenderTabStepColumns(t *testing.T) {
	entries := []TabEntry{
		{Binding: "a", Entry: store.LogEntry{TS: tabNow, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
			Usage: &usage.Usage{Harness: "h", Provider: "p", Model: "m", Samples: 1, Steps: 10, ToolCalls: 15}}},
		{Binding: "b", Entry: store.LogEntry{TS: tabNow, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
			Usage: &usage.Usage{Harness: "h", Provider: "p", Model: "m", Samples: 1}}},
	}
	rows, total, err := TabRows(entries, "binding", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderTab(TabReport{By: "binding", Rows: rows, Total: total})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "steps") || !strings.Contains(lines[0], "calls/st") {
		t.Errorf("header = %q, want the steps and calls/st columns", lines[0])
	}
	// The group column is as wide as "group", then two spaces and the six
	// characters of the rounds cell, then two spaces: the six after that
	// are the steps cell.
	stepsCell := func(line string) string {
		at := len("group") + 2 + 6 + 2
		return strings.TrimSpace(line[at : at+6])
	}
	if got := stepsCell(lines[1]); got != "10" {
		t.Errorf("a row steps cell = %q, want 10", got)
	}
	if !strings.Contains(lines[1], "1.50") {
		t.Errorf("a row = %q, want calls/st 1.50", lines[1])
	}
	if got := stepsCell(lines[2]); got != "" {
		t.Errorf("b row steps cell = %q, want blank", got)
	}
	if strings.Contains(lines[2], "1.50") {
		t.Errorf("b row = %q, want no calls/st cell", lines[2])
	}
	if got := stepsCell(lines[3]); got != "10" || !strings.Contains(lines[3], "1.50") {
		t.Errorf("total row = %q, want steps 10 and calls/st 1.50", lines[3])
	}
}

// TestTabEntriesLiveAndArchived pins the gather half `relevo tab` and
// `relevo serve tab` share: a live binding's log and an archived binding's log
// both contribute entries, an unreadable archive is a warning rather than a
// failure, and a cut newer than the archive's own stamp skips the archive
// whole. Store-only: no harness, no network.
func TestTabEntriesLiveAndArchived(t *testing.T) {
	s := store.New(t.TempDir())

	if err := s.Save(store.Binding{Name: "live", CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save live: %v", err)
	}
	if err := s.AppendLog("live", store.LogEntry{TS: tabNow, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Usage: &usage.Usage{Harness: "h", Provider: "anthropic", Model: "claude-sonnet-5", Samples: 1}}); err != nil {
		t.Fatalf("AppendLog live: %v", err)
	}

	if err := s.Save(store.Binding{Name: "old", CWD: t.TempDir(), Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save old: %v", err)
	}
	if err := s.AppendLog("old", store.LogEntry{TS: tabNow, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Usage: &usage.Usage{Harness: "h", Provider: "google", Model: "gemini-3.8-flash-high", Samples: 1}}); err != nil {
		t.Fatalf("AppendLog old: %v", err)
	}
	if _, err := s.Archive("old"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	rt := Runtime{Store: s, Now: func() time.Time { return tabNow }}
	var warnings []string
	warn := func(msg string) { warnings = append(warnings, msg) }

	entries, err := TabEntries(rt, time.Time{}, warn)
	if err != nil {
		t.Fatalf("TabEntries: %v", err)
	}
	got := map[string]int{}
	for _, e := range entries {
		got[e.Binding]++
	}
	if got["live"] != 1 || got["old"] != 1 {
		t.Errorf("entries by binding = %v, want one each for live and old", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	archives, err := s.ListArchives()
	if err != nil || len(archives) != 1 {
		t.Fatalf("ListArchives = %v, %v, want exactly one archive", archives, err)
	}
	// A cut past the archive's stamp skips it: every entry in it predates
	// the archive itself.
	entries, err = TabEntries(rt, archives[0].At.Add(time.Second), warn)
	if err != nil {
		t.Fatalf("TabEntries with cut: %v", err)
	}
	if len(entries) != 1 || entries[0].Binding != "live" {
		t.Errorf("entries with a cut past the archive = %+v, want only live's", entries)
	}
}
