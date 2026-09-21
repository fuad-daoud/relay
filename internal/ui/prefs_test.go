package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPrefsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.json")
	if got := loadPrefs(path); got != (prefs{}) {
		t.Errorf("missing file must load zero prefs, got %+v", got)
	}
	want := prefs{Sort: "name", Compact: true, RailCols: 42}
	if msg := savePrefs(path, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(path); got != want {
		t.Errorf("round trip: %+v", got)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(path); got != (prefs{}) {
		t.Errorf("garbage must load zero prefs, got %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("save must leave no temp file behind: %v", entries)
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
	path := filepath.Join(t.TempDir(), "ui.json")
	want := prefs{Sort: "attention", Scope: "all"}
	if msg := savePrefs(path, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(path); got != want {
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
	path := filepath.Join(t.TempDir(), "ui.json")
	want := prefs{
		Sort:          "attention",
		RailCols:      railDefault,
		Dashboard:     "harness:agy since:30d",
		DashboardSort: "cost",
	}
	if msg := savePrefs(path, want)(); msg != (prefsSavedMsg{}) {
		t.Errorf("save returned %v", msg)
	}
	if got := loadPrefs(path); got != want {
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

func TestChangesSaveWhenAPathIsSet(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.opts.PrefsPath = filepath.Join(t.TempDir(), "ui.json")
	for _, r := range []rune{'s', 'c', '>'} {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd == nil {
			t.Errorf("%q must return a save command", r)
		}
	}
	m.opts.PrefsPath = ""
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}); cmd != nil {
		t.Error("no path: no save command")
	}
}
