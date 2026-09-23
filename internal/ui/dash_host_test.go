package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
)

// keyD is the `d` key, which enters and leaves the dashboard screen.
func keyD() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}} }

// dashHostModel is splitModel with a database behind the source, so `d` and
// --dashboard can open the dashboard screen. The db is empty: what these
// tests read is the host's wiring, not the rows.
func dashHostModel(t *testing.T, width, height int, opts Options, rows ...relevo.BindingStatus) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st, DB: d}
	opts.Interval = time.Second
	m := newModel(context.Background(), plannerSource{rt}, opts)
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relevo.Report{Bindings: rows}})
	return res.(Model)
}

// drain runs cmds through the model, unwrapping tea.BatchMsg as the
// bubbletea loop does, until nothing is left. Commands the model returns
// are drained too, so a save and its prefsSavedMsg do not leak.
func drain(t *testing.T, m Model, cmds ...tea.Cmd) Model {
	t.Helper()
	queue := append([]tea.Cmd(nil), cmds...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(append([]tea.Cmd(nil), b...), queue...)
			continue
		}
		res, next := m.Update(msg)
		m = res.(Model)
		if next != nil {
			queue = append(queue, next)
		}
	}
	return m
}

func TestKeyDEntersAndLeavesDash(t *testing.T) {
	m := dashHostModel(t, 140, 40, Options{}, threeRows()...)

	res, cmd := m.Update(keyD())
	m = res.(Model)
	if m.screen != screenDash {
		t.Fatalf("d: screen = %v, want screenDash", m.screen)
	}
	if !m.dashSet {
		t.Fatal("d: the dashboard model was not built")
	}
	if cmd == nil {
		t.Fatal("d: no first fetch was issued")
	}
	m = drain(t, m, cmd)
	if m.dash.QueryText() != "" {
		t.Errorf("fresh dashboard query text = %q, want empty", m.dash.QueryText())
	}

	res, _ = m.Update(keyD())
	m = res.(Model)
	if m.screen != screenList {
		t.Fatalf("second d: screen = %v, want screenList", m.screen)
	}

	// esc leaves too, and the model (and its query) is kept.
	res, _ = m.Update(keyD())
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = res.(Model)
	if m.screen != screenList {
		t.Fatalf("esc: screen = %v, want screenList", m.screen)
	}
	if !m.dashSet {
		t.Error("esc must not throw the dashboard away")
	}
}

func TestKeyDWithoutDBNotices(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	res, _ := m.Update(keyD())
	m = res.(Model)
	if m.screen != screenList {
		t.Errorf("no database: screen = %v, want the fleet screen", m.screen)
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("no database: notice = %q, want the `a` notice", m.notice)
	}
	if m.dashSet {
		t.Error("no database: the dashboard model must not be built")
	}
}

