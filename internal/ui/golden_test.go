package ui

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// goldenModel is splitModel widened to take a full relay.Report, so a
// fixture can carry Gated alongside its rows.
func goldenModel(t *testing.T, width, height int, rep relay.Report) Model {
	t.Helper()
	st := store.New(t.TempDir())
	fh := newFakeHerdr(t)
	m := newModel(context.Background(), relay.Runtime{Store: st, Herdr: fh}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	return res.(Model)
}

// allStatesRows covers every display state and the row facts the rail and
// pane can show today: NEEDS YOU (blocked, dirty, consults), HELD (hold
// clock), ACTIVE (pane builder, nudged), ACTIVE (headless, pid), DONE
// (--cwd, no branch).
func allStatesRows() []relay.BindingStatus {
	return []relay.BindingStatus{
		{
			Name: "webshop", Round: 4, Display: "NEEDS YOU",
			PlannerPane: "%1", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "%7", BuilderKind: "agy", BuilderStatus: "blocked", Branch: "relay/webshop",
			Dirty: true, Consults: 2,
			Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute), Hint: "relay answer --name webshop"},
			Last:    &relay.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
		},
		{
			Name: "ledger", Round: 3, Display: "HELD",
			PlannerPane: "%2", PlannerKind: "claude", PlannerStatus: "idle", PlannerFocus: true,
			BuilderPane: "%8", BuilderKind: "agy", BuilderStatus: "idle", Branch: "relay/ledger",
			Pending: &relay.PendingInfo{Round: 3, Kind: store.KindReport, Hold: &relay.HoldInfo{QuietMS: 23000, GraceMS: 60000}},
		},
		{
			Name: "api", Round: 2, Display: "ACTIVE",
			PlannerPane: "%3", PlannerKind: "claude", PlannerStatus: "working",
			BuilderPane: "%9", BuilderKind: "agy", BuilderStatus: "working", Branch: "relay/api",
			Nudge: &relay.NudgeInfo{At: railNow.Add(-time.Minute), QuietMS: 23000, GraceMS: 60000},
		},
		{
			Name: "worker", Round: 2, Display: "ACTIVE",
			PlannerPane: "%4", PlannerKind: "claude", PlannerStatus: "working",
			BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1", Branch: "relay/worker",
			Headless: &relay.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
		},
		{
			Name: "docs", Round: 1, Display: "DONE",
			PlannerPane: "%5", PlannerKind: "claude", PlannerStatus: "idle",
			BuilderPane: "%10", BuilderKind: "agy", BuilderStatus: "gone", CWD: "/home/x/docs",
			Last: &relay.LastEvent{TS: railNow.Add(-3 * time.Hour)},
		},
	}
}

func gatedGates() []ledger.Gate {
	return []ledger.Gate{
		{Token: "codex", Kind: ledger.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)},
	}
}

// feedTerminal switches the pane to the terminal tab and delivers a
// tabMsg reply for it, so a golden fixture shows content instead of
// "loading…".
func feedTerminal(t *testing.T, m Model, name, body string) Model {
	t.Helper()
	m.detail.active = tabTerminal
	res, _ := m.Update(tabMsg{name: name, t: tabTerminal, content: tabContent{loaded: true, body: body, at: railNow}})
	return res.(Model)
}

func TestGoldenViews(t *testing.T) {
	terminalBody := "$ go test ./...\nok  \tgithub.com/fuad-daoud/relay/internal/ui\t1.2s\n"

	cases := []struct {
		name          string
		width, height int
		build         func(t *testing.T) Model
	}{
		{
			name: "split-all-states", width: 140, height: 40,
			build: func(t *testing.T) Model {
				rows := allStatesRows()
				m := goldenModel(t, 140, 40, relay.Report{Bindings: rows, Gated: gatedGates()})
				return feedTerminal(t, m, m.detail.name, terminalBody)
			},
		},
		{
			name: "split-long-name", width: 140, height: 40,
			build: func(t *testing.T) Model {
				rows := []relay.BindingStatus{
					{Name: strings.Repeat("x", 40), Round: 1, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"},
				}
				m := goldenModel(t, 140, 40, relay.Report{Bindings: rows})
				return feedTerminal(t, m, m.detail.name, terminalBody)
			},
		},
		{
			name: "split-foreign", width: 140, height: 40,
			build: func(t *testing.T) Model {
				rows := []relay.BindingStatus{
					{
						Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working",
						Foreign: []relay.ForeignAgent{
							{PaneID: "%9", Kind: "claude", Status: "working", Title: "reviewer"},
							{PaneID: "%10", Kind: "opencode", Status: "idle", Title: "researcher"},
						},
					},
				}
				m := goldenModel(t, 140, 40, relay.Report{Bindings: rows})
				return feedTerminal(t, m, m.detail.name, terminalBody)
			},
		},
		{
			name: "split-empty", width: 140, height: 40,
			build: func(t *testing.T) Model {
				return goldenModel(t, 140, 40, relay.Report{})
			},
		},
		{
			name: "split-error-before-load", width: 140, height: 40,
			build: func(t *testing.T) Model {
				st := store.New(t.TempDir())
				fh := newFakeHerdr(t)
				m := newModel(context.Background(), relay.Runtime{Store: st, Herdr: fh}, Options{Interval: time.Second})
				m.now = func() time.Time { return railNow }
				res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
				m = res.(Model)
				m.statusInFlight = false
				res, _ = m.Update(statusMsg{err: errors.New("herdr connection refused")})
				return res.(Model)
			},
		},
		{
			name: "stack-all-states", width: 80, height: 30,
			build: func(t *testing.T) Model {
				rows := allStatesRows()
				return goldenModel(t, 80, 30, relay.Report{Bindings: rows, Gated: gatedGates()})
			},
		},
		{
			name: "stack-detail", width: 80, height: 30,
			build: func(t *testing.T) Model {
				rows := allStatesRows()
				m := goldenModel(t, 80, 30, relay.Report{Bindings: rows, Gated: gatedGates()})
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m = res.(Model)
				return feedTerminal(t, m, m.detail.name, terminalBody)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			got := stripANSI(m.View())

			path := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("%s: golden mismatch\ngot:\n%s\nwant:\n%s", tc.name, got, string(want))
			}

			lines := strings.Split(got, "\n")
			if len(lines) != tc.height {
				t.Errorf("%s: %d lines, want %d (height)", tc.name, len(lines), tc.height)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > tc.width {
					t.Errorf("%s: line %d is %d wide, want <= %d: %q", tc.name, i, w, tc.width, l)
				}
			}
		})
	}
}
