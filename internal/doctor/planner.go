package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/release"
)

// The relevo Claude Code plugin's install facts, resolved through $HOME so
// cmd/relevo's TestMain isolation holds.
const (
	claudeSettingsRel     = ".claude/settings.json"
	claudePluginStateRel  = ".claude/plugins/installed_plugins.json"
	claudePluginName      = "relevo@relevo"
	claudeHooksRel        = "hooks/hooks.json"
	pluginHookInitCommand = "planner init"
	pluginHookEvent       = "SessionStart"
)

// ChildProcess is one entry of a host process's child table: the pid and its
// argv, as /proc/<pid>/task/*/children plus /proc/<pid>/cmdline report them.
type ChildProcess struct {
	PID  int
	Args []string
}

// HasMCPChild reports whether any of a host process's children is a `relevo
// mcp` process. Pure, so it is testable without a live process tree.
func HasMCPChild(children []ChildProcess) bool {
	for _, c := range children {
		if len(c.Args) == 0 {
			continue
		}
		if base := filepath.Base(c.Args[0]); base != "relevo" && base != "relevo.exe" {
			continue
		}
		for _, a := range c.Args[1:] {
			if a == "mcp" {
				return true
			}
		}
	}
	return false
}

// PlannerCheckInput is everything the planner rows need that doctor.Run
// cannot read itself. cmd/relevo gathers it; the rules live here.
type PlannerCheckInput struct {
	Claude bool   // a claude candidate or planner record exists; gates the plugin rows
	Home   string // $HOME, so a test can point it at a temp dir
	Repo   string // the repository `relevo doctor` runs in

	Detected  bool            // planner.Detect: relevo runs inside a Claude Code session
	Resolved  *planner.Record // the planner Resolve found; nil on a miss
	Chat      string          // the resolved planner's chatlabel, "" when Resolved is nil
	ClaimLive bool            // a live channel claim exists for Resolved
	MCPChild  bool            // a `relevo mcp` process is a child of the planner's host; false is FAIL

	Stale   []string // planner records seen over 7 days ago with no live binding
	Running string   // relevo's own version, compared with the installed plugin's; "" skips it
}

// PlannerChecks reports the plugin rows for Claude Code, the installed
// plugin's SessionStart hook, this session's planner, and the stale-record
// note. Leaving every field zero reports nothing.
func PlannerChecks(in PlannerCheckInput) []Check {
	var checks []Check

	if in.Claude {
		checks = append(checks, pluginEnabledCheck(in.Home, in.Repo))
		checks = append(checks, pluginHookCheck(in.Home))
		checks = append(checks, pluginVersionCheck(in.Home, in.Running))
	}

	if in.Detected {
		checks = append(checks, plannerSessionCheck(in))
	}

	if len(in.Stale) > 0 {
		checks = append(checks, Check{
			Name:     "planners",
			Severity: SevInfo,
			Detail:   fmt.Sprintf("seen over 7 days ago and no live binding: %s", strings.Join(in.Stale, ", ")),
			Fix:      "relevo planner forget <id|name>",
		})
	}

	return checks
}

// plannerSessionCheck is the planner row for a detected Claude Code session:
// FAIL when Resolve missed or no relevo mcp child reaches the channel, INFO
// while push is unavailable (tools mode), OK once the channel claim is live.
func plannerSessionCheck(in PlannerCheckInput) Check {
	switch {
	case in.Resolved == nil:
		return Check{
			Name:     "planner",
			Severity: SevFail,
			Detail:   "no relevo planner resolved for this Claude Code session",
			Fix:      "relevo planner init (or enable the relevo plugin so its SessionStart hook runs)",
		}
	case !in.MCPChild:
		return Check{
			Name:     "planner",
			Severity: SevFail,
			Detail:   fmt.Sprintf("planner %s: no relevo mcp process is a child of its host process; reports never arrive", plannerRef(in.Resolved, in.Chat)),
			Fix:      "enable the relevo plugin so relevo mcp starts with the session (relevo doctor)",
		}
	case !in.ClaimLive:
		return Check{
			Name:     "planner",
			Severity: SevInfo,
			Detail:   fmt.Sprintf("planner %s: tools mode: reports arrive by background wait. For push, launch with `--dangerously-load-development-channels plugin:relevo@relevo`, or have an org admin add relevo to `allowedChannelPlugins`", plannerRef(in.Resolved, in.Chat)),
		}
	default:
		return Check{
			Name:     "planner",
			Severity: SevOK,
			Detail:   fmt.Sprintf("%s; channel claim live", plannerRef(in.Resolved, in.Chat)),
		}
	}
}

// plannerRef renders "name (id)", with the chat label appended when non-empty.
func plannerRef(rec *planner.Record, chat string) string {
	if chat == "" {
		return rec.Name + " (" + rec.ID + ")"
	}
	return rec.Name + " (" + rec.ID + ") · " + chat
}

