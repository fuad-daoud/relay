package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// opencodeServicePath is where opencode 2.x's shared background service
// records its listening address and password: written once by `opencode
// serve --service` (also `opencode service start`), read by every `opencode
// run` on this machine until it stops (#256).
const opencodeServicePath = ".config/opencode/service.json"

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
// relay has moved on from -- Runner.Kill (a process-group kill of the
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
	svcPath, err := env.HomePath(opencodeServicePath)
	if err != nil || env.Stat(svcPath) != nil {
		return Check{} // caller skips a zero-value row (Name == "")
	}

	detail := fmt.Sprintf("2.x shared service (~/%s); a killed or switched-away client leaves its session running inside the service (#256)", opencodeServicePath)
	if dbPath, derr := env.HomePath(opencodeDBPath); derr == nil && env.Stat(dbPath) == nil {
		if n, ok := opencodeSessionCount(ctx, env, dbPath); ok {
			detail = fmt.Sprintf("%s; %d session(s) recorded in opencode.db", detail, n)
		}
	}
	return Check{Group: "opencode", Name: "service", Severity: SevOK, Detail: detail}
}

// opencodeSessionCount reads the session table's row count from dbPath
// through sqlite3 -readonly, the same tool the usage checks require on
// PATH. false means the count could not be read (no sqlite3, no such
// table, unparseable output) -- never an error the caller must handle.
func opencodeSessionCount(ctx context.Context, env Env, dbPath string) (int, bool) {
	out, err := env.Command(ctx, "sqlite3", "-readonly", dbPath, "select count(*) from session")
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return n, true
}

// opencodeAllowlistCheck reports whether opencode's own config lets a headless
// builder read the plan relay stages under stateRoot (#236). opencode refuses
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
