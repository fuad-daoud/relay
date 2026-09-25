package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// opencodeServicePaths are where opencode 2.x's background service records
// its listening address: the live record with url and pid is in the state dir
// on 2.0.14, with the config dir as fallback (#393).
const (
	opencodeStateServicePath  = ".local/state/opencode/service.json"
	opencodeConfigServicePath = ".config/opencode/service.json"
)

var opencodeServicePaths = []string{
	opencodeStateServicePath,
	opencodeConfigServicePath,
}

// opencodeDBPath is opencode's own SQLite store, already read (through
// sqlite3, never database/sql) by the usage checks below for round
// accounting. Its session table carries the same id/directory/time_updated
// facts the service's own HTTP API reports, so opencodeServiceCheck's
// session count needs no network call and no credential -- consistent with
// #256's standalone fix, which the plan preferred precisely because it
// needs neither.
const opencodeDBPath = ".local/share/opencode/opencode.db"

// opencodeServiceCheck reports opencode 2.x's shared-service model (#256):
// `opencode run` without --standalone is a thin client of the one `opencode
// serve --service` per user, so a killed or switched-away client leaves its
// agent session running inside the service, still editing the worktree
// relevo has moved on from -- Runner.Kill (a process-group kill of the
// client) never reaches it. This row is informational only, so it is always
// SevOK when it appears, and it appears only when service.json exists: the
// one local signal that the shared-service model is actually in play here.
//
// The session count is a courtesy, not a live count of "still-running"
// sessions (opencode.db persists a session's row long after its process
// exits): a sqlite3 query that fails, or finds no opencode.db, still leaves
// the base note, because whether the count is readable is never itself a
// fault.
func opencodeServiceCheck(ctx context.Context, env Env) Check {
	var usedRelPath string
	for _, rel := range opencodeServicePaths {
		svcPath, err := env.HomePath(rel)
		if err != nil || env.Stat(svcPath) != nil {
			continue
		}
		b, err := env.ReadFile(svcPath)
		if err != nil {
			continue
		}
		var record struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(b, &record); err != nil || record.URL == "" {
			continue
		}
		usedRelPath = rel
		break
	}
	if usedRelPath == "" {
		return Check{} // caller skips a zero-value row (Name == "")
	}

	detail := fmt.Sprintf("2.x shared service (~/%s); a killed or switched-away client leaves its session running inside the service (#256)", usedRelPath)
	if dbPath, derr := env.HomePath(opencodeDBPath); derr == nil && env.Stat(dbPath) == nil {
		if n, ok := opencodeSessionCount(ctx, env, dbPath); ok {
			detail = fmt.Sprintf("%s; %d session(s) recorded in opencode.db", detail, n)
		}
	}
	return Check{Group: "opencode", Name: "service", Severity: SevOK, Detail: detail}
}

