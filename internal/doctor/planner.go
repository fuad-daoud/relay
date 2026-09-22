package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relay/internal/planner"
)

// The relay Claude Code plugin's install facts (§4.8 rows 1-2). Both live
// under the user's home, resolved through $HOME so cmd/relay's TestMain
// isolation holds.
const (
	claudeSettingsRel     = ".claude/settings.json"
	claudePluginStateRel  = ".claude/plugins/installed_plugins.json"
	claudePluginName      = "relay@relay"
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

// HasMCPChild reports whether any of a host process's children is a `relay
// mcp` process (§4.8's second planner FAIL). Pure: the caller reads the OS
// and passes the table, so the rule is testable without a live process tree.
func HasMCPChild(children []ChildProcess) bool {
	for _, c := range children {
		if len(c.Args) == 0 {
			continue
		}
		if base := filepath.Base(c.Args[0]); base != "relay" && base != "relay.exe" {
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
// working directory. cmd/relay gathers it; the rules live here.
type PlannerCheckInput struct {
	// Claude is true when a claude candidate or a claude planner record
	// exists. The two plugin rows exist only then (§4.8).
	Claude bool
	// Home is the home directory the plugin files are read under: $HOME, so
	// a test (and cmd/relay's TestMain) can point it at a temp dir.
	Home string
	// Repo is the project directory whose .claude/settings.json is read: the
	// repository `relay doctor` runs in.
	Repo string
	// Detected is planner.Detect's answer: relay is running inside a Claude
	// Code session, which is when the planner row exists.
	Detected bool
	// Resolved is the planner Resolve found for this session; nil when it
	// found none.
	Resolved *planner.Record
	// ClaimLive is true when a live channel claim exists for Resolved.
	ClaimLive bool
	// MCPChild is true when a `relay mcp` process is a child of the
	// planner's host process, which is how this session reaches the channel
	// at all (§4.8). False reads as FAIL: without it, push never arrives.
	MCPChild bool
	// Stale names the planner records seen more than seven days ago that no
	// live binding names.
	Stale []string
}

// PlannerChecks reports §4.8's rows: the relay plugin enabled in Claude Code,
// the installed plugin's SessionStart hook, this session's planner, and the
// stale-record note. Leaving every field zero reports nothing, so a caller
// with no Claude Code candidate gets exactly the report it had before.
func PlannerChecks(in PlannerCheckInput) []Check {
	var checks []Check

	if in.Claude {
		checks = append(checks, pluginEnabledCheck(in.Home, in.Repo))
		checks = append(checks, pluginHookCheck(in.Home))
	}

	if in.Detected {
		switch {
		case in.Resolved == nil:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevFail,
				Detail:   "no relay planner resolved for this Claude Code session",
				Fix:      "relay planner init (or enable the relay plugin so its SessionStart hook runs)",
			})
		case !in.MCPChild:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevFail,
				Detail:   fmt.Sprintf("planner %s (%s): no relay mcp process is a child of its host process; reports never arrive", in.Resolved.Name, in.Resolved.ID),
				Fix:      "enable the relay plugin so relay mcp starts with the session (relay doctor)",
			})
		case !in.ClaimLive:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevInfo,
				Detail:   fmt.Sprintf("planner %s (%s): tools mode: reports arrive by background wait. For push, launch with `--dangerously-load-development-channels plugin:relay@relay`, or have an org admin add relay to `allowedChannelPlugins`", in.Resolved.Name, in.Resolved.ID),
			})
		default:
			checks = append(checks, Check{
				Name:     "planner",
				Severity: SevOK,
				Detail:   fmt.Sprintf("%s (%s); channel claim live", in.Resolved.Name, in.Resolved.ID),
			})
		}
	}

	if len(in.Stale) > 0 {
		checks = append(checks, Check{
			Name:     "planners",
			Severity: SevInfo,
			Detail:   fmt.Sprintf("seen over 7 days ago and no live binding: %s", strings.Join(in.Stale, ", ")),
			Fix:      "relay planner forget <id|name>",
		})
	}

	return checks
}

// pluginEnabledCheck is §4.8's first row: FAIL when a claude candidate or
// planner record exists and neither the user's nor the repo's settings.json
// has enabledPlugins["relay@relay"] == true.
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
			return Check{Name: "plugin", Severity: SevOK, Detail: "relay@relay enabled in " + path}
		}
	}
	return Check{
		Name:     "plugin",
		Severity: SevFail,
		Detail:   "relay@relay is not enabled in " + filepath.Join(home, claudeSettingsRel) + " or the project's " + claudeSettingsRel,
		Fix:      "enable the relay plugin in Claude Code (/plugin), or set enabledPlugins[\"relay@relay\"] = true in " + claudeSettingsRel,
	}
}

// pluginEnabled reports whether a settings.json enables relay@relay. Anything
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
// SessionStart hook running `relay planner init`. A missing
// installed_plugins.json, a plugin that cannot be found, or a plugin with no
// hooks.json all read `not checked` (OK), never FAIL: relay cannot establish
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
			return Check{Name: "plugin hook", Severity: SevOK, Detail: dir + ": " + pluginHookEvent + " runs relay " + pluginHookInitCommand}
		}
	}
	if !checked {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (the installed plugin ships no " + claudeHooksRel + ")"}
	}
	return Check{
		Name:     "plugin hook",
		Severity: SevFail,
		Detail:   "the installed relay plugin has no " + pluginHookEvent + " hook running relay " + pluginHookInitCommand,
		Fix:      "reinstall the relay plugin so its " + claudeHooksRel + " ships the " + pluginHookEvent + " hook",
	}
}

// installedPluginDirs finds the installed plugin directories the state file
// names: every absolute path under it that exists and is a directory, and
// that mentions relay. The file's exact schema is Claude Code's to change, so
// this walks the decoded document for strings instead of binding to a shape;
// anything it cannot find leaves the caller reporting `not checked`.
func installedPluginDirs(raw []byte) []string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []string
	for _, s := range jsonStrings(v) {
		if !filepath.IsAbs(s) || !strings.Contains(s, "relay") {
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
// whose command runs `relay planner init`. Decoded and walked rather than
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
