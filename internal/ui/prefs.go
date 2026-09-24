package ui

import (
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fuad-daoud/relevo/internal/db"
)

// prefs is what the ui's preference document holds: the three things a human
// sets and would not want to set again next time (spec §6.0). It lives in the
// machine database's kv row "ui" (or "serve.ui" for the server ui) -- the file
// ui.json before this round -- written by the ui alone and read by nothing else.
type prefs struct {
	Sort     string `json:"sort"` // "attention" | "name"
	Compact  bool   `json:"compact"`
	RailCols int    `json:"rail_cols"`
	// Scope is the rail's breadth (#172): "all" or "" -- "" reads as
	// live, so a prefs file written before this round keeps opening live.
	Scope string `json:"scope,omitempty"`
	// Dashboard is the dashboard screen's applied query text and its sort
	// column (docs/specs/2026-09-21-dashboard-design.md §6). Empty means
	// the defaults: every round, newest first.
	Dashboard     string `json:"dashboard,omitempty"`
	DashboardSort string `json:"dashboard_sort,omitempty"`
}

// PrefsStore is where the ui's preferences live (P3b plan §4.4): the kv row Key
// in KV, with LegacyPath (ui.json) imported on the first read. A zero
// PrefsStore -- KV nil -- keeps the ui stateless, nothing loaded, nothing saved,
// exactly as an empty Options.PrefsPath did.
type PrefsStore struct {
	KV         db.KV
	Key        string
	LegacyPath string
}

type prefsSavedMsg struct{}

// loadPrefs reads the KV row; any error -- missing, unreadable, not JSON -- is
// the zero prefs, which applyPrefs reads as the defaults. Never errors: a
// preference record is not worth refusing to start over.
func loadPrefs(ps PrefsStore) prefs {
	if ps.KV == nil {
		return prefs{}
	}
	data, ok, err := db.KVImportFile(ps.KV, ps.Key, ps.LegacyPath)
	if err != nil || !ok {
		return prefs{}
	}
	var p prefs
	if json.Unmarshal(data, &p) != nil {
		return prefs{}
	}
	return p
}

// savePrefs writes p to the KV row from inside the command, so Update stays
// pure. A failed save is silent: the change still applies for this run.
func savePrefs(ps PrefsStore, p prefs) tea.Cmd {
	return func() tea.Msg {
		if ps.KV == nil {
			return prefsSavedMsg{}
		}
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return prefsSavedMsg{}
		}
		_ = ps.KV.KVPut(ps.Key, append(data, '\n'))
		return prefsSavedMsg{}
	}
}

// prefs is the model's current preferences, as saved.
func (m Model) prefs() prefs {
	sort := "attention"
	if !m.sort {
		sort = "name"
	}
	scope := ""
	if m.scope == scopeAll {
		scope = "all"
	}
	return prefs{Sort: sort, Compact: m.compact, RailCols: m.railWidthStored(), Scope: scope,
		Dashboard: m.dashQuery, DashboardSort: m.dashSort}
}

// applyPrefs sets the model from p; zero values mean the defaults.
func (m Model) applyPrefs(p prefs) Model {
	m.sort = p.Sort != "name"
	m.compact = p.Compact
	m.railCols = railDefault
	if p.RailCols > 0 {
		m.railCols = p.RailCols
	}
	m.scope = scopeLive
	if p.Scope == "all" {
		m.scope = scopeAll
	}
	m.dashQuery = p.Dashboard
	m.dashSort = p.DashboardSort
	return m
}

// save is the command every preference change returns: the save when a store
// is configured, nil otherwise.
func (m Model) save() tea.Cmd {
	if m.opts.Prefs.KV == nil {
		return nil
	}
	return savePrefs(m.opts.Prefs, m.prefs())
}