// opencodeSessionCount reads the session count from dbPath through sqlite3
// -readonly, the same tool the usage checks require on PATH. OpenCode 2.0.14
// keeps its sessions in session_v2, and only pre-2.0 databases have the legacy
// session table, so session_v2 is counted first and session is the fallback.
// The first query that succeeds and parses as an integer gives the count.
// false means the count could not be read (no sqlite3, no such table,
// unparseable output) -- never an error the caller must handle.
func opencodeSessionCount(ctx context.Context, env Env, dbPath string) (int, bool) {
	for _, query := range []string{
		"select count(*) from session_v2",
		"select count(*) from session",
	} {
		out, err := env.Command(ctx, "sqlite3", "-readonly", dbPath, query)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

// opencodeAllowlistCheck reports whether opencode's own config lets a headless
// builder read the plan relevo stages under stateRoot (#236). opencode refuses
// that read unless permission.external_directory allows the directory, and it
// does not expand ~ or $HOME in these patterns, so the entry is compared
// against the literal home-resolved root.
//
// Order of operations: locate -> read -> strip -> parse -> walk -> classify.
// Every failure mode is a SevWarn row carrying a Fix; doctor never fails a run
// over this.
func opencodeAllowlistCheck(env Env, stateRoot string) Check {
	// Locate: the first readable candidate wins. opencode.jsonc is the file
	// the README tells users to edit; the extensionless name is the fallback.
	var path string
	var body []byte
	for _, rel := range []string{".config/opencode/opencode.jsonc", ".config/opencode/opencode.json"} {
		p, err := env.HomePath(rel)
		if err != nil {
			continue
		}
		if err := env.Stat(p); err != nil {
			continue
		}
		b, err := env.ReadFile(p)
		if err != nil {
			continue
		}
		path, body = p, b
		break
	}

	warn := func(detail string) Check {
		return Check{
			Group:    "opencode",
			Name:     "external_directory",
			Severity: SevWarn,
			Detail:   detail,
			Fix:      snippet(stateRoot),
		}
	}

	if path == "" {
		return warn("no ~/.config/opencode/opencode.jsonc; headless opencode builders auto-reject reading their plan")
	}

	// Strip -> parse: opencode's config is JSONC. A file we cannot parse is
	// still only a warning -- the Fix is what the user acts on.
	var cfg map[string]any
	if err := json.Unmarshal(stripJSONC(body), &cfg); err != nil {
		return warn(fmt.Sprintf("could not parse %s: %v", path, err))
	}

	// Walk: permission -> external_directory -> pattern -> value. Any value
	// other than "allow" (opencode's "ask", "deny") leaves the read blocked.
	permission, _ := cfg["permission"].(map[string]any)
	external, _ := permission["external_directory"].(map[string]any)
	for pattern, raw := range external {
		if value, _ := raw.(string); value != "allow" {
			continue
		}
		if patternCovers(pattern, stateRoot) {
			return Check{
				Group:    "opencode",
				Name:     "external_directory",
				Severity: SevOK,
				Detail:   fmt.Sprintf("%s allows %s/**", path, stateRoot),
			}
		}
	}

	return warn(fmt.Sprintf("%s has no permission.external_directory allow entry for %s/**; headless opencode builders auto-reject reading their plan", path, stateRoot))
}

// patternCovers reports whether an allow entry opens stateRoot: the root
// itself, the root with /* or /**, or a glob whose prefix before the first
// '*' is a parent of the root (e.g. "/home/x/.local/state/**").
func patternCovers(pattern, stateRoot string) bool {
	if pattern == stateRoot || pattern == stateRoot+"/*" || pattern == stateRoot+"/**" {
		return true
	}
	i := strings.Index(pattern, "*")
	if i <= 0 {
		return false
	}
	prefix := pattern[:i]
	if len(stateRoot) <= len(prefix) || !strings.HasPrefix(stateRoot, prefix) {
		return false
	}
	return strings.HasSuffix(prefix, "/") || stateRoot[len(prefix)] == '/'
}

// snippet is the README's jsonc block with stateRoot substituted, so the Fix
// is something the user can paste into their opencode config.
func snippet(stateRoot string) string {
	return "add to ~/.config/opencode/opencode.jsonc:\n" +
		`"permission": {
  "external_directory": {
    "` + stateRoot + `/*": "allow",
    "` + stateRoot + `/**": "allow"
  }
}`
}

// stripJSONC removes // line comments and /* */ block comments outside string
// literals, and a trailing comma before } or ], so opencode's config parses
// with encoding/json. It is string-literal aware: a '/' inside "..." is
// content, and \" escapes are honoured. Malformed input still returns bytes;
// the json.Unmarshal error is what the caller reports.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		switch {
		case b[i] == '"':
			// Copy the literal verbatim, escapes included.
			out = append(out, b[i])
			i++
			for i < len(b) {
				ch := b[i]
				if ch == '\\' && i+1 < len(b) {
					out = append(out, ch, b[i+1])
					i += 2
					continue
				}
				out = append(out, ch)
				i++
				if ch == '"' {
					break
				}
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			if i+1 < len(b) {
				i += 2
			} else {
				i = len(b)
			}
		default:
			// A trailing comma before a closing brace or bracket is not
			// JSON. Comments were dropped rather than copied, so looking
			// back past whitespace in out finds it.
			if b[i] == '}' || b[i] == ']' {
				k := len(out)
				for k > 0 && jsoncSpace(out[k-1]) {
					k--
				}
				if k > 0 && out[k-1] == ',' {
					out = out[:k-1]
				}
			}
			out = append(out, b[i])
			i++
		}
	}
	return out
}

func jsoncSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// opencodePluginDirPath is the home-relative directory the plugin package
// installs into (#393 §5.5); the installed row names it.
const opencodePluginDirPath = ".config/opencode/plugins/relevo"

