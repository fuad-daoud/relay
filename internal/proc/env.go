package proc

import (
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// DeniedEnv names the variables relevo never passes to a builder process.
// They are relevo's own secrets, not the harness's: a builder IS the harness
// and needs its provider credentials, so nothing like ANTHROPIC_API_KEY or
// GOOGLE_API_KEY belongs here. Constant on purpose -- a user who wants a
// builder to hold one of these sets it in the harness's own config.
var DeniedEnv = []string{"TYPESAFE_API_KEY"}

// ChildEnv returns parent with every entry whose name is in deny removed,
// then extra appended verbatim. Order is otherwise preserved. A name
// matches when the entry is "NAME=..." or exactly "NAME". extra is not
// filtered: it is relevo's own and may set a denied name deliberately.
// Pure; never mutates its inputs; returns a fresh slice.
func ChildEnv(parent, deny, extra []string) []string {
	denied := make(map[string]struct{}, len(deny))
	for _, d := range deny {
		denied[d] = struct{}{}
	}

	out := make([]string, 0, len(parent)+len(extra))
	for _, e := range parent {
		name, _, _ := strings.Cut(e, "=")
		if _, ok := denied[name]; ok {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}

// goMaxProcsEnv returns the GOMAXPROCS entry to add to a scoped child's
// environment, or nil when none is wanted (#315): nil when scope is nil,
// when the scope limits nothing (relevo.GoMaxProcsFor), or when extra
// (relevo's own spec.Env) already carries one. An inherited GOMAXPROCS in
// parent does not stop it: a scope that limits CPUs is the operator's
// explicit choice, and Start removes the parent's entry so the child sees
// one value (#315 round 2). Otherwise it returns exactly one entry,
// "GOMAXPROCS=<n>". Pure; never mutates its inputs.
func goMaxProcsEnv(parent, extra []string, scope *relevo.ScopeSpec) []string {
	if scope == nil {
		return nil
	}
	n, ok := relevo.GoMaxProcsFor(*scope)
	if !ok {
		return nil
	}
	if hasEnvName(extra, "GOMAXPROCS") {
		return nil
	}
	return []string{"GOMAXPROCS=" + strconv.Itoa(n)}
}

// hasEnvName reports whether env carries name, matching the same way
// ChildEnv's deny list does: an entry "NAME=..." or exactly "NAME". Pure.
func hasEnvName(env []string, name string) bool {
	for _, e := range env {
		if n, _, _ := strings.Cut(e, "="); n == name {
			return true
		}
	}
	return false
}
