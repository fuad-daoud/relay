package relay

import (
	"fmt"
	"strings"
)

// The result lines of the three pick-able verbs live here, not in cmd/relay,
// because the picker's result screen (internal/pick) prints the same text the
// terminal does. Each returns the exact bytes cmd/relay printed before #15,
// without a trailing newline; the caller adds one.

// DoneText is what `relay done` says on success.
func DoneText(name string) string {
	return fmt.Sprintf("%s marked done; relaying stopped (relay gc archives it when you are finished with it)", name)
}

// AnswerText is what `relay answer` says on success.
func AnswerText(name string) string {
	return fmt.Sprintf("answered %s's builder", name)
}

// UnbindText is what `relay unbind` says on success: one line for the
// binding, then at most one for its worktree, then at most one for a headless process.
func UnbindText(name string, res UnbindResult) string {
	var lines []string
	if res.ArchivedTo != "" {
		lines = append(lines, fmt.Sprintf("archived %s to %s (panes left untouched)", name, res.ArchivedTo))
	} else {
		lines = append(lines, fmt.Sprintf("unbound %s (panes left untouched)", name))
	}
	switch {
	case res.WorktreeRemoved != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s", res.WorktreeRemoved))
	case res.WorktreeKept != "":
		lines = append(lines, fmt.Sprintf("kept worktree %s (%s)\n  remove by hand: git -C %s worktree remove %s",
			res.WorktreeKept, res.KeptReason, res.WorktreeKept, res.WorktreeKept))
	case res.WorktreeGone != "":
		lines = append(lines, fmt.Sprintf("worktree %s was already gone", res.WorktreeGone))
	}
	switch {
	case res.ProcessStopped != 0:
		lines = append(lines, fmt.Sprintf("stopped builder process %d", res.ProcessStopped))
	case res.ProcessErr != "":
		lines = append(lines, fmt.Sprintf("could not stop builder process (%s); check for it yourself", res.ProcessErr))
	}
	return strings.Join(lines, "\n")
}
