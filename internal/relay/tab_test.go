package relay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
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

func TestTabRowsUnknownModelGroup(t *testing.T) {
	e := TabEntry{Binding: "x", Entry: store.LogEntry{TS: tabNow, Kind: store.KindReport, Usage: &usage.Usage{Cost: usage.Cost{Basis: usage.Unknown}}}}
	rows, _, _ := TabRows([]TabEntry{e}, "model", time.Time{})
	if len(rows) != 1 || rows[0].Group != "unknown" {
		t.Errorf("empty provider/model must group as unknown: %+v", rows)
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
	if !strings.Contains(api, "12k") { // 3 entries × 4000 prompt tokens
		t.Errorf("api in-tokens = %q", api)
	}
	empty := RenderTab(TabReport{By: "binding"})
	if !strings.Contains(empty, "no rounds with usage") {
		t.Errorf("empty = %q", empty)
	}
}