// pluginEnabledCheck is the first plugin row: FAIL unless the user's or the
// repo's settings.json has enabledPlugins["relevo@relevo"] == true.
func pluginEnabledCheck(home, repo string) Check {
	for _, path := range []string{
		filepath.Join(home, claudeSettingsRel),
		filepath.Join(repo, claudeSettingsRel),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pluginEnabled(raw) {
			return Check{Name: "plugin", Severity: SevOK, Detail: "relevo@relevo enabled in " + path}
		}
	}
	return Check{
		Name:     "plugin",
		Severity: SevFail,
		Detail:   "relevo@relevo is not enabled in " + filepath.Join(home, claudeSettingsRel) + " or the project's " + claudeSettingsRel,
		Fix:      "enable the relevo plugin in Claude Code (/plugin), or set enabledPlugins[\"relevo@relevo\"] = true in " + claudeSettingsRel,
	}
}

// pluginEnabled reports whether a settings.json enables relevo@relevo; an
// unparseable one reads as not enabled.
func pluginEnabled(raw []byte) bool {
	var s struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	v, ok := s.EnabledPlugins[claudePluginName]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("true"))
}

// pluginHookCheck is the second plugin row: FAIL when the installed plugin
// has no SessionStart hook running `relevo planner init`. Anything relevo
// cannot establish reads `not checked` (OK), never FAIL.
func pluginHookCheck(home string) Check {
	raw, err := os.ReadFile(filepath.Join(home, claudePluginStateRel))
	if err != nil {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (no ~/" + claudePluginStateRel + ")"}
	}

	dirs := installedPluginDirs(raw)
	if len(dirs) == 0 {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (no installed plugin found in ~/" + claudePluginStateRel + ")"}
	}

	checked := false
	for _, dir := range dirs {
		hooks, err := os.ReadFile(filepath.Join(dir, claudeHooksRel))
		if err != nil {
			continue
		}
		checked = true
		if hookRunsPlannerInit(hooks) {
			return Check{Name: "plugin hook", Severity: SevOK, Detail: dir + ": " + pluginHookEvent + " runs relevo " + pluginHookInitCommand}
		}
	}
	if !checked {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (the installed plugin ships no " + claudeHooksRel + ")"}
	}
	return Check{
		Name:     "plugin hook",
		Severity: SevFail,
		Detail:   "the installed relevo plugin has no " + pluginHookEvent + " hook running relevo " + pluginHookInitCommand,
		Fix:      "reinstall the relevo plugin so its " + claudeHooksRel + " ships the " + pluginHookEvent + " hook",
	}
}

// pluginVersionCheck compares the installed relevo@* plugin's version with
// the running binary's. Advisory only: the MCP server runs the binary on
// PATH regardless. Every fact relevo cannot prove reads `not checked` (OK).
func pluginVersionCheck(home, running string) Check {
	const name = "plugin version"

	if running == "" {
		return Check{Name: name, Severity: SevOK, Detail: "not checked"}
	}

	raw, err := os.ReadFile(filepath.Join(home, claudePluginStateRel))
	if err != nil {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (no readable ~/" + claudePluginStateRel + ")"}
	}

	// {"version":2,"plugins":{"relevo@relevo":[{"version":"0.8.0",...}]}}
	var state struct {
		Plugins map[string][]struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (no readable ~/" + claudePluginStateRel + ")"}
	}

	plugin, found := "", false
	for key, entries := range state.Plugins {
		at := strings.Index(key, "@")
		if at <= 0 || key[:at] != "relevo" || len(entries) == 0 {
			continue
		}
		plugin, found = entries[0].Version, true // several entries: report the first
		break
	}
	if !found {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (relevo plugin not installed)"}
	}

	pv, pok := release.ParseVersion(plugin)
	rv, rok := release.ParseVersion(running)
	if !pok || !rok {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (relevo is " + running + ")"}
	}
	// Major.Minor.Patch only, so v0.8.0-15-gd664545 matches 0.8.0.
	if pv.Major == rv.Major && pv.Minor == rv.Minor && pv.Patch == rv.Patch {
		return Check{Name: name, Severity: SevOK, Detail: "plugin " + plugin + " matches relevo"}
	}
	return Check{
		Name:     name,
		Severity: SevWarn,
		Detail:   "plugin " + plugin + ", relevo " + running,
		Fix:      "claude plugin update relevo@relevo",
	}
}

// installedPluginDirs finds every absolute, existing directory path
// mentioning relevo inside the state file, walking the decoded document for
// strings rather than binding to Claude Code's schema for it.
func installedPluginDirs(raw []byte) []string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []string
	for _, s := range jsonStrings(v) {
		if !filepath.IsAbs(s) || !strings.Contains(s, "relevo") {
			continue
		}
		if info, err := os.Stat(s); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, s)
	}
	return out
}

// hookRunsPlannerInit reports whether a hooks.json has a SessionStart entry
// whose command runs `relevo planner init`.
func hookRunsPlannerInit(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return hasSessionStartPlannerInit(v)
}

func hasSessionStartPlannerInit(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == pluginHookEvent && jsonContains(val, pluginHookInitCommand) {
				return true
			}
			if hasSessionStartPlannerInit(val) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasSessionStartPlannerInit(e) {
				return true
			}
		}
	}
	return false
}

func jsonContains(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, want)
	case map[string]any, []any:
		raw, err := json.Marshal(t)
		return err == nil && bytes.Contains(raw, []byte(want))
	}
	return false
}

func jsonStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, jsonStrings(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range t {
			out = append(out, jsonStrings(e)...)
		}
		return out
	}
	return nil
}
