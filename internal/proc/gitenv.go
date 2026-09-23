package proc

import (
	"strconv"
	"strings"
)

// gitNoFsmonitorEnv returns the environment entries that disable git's
// fsmonitor for a spawn (#378): GIT_CONFIG_KEY_<n>=core.fsmonitor,
// GIT_CONFIG_VALUE_<n>=false and GIT_CONFIG_COUNT=<n+1>. Belt and braces
// beside the supervisor's reap: a builder whose git commands never start
// fsmonitor--daemon never leaves one behind, reparented to the user manager,
// to keep its round's scope alive after the builder has gone.
//
// n continues the count the child would otherwise have: extra (relevo's own
// spec.Env) if it sets GIT_CONFIG_COUNT, else parent, else 0, so the entries
// git already had stay valid and this one lands after them and wins. A
// non-numeric count is treated as 0 and replaced. The caller must make the
// entry returned here win over the parent's own GIT_CONFIG_COUNT by denying
// that name from the parent (Start does, the way it does for GOMAXPROCS).
// Pure; never mutates its inputs.
func gitNoFsmonitorEnv(parent, extra []string) []string {
	n := 0
	if v, ok := envLookup(extra, "GIT_CONFIG_COUNT"); ok {
		n, _ = strconv.Atoi(v) // a non-numeric count is 0
	} else if v, ok := envLookup(parent, "GIT_CONFIG_COUNT"); ok {
		n, _ = strconv.Atoi(v)
	}
	idx := strconv.Itoa(n)
	return []string{
		"GIT_CONFIG_KEY_" + idx + "=core.fsmonitor",
		"GIT_CONFIG_VALUE_" + idx + "=false",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(n+1),
	}
}

// envLookup returns the value of name in env and whether name is set at all,
// matching the same way ChildEnv's deny list does: an entry "NAME=..." or
// exactly "NAME" (whose value is ""). The last entry wins, as os/exec
// resolves a duplicated name. Pure.
func envLookup(env []string, name string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		n, v, has := strings.Cut(e, "=")
		if n != name {
			continue
		}
		if has {
			value = v
		} else {
			value = ""
		}
		found = true
	}
	return value, found
}
