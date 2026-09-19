package proc

import "strings"

// DeniedEnv names the variables relay never passes to a builder process.
// They are relay's own secrets, not the harness's: a builder IS the harness
// and needs its provider credentials, so nothing like ANTHROPIC_API_KEY or
// GOOGLE_API_KEY belongs here. Constant on purpose -- a user who wants a
// builder to hold one of these sets it in the harness's own config.
var DeniedEnv = []string{"TYPESAFE_API_KEY"}

// ChildEnv returns parent with every entry whose name is in deny removed,
// then extra appended verbatim. Order is otherwise preserved. A name
// matches when the entry is "NAME=..." or exactly "NAME". extra is not
// filtered: it is relay's own and may set a denied name deliberately.
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
