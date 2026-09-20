package ui

import (
	"encoding/json"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// prefs is what ui.json holds: the three things a human sets and would
// not want to set again next time (spec §6.0). The ui's own file, under
// relay's state root, written by the ui alone and read by nothing else.
type prefs struct {
	Sort     string `json:"sort"` // "attention" | "name"
	Compact  bool   `json:"compact"`
	RailCols int    `json:"rail_cols"`
	// Scope is the rail's breadth (#172): "all" or "" -- "" reads as
	// live, so a prefs file written before this round keeps opening live.
	Scope string `json:"scope,omitempty"`
}

type prefsSavedMsg struct{}

// loadPrefs reads path; any error -- missing, unreadable, not JSON -- is
// the zero prefs, which applyPrefs reads as the defaults. Never errors:
// a preference file is not worth refusing to start over.
func loadPrefs(path string) prefs {
	var p prefs
	data, err := os.ReadFile(path)
	if err != nil {
		return prefs{}
	}
	if json.Unmarshal(data, &p) != nil {
		return prefs{}
	}
	return p
}

// savePrefs writes p to path atomically (temp file in the same directory,
// then rename) from inside the command, so Update stays pure. A failed
// save is silent: the change still applies for this run.
func savePrefs(path string, p prefs) tea.Cmd {
	return func() tea.Msg {
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return prefsSavedMsg{}
		}
		dir := filepath.Dir(path)
		_ = os.MkdirAll(dir, 0o755)
		tmp, err := os.CreateTemp(dir, ".ui-*.json")
		if err != nil {
			return prefsSavedMsg{}
		}
		name := tmp.Name()
		if _, err := tmp.Write(append(data, '\n')); err != nil {
			tmp.Close()
			os.Remove(name)
			return prefsSavedMsg{}
		}
		if err := tmp.Close(); err != nil {
			os.Remove(name)
			return prefsSavedMsg{}
		}
		if err := os.Rename(name, path); err != nil {
			os.Remove(name)
		}
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
	return prefs{Sort: sort, Compact: m.compact, RailCols: m.railWidthStored(), Scope: scope}
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
	return m
}

// save is the command every preference change returns: the save when a
// path is configured, nil otherwise.
func (m Model) save() tea.Cmd {
	if m.opts.PrefsPath == "" {
		return nil
	}
	return savePrefs(m.opts.PrefsPath, m.prefs())
}