// opencodePluginCheck reports whether relevo's OpenCode plugin package is
// installed (#393 §5.5). None of the three shipped files present reads as
// "not installed" -- the plugin is opt-in -- all present and equal to the
// embedded bytes reads as installed, and any missing or edited file is a
// warning naming each.
//
// The comparison is against the table's embedded copies, through
// harness.ShippedFileBytes, and harness.DocEqual applies the same
// trailing-whitespace tolerance the role checks use.
func opencodePluginCheck(env Env) Check {
	h, ok := harness.Lookup("opencode")
	if !ok {
		return Check{}
	}

	present := 0
	var problems []string
	for _, f := range h.Files {
		full, err := env.HomePath(f.Path)
		if err != nil || env.Stat(full) != nil {
			problems = append(problems, "~/"+f.Path+" is missing")
			continue
		}
		present++
		shipped, err := harness.ShippedFileBytes("opencode", f.Name)
		if err != nil {
			problems = append(problems, "~/"+f.Path+" has no shipped copy")
			continue
		}
		b, err := env.ReadFile(full)
		if err != nil {
			problems = append(problems, "~/"+f.Path+" is unreadable")
			continue
		}
		if !harness.DocEqual(shipped, b) {
			problems = append(problems, "~/"+f.Path+" differs from the shipped copy")
		}
	}

	switch {
	case present == 0:
		return Check{
			Group:    "opencode",
			Name:     "plugin",
			Severity: SevOK,
			Detail:   "not installed -- relevo config agents installs the OpenCode plugin",
		}
	case len(problems) == 0:
		return Check{
			Group:    "opencode",
			Name:     "plugin",
			Severity: SevOK,
			Detail:   "installed (~/" + opencodePluginDirPath + ")",
		}
	default:
		return Check{
			Group:    "opencode",
			Name:     "plugin",
			Severity: SevWarn,
			Detail:   strings.Join(problems, "; "),
			Fix:      "relevo config agents (add --force to replace your edits)",
		}
	}
}

// opencodeReservedKeys are the key strings the relevo plugin binds, in both
// spellings a user may write: the leader form OpenCode documents and the
// literal chord the leader expands to (#393 §5.5).
var opencodeReservedKeys = map[string]bool{
	"<leader>o": true,
	"<leader>j": true,
	"ctrl+x o":  true,
	"ctrl+x j":  true,
}

// opencodePluginKeysCheck reports whether another command already binds a key
// the relevo plugin uses (#393 §5.5): a WARN naming the file, command and key,
// or an OK row when the keys are free. It answers no row (zero Check) unless
// the plugin is installed, since a clash only matters then.
//
// The user's OpenCode config is read in order -- opencode.jsonc, opencode.json,
// cli.json -- and a file that is absent, unreadable or does not parse is
// skipped: it never fails the run.
func opencodePluginKeysCheck(env Env) Check {
	if !opencodePluginInstalled(env) {
		return Check{}
	}

	for _, rel := range []string{
		".config/opencode/opencode.jsonc",
		".config/opencode/opencode.json",
		".config/opencode/cli.json",
	} {
		full, err := env.HomePath(rel)
		if err != nil || env.Stat(full) != nil {
			continue
		}
		body, err := env.ReadFile(full)
		if err != nil {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(stripJSONC(body), &cfg); err != nil {
			continue
		}
		keybinds, _ := cfg["keybinds"].(map[string]any)
		commands := make([]string, 0, len(keybinds))
		for command := range keybinds {
			commands = append(commands, command)
		}
		sort.Strings(commands)
		for _, command := range commands {
			if strings.HasPrefix(command, "relevo.") {
				continue
			}
			for _, key := range opencodeKeyList(keybinds[command]) {
				if opencodeReservedKeys[key] {
					return Check{
						Group:    "opencode",
						Name:     "plugin keys",
						Severity: SevWarn,
						Detail:   fmt.Sprintf("~/%s: %s is bound to %s, which the relevo plugin uses", rel, command, key),
					}
				}
			}
		}
	}

	return Check{
		Group:    "opencode",
		Name:     "plugin keys",
		Severity: SevOK,
		Detail:   "ctrl+x o and ctrl+x j are free",
	}
}

// opencodePluginInstalled reports whether at least one shipped plugin file is
// present under the home: the signal the keys check needs to decide whether to
// answer a row at all.
func opencodePluginInstalled(env Env) bool {
	h, ok := harness.Lookup("opencode")
	if !ok {
		return false
	}
	for _, f := range h.Files {
		if full, err := env.HomePath(f.Path); err == nil && env.Stat(full) == nil {
			return true
		}
	}
	return false
}

// opencodeKeyList reads a keybinds value: one key string or an array of them.
// Any other JSON shape contributes nothing.
func opencodeKeyList(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
