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

// The relevo Claude Code plugin's install facts (§4.8 rows 1-2). Both live
// under the user's home, resolved through $HOME so cmd/relevo's TestMain
// isolation holds.
const (
	claudeSettingsRel     = ".claude/settings.json"
	claudePluginStateRel  = ".claude/plugins/installed_plugins.json"
	claudePluginName      = "relevo@relevo"
	claudeHooksRel        = "hooks/hooks.json"
	pluginHookInitCommand = "planner init"
	pluginHookEvent       = "SessionStart"
)

// ChildProcess is one entry of a host process's child table: the pid and its
// argv, as /proc/<pid>/task/*/children plus /proc/<pid>/cmdline (or `ps
// --ppid`) report them.
type ChildProcess struct {
	PID  int
	Args []string
}

// HasMCPChild reports whether any of a host process's children is a `relevo
// mcp` process (§4.8's second planner FAIL). Pure: the caller reads the OS
// and passes the table, so the rule is testable without a live process tree.
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

// PlannerCheckInput is everything §4.8's planner rows need that doctor.Run
// cannot read itself: it has no planner registry, no environment and no
// working directory. cmd/relevo gathers it; the rules live here.
type PlannerCheckInput struct {
	// Claude is true when a claude candidate or a claude planner record
	// exists. The two plugin rows exist only then (§4.8).
	Claude bool
	// Home is the home directory the plugin files are read under: $HOME, so
	// a test (and cmd/relevo's TestMain) can point it at a temp dir.
	Home string
	// Repo is the project directory whose .claude/settings.json is read: the
	// repository `relevo doctor` runs in.
	Repo string
	// Detected is planner.Detect's answer: relevo is running inside a Claude
	// Code session, which is when the planner row exists.
	Detected bool
	// Resolved is the planner Resolve found for this session; nil when it
	// found none.
	Resolved *planner.Record
	// Chat is the resolved planner's chatlabel.Label.String() (#386): the
	// harness's own name for its session, read by cmd/relevo when the
	// command runs. "" when the label is empty or Resolved is nil.
	Chat string
	// ClaimLive is true when a live channel claim exists for Resolved.
	ClaimLive bool
	// MCPChild is true when a `relevo mcp` process is a child of the
	// planner's host process, which is how this session reaches the channel
	// at all (§4.8). False reads as FAIL: without it, push never arrives.
	MCPChild bool
	// Stale names the planner records seen more than seven days ago that no
	// live binding names.
	Stale []string
	// Running is the relevo binary's own version (buildVersion()), compared
	// with the installed plugin's by the plugin version row. "" skips it.
	Running string
}

// PlannerChecks reports §4.8's rows: the relevo plugin enabled in Claude Code,
// the installed plugin's SessionStart hook, this session's planner, and the
// stale-record note. Leaving every field zero reports nothing, so a caller
// with no Claude Code candidate gets exactly the report it had before.
func PlannerChecks(in PlannerCheckInput) []Check {
	var checks []Check

	if in.Claude {
		checks = append(checks, pluginEnabledCheck(in.Home, in.Repo))
		checks = append(checks, pluginHookCheck(in.Home))
		checks = append(checks, pluginVersionCheck(in.Home, in.Running))
	}

	if in.Detected {
		switch {
		case in.Resolved == nil:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevFail,
				Detail:   "no relevo planner resolved for this Claude Code session",
				Fix:      "relevo planner init (or enable the relevo plugin so its SessionStart hook runs)",
			})
		case !in.MCPChild:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevFail,
				Detail:   fmt.Sprintf("planner %s: no relevo mcp process is a child of its host process; reports never arrive", plannerRef(in.Resolved, in.Chat)),
				Fix:      "enable the relevo plugin so relevo mcp starts with the session (relevo doctor)",
			})
		case !in.ClaimLive:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevInfo,
				Detail:   fmt.Sprintf("planner %s: tools mode: reports arrive by background wait. For push, launch with `--dangerously-load-development-channels plugin:relevo@relevo`, or have an org admin add relevo to `allowedChannelPlugins`", plannerRef(in.Resolved, in.Chat)),
			})
		default:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevOK,
				Detail:   fmt.Sprintf("%s; channel claim live", plannerRef(in.Resolved, in.Chat)),
			})
		}
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

// plannerRef renders a planner record for a doctor detail: its name and id,
// with the harness's own chat label appended when cmd/relevo resolved one
// (#386). An empty chat leaves today's "name (id)" byte-identical.
func plannerRef(rec *planner.Record, chat string) string {
	if chat == "" {
		return rec.Name + " (" + rec.ID + ")"
	}
	return rec.Name + " (" + rec.ID + ") · " + chat
}

// pluginEnabledCheck is §4.8's first row: FAIL when a claude candidate or
// planner record exists and neither the user's nor the repo's settings.json
// has enabledPlugins["relevo@relevo"] == true.
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

// pluginEnabled reports whether a settings.json enables relevo@relevo. Anything
// it cannot parse reads as not enabled, which is the FAIL the fix acts on.
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

// pluginHookCheck is §4.8's second row: FAIL when the installed plugin has no
// SessionStart hook running `relevo planner init`. A missing
// installed_plugins.json, a plugin that cannot be found, or a plugin with no
// hooks.json all read `not checked` (OK), never FAIL: relevo cannot establish
// the fact, and the row's fix would be a guess.
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
// the running binary's release version. Advisory: the MCP server is the
// binary on PATH, so a stale plugin serves current tools -- what it
// carries stale is its manifest and hooks.json, whose concrete symptom
// the plugin hook row already fails.
//
// Every fact relevo cannot prove reads `not checked` (OK), never a warning:
// a missing or unreadable file, no relevo entry, and a version either side
// of the comparison cannot parse all take that route.
func pluginVersionCheck(home, running string) Check {
	const name = "plugin version"

	if running == "" {
		return Check{Name: name, Severity: SevOK, Detail: "not checked"}
	}

	raw, err := os.ReadFile(filepath.Join(home, claudePluginStateRel))
	if err != nil {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (no readable ~/" + claudePluginStateRel + ")"}
	}

	// Claude Code's private shape:
	// {"version":2,"plugins":{"relevo@relevo":[{"version":"0.8.0",...}]}}.
	// A small struct, not installedPluginDirs: that scans every string for
	// a path and cannot yield a version.
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
		// Several entries for one key: the first is the one to report.
		plugin, found = entries[0].Version, true
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
	// Major.Minor.Patch only: Suffix is ignored, so v0.8.0-15-gd664545
	// matches 0.8.0.
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

// installedPluginDirs finds the installed plugin directories the state file
// names: every absolute path under it that exists and is a directory, and
// that mentions relevo. The file's exact schema is Claude Code's to change, so
// this walks the decoded document for strings instead of binding to a shape;
// anything it cannot find leaves the caller reporting `not checked`.
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
// whose command runs `relevo planner init`. Decoded and walked rather than
// substring-matched, so an unrelated mention of "planner init" in another
// event's command does not count.
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

// jsonContains reports whether any string inside v contains want. Marshal
// cannot fail on a decoded document, and its error reads as "no match".
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

// jsonStrings collects every string in a decoded JSON document.
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