func TestJumpFromDashPointsDetail(t *testing.T) {
	rows := []relevo.BindingStatus{
		{Name: "persist", Round: 3, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"},
	}
	m := dashHostModel(t, 140, 40, Options{}, rows...)
	// The rail is in scope all, as it must be for a hist-only binding to be
	// visible at all: oldapi is in the database, not the live report.
	m.scope = scopeAll
	m.dbRows = []relevo.HistoryBinding{
		{Name: "oldapi", ID: "h1", Rounds: 4, LastActivity: railNow.Add(-48 * time.Hour)},
	}

	res, _ := m.Update(dash.JumpMsg{BindingName: "oldapi", BindingID: "h1", Round: 2})
	m = res.(Model)
	if m.scope != scopeAll {
		t.Errorf("jump to a hist-only binding: scope = %v, want scopeAll", m.scope)
	}
	if m.detail.name != "oldapi" {
		t.Errorf("detail.name = %q, want oldapi", m.detail.name)
	}
	if m.detail.round != 2 {
		t.Errorf("detail.round = %d, want 2", m.detail.round)
	}
	if m.detail.live {
		t.Error("detail.live = true for a hist-only binding")
	}
	if m.screen != screenList {
		t.Errorf("screen = %v, want screenList in split layout", m.screen)
	}

	// A jump to the live binding keeps the scope it was on.
	res, _ = m.Update(dash.JumpMsg{BindingName: "persist", Round: 1})
	m = res.(Model)
	if m.scope != scopeAll {
		t.Errorf("jump to a live binding: scope = %v, want it kept (all)", m.scope)
	}
	if m.detail.name != "persist" {
		t.Errorf("detail.name = %q, want persist", m.detail.name)
	}
	if m.detail.round != 1 {
		t.Errorf("detail.round = %d, want 1", m.detail.round)
	}

	// A name in neither list is a notice and no screen change: empty the
	// database's rows and ask for a stranger.
	res, _ = m.Update(dash.JumpMsg{BindingName: "ghost", Round: 1})
	m = res.(Model)
	if !strings.Contains(m.notice, "ghost is not in the fleet or the database") {
		t.Errorf("notice = %q, want the not-in-the-fleet text", m.notice)
	}
}

// TestJumpFromDashTurnsScopeAll proves the live-scope half: a hist-only
// binding jumps only if scope all is turned on first, exactly as `a` does.
func TestJumpFromDashTurnsScopeAll(t *testing.T) {
	rows := []relevo.BindingStatus{
		{Name: "persist", Round: 3, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"},
	}
	m := dashHostModel(t, 140, 40, Options{}, rows...)
	if m.scope != scopeLive {
		t.Fatalf("setup: scope = %v, want live", m.scope)
	}
	m.dbRows = []relevo.HistoryBinding{
		{Name: "oldapi", ID: "h1", Rounds: 4, LastActivity: railNow.Add(-48 * time.Hour)},
	}

	res, _ := m.Update(dash.JumpMsg{BindingName: "oldapi", BindingID: "h1", Round: 2})
	m = res.(Model)
	if m.scope != scopeAll {
		t.Fatalf("scope = %v, want scopeAll after a jump to a hist-only binding", m.scope)
	}
	if !m.statusInFlight {
		t.Error("turning scope all on must refresh the rail")
	}
	if m.detail.name != "oldapi" || m.detail.round != 2 {
		t.Errorf("detail = %s r%d, want oldapi r2", m.detail.name, m.detail.round)
	}
}

func TestOptionsDashboardStartsOnDash(t *testing.T) {
	m := dashHostModel(t, 140, 40, Options{Dashboard: true}, threeRows()...)
	if m.screen != screenDash {
		t.Fatalf("--dashboard: screen = %v, want screenDash after the first statusMsg", m.screen)
	}
	if !m.dashSet {
		t.Fatal("--dashboard: the dashboard model was not built")
	}
}

// TestOptionsDashboardWithoutDBNotices: --dashboard with no database is the
// same refusal `d` shows, and the fleet screen stays.
func TestOptionsDashboardWithoutDBNotices(t *testing.T) {
	st := store.New(t.TempDir())
	m := newModel(context.Background(), plannerSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Dashboard: true})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: relevo.Report{Bindings: threeRows()}})
	m = res.(Model)
	if m.screen != screenList {
		t.Errorf("--dashboard with no database: screen = %v, want the fleet screen", m.screen)
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want the no-database text", m.notice)
	}
}

// TestDashSavePrefsOnChange: a query and a sort change on the dashboard are
// persisted on the same path Scope is.
func TestDashSavePrefsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")
	m := dashHostModel(t, 140, 40, Options{PrefsPath: path}, threeRows()...)
	res, cmd := m.Update(keyD())
	m = res.(Model)
	m = drain(t, m, cmd)

	// Sort: s changes the screen's sort key, and the keypress must save.
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = res.(Model)
	if m.dashSort == "" {
		t.Fatal("s did not record a sort key")
	}
	m = drain(t, m, cmd)
	if got := loadPrefs(path).DashboardSort; got != m.dashSort {
		t.Errorf("saved DashboardSort = %q, want %q", got, m.dashSort)
	}

	// Query: / then text then enter.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("harness:agy")})
	m = res.(Model)
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, m, cmd)
	if got := loadPrefs(path).Dashboard; got != "harness:agy" {
		t.Errorf("saved Dashboard = %q, want harness:agy", got)
	}
}
