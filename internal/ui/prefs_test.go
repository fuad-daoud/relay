package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testPrefsStore is a real t.TempDir() database and a legacy ui.json path, so
// a preference round trip exercises the kv row (P3b plan §4.4, §7).
func testPrefsStore(t *testing.T) PrefsStore {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return PrefsStore{KV: d, Key: "ui", LegacyPath: filepath.Join(t.TempDir(), "ui.json")}
}

func TestPrefsRoundTrip(t *testing.T) {
	ps := testPrefsStore(t)
	if got := loadPrefs(ps); got != (prefs{}) {
		t.Errorf("missing record must load zero prefs, got %+v", got)
	}
	want := prefs{Sort: "name", Compact: true, RailCols: 42}
	if msg := savePrefs(ps, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(ps); got != want {
		t.Errorf("round trip: %+v", got)
	}

	// A corrupt legacy ui.json is read as zero prefs: an import that refuses it
	// leaves nothing to load.
	bad := testPrefsStore(t)
	if err := os.WriteFile(bad.LegacyPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(bad); got != (prefs{}) {
		t.Errorf("garbage must load zero prefs, got %+v", got)
	}

	// A zero PrefsStore (KV nil) is unscoped: nothing loads.
	if got := loadPrefs(PrefsStore{}); got != (prefs{}) {
		t.Errorf("zero PrefsStore must load zero prefs, got %+v", got)
	}
}

func TestApplyPrefs(t *testing.T) {
	m := Model{width: 140, height: 40, sort: true}
	m = m.applyPrefs(prefs{Sort: "name", Compact: true, RailCols: 40})
	if m.sort || !m.compact || m.railCols != 40 {
		t.Errorf("applied: sort %v compact %v rail %d", m.sort, m.compact, m.railCols)
	}
	m = m.applyPrefs(prefs{})
	if !m.sort || m.compact || m.railCols != railDefault {
		t.Errorf("zero prefs restore defaults: sort %v compact %v rail %d", m.sort, m.compact, m.railCols)
	}
	if p := m.prefs(); p != (prefs{Sort: "attention", Compact: false, RailCols: railDefault}) {
		t.Errorf("prefs() = %+v", p)
	}
}

func TestPrefsScopeRoundTrip(t *testing.T) {
	ps := testPrefsStore(t)
	want := prefs{Sort: "attention", Scope: "all"}
	if msg := savePrefs(ps, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(ps); got != want {
		t.Errorf("round trip: %+v, want %+v", got, want)
	}

	m := Model{}
	m = m.applyPrefs(want)
	if m.scope != scopeAll {
		t.Errorf("applyPrefs(Scope: all): scope = %v, want scopeAll", m.scope)
	}
	if p := m.prefs(); p.Scope != "all" {
		t.Errorf("prefs().Scope = %q, want %q", p.Scope, "all")
	}
}

func TestPrefsScopeEmptyIsLive(t *testing.T) {
	m := Model{scope: scopeAll}
	m = m.applyPrefs(prefs{})
	if m.scope != scopeLive {
		t.Errorf("applyPrefs(zero prefs): scope = %v, want scopeLive", m.scope)
	}
	if p := m.prefs(); p.Scope != "" {
		t.Errorf("prefs().Scope = %q, want empty (live)", p.Scope)
	}
}

func TestPrefsDashboardRoundTrip(t *testing.T) {
	ps := testPrefsStore(t)
	want := prefs{
		Sort:          "attention",
		RailCols:      railDefault,
		Dashboard:     "harness:agy since:30d",
		DashboardSort: "cost",
	}
	if msg := savePrefs(ps, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(ps); got != want {
		t.Errorf("round trip: %+v, want %+v", got, want)
	}

	m := Model{}
	m = m.applyPrefs(want)
	if m.dashQuery != "harness:agy since:30d" {
		t.Errorf("applyPrefs: dashQuery = %q", m.dashQuery)
	}
	if m.dashSort != "cost" {
		t.Errorf("applyPrefs: dashSort = %q", m.dashSort)
	}
	if p := m.prefs(); p.Dashboard != want.Dashboard || p.DashboardSort != want.DashboardSort {
		t.Errorf("prefs() = %+v, want the dashboard fields kept", p)
	}
}

func TestChangesSaveWhenAStoreIsSet(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.opts.Prefs = testPrefsStore(t)
	for _, r := range []rune{'s', 'c', '>'} {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd == nil {
			t.Errorf("%q must return a save command", r)
		}
	}
	m.opts.Prefs = PrefsStore{}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}); cmd != nil {
		t.Error("no store: no save command")
	}
}
