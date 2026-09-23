package relay

import (
	"fmt"
	"strings"
)

// The result lines of the three pick-able verbs live here, not in cmd/relay,
// because the picker's result screen (internal/pick) prints the same text the
// terminal does. Each returns the exact bytes cmd/relay printed before #15,
// without a trailing newline; the caller adds one.

// DoneText is what `relay done` says on success: one line for the binding,
// then at most one for its worktree.
func DoneText(name string, r DoneResult) string {
	lines := []string{
		fmt.Sprintf("%s marked done; relaying stopped (relay gc archives it when you are finished with it)", name),
	}
	switch {
	case r.WorktreeRemoved != "" && r.Branch != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s (branch %s is free to check out)", r.WorktreeRemoved, r.Branch))
	case r.WorktreeRemoved != "":
		lines = append(lines, fmt.Sprintf("removed worktree %s", r.WorktreeRemoved))
	case r.WorktreeKept != "":
		lines = append(lines, fmt.Sprintf("kept worktree %s (%s); relay gc retries when it is clean", r.WorktreeKept, r.KeptReason))
	case r.WorktreeGone != "":
		lines = append(lines, fmt.Sprintf("worktree %s was already gone", r.WorktreeGone))
	}
	return strings.Join(lines, "\n")
}

// RestoreText is what `relay bind --resume` prints when a missing worktree was restored.
func RestoreText(res Resolution) string {
	if res.RestoredWorktree == "" {
		return ""
	}
	lines := []string{
		fmt.Sprintf("restored worktree %s on %s", res.RestoredWorktree, res.RestoredBranch),
	}
	return strings.Join(lines, "\n")
}

// PauseText is what `relay pause` says on success: the binding and what it
// kept, then what it did with the builder pane, then how to bring it back.
func PauseText(name string, r PauseResult) string {
	lines := []string{
		fmt.Sprintf("%s paused after round %d; worktree %s released, branch %s kept", name, r.Round, r.Worktree, r.Branch),
	}
	if r.Committed != "" {
		sha12 := r.Committed
		if len(sha12) > 12 {
			sha12 = sha12[:12]
		}
		lines = append(lines, fmt.Sprintf("  committed %s ([relay] %s: paused after round %d)", sha12, name, r.Round))
	}
	lines = append(lines, fmt.Sprintf("  resume: relay bind --resume --name %s", name))
	return strings.Join(lines, "\n")
}

// LandText is what `relay land` says on success: what was landed and how far
// it got, then the PR (or the exact command that would open one).
func LandText(res LandResult) string {
	how := "merged"
	if res.Rebased {
		how = "rebased"
	}
	line := fmt.Sprintf("landed %s -> %s (%s; gate %s; pushed)",
		res.Branch, res.Base, how, res.GateResult)
	switch {
	case res.PRURL != "":
		line += "\n  pr: " + res.PRURL
	case res.PRCommand != "":
		line += "\n  open the PR: " + res.PRCommand
	}
	return line
}

// StopText is what `relay stop` says on success: what happened to the round.
func StopText(name string, res StopResult) string {
	switch res.Action {
	case "killed":
		return fmt.Sprintf("%s round %d stopped: process killed; round closed without a report unless one was on disk", name, res.Round)
	case "dequeued":
		return fmt.Sprintf("%s round %d stopped: dropped from the server queue before it started; round closed without a report", name, res.Round)
	default:
		return fmt.Sprintf("%s has no open round; nothing to stop", name)
	}
}

// HumanBytes renders n as a binary (1024-based) human-readable size, e.g.
// "512 B", "1.5 KiB", "3.0 MiB". It is the one implementation the CLI and the
// dry run share, so `relay db stats` and `relay send --dry-run` never disagree.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// RenderDryRun is what `relay send --dry-run` prints: the round it would open,
// the builder and where it would go, the paths, and the head of the prompt. It
// names only what the preflight read; nothing here was sent (#149).
func RenderDryRun(d DryRun) string {
	lines := []string{
		fmt.Sprintf("would send round %d to %s", d.Round, d.Name),
		fmt.Sprintf("  %-8s  %s", "builder", dryRunBuilderLine(d)),
		fmt.Sprintf("  %-8s  %s", "where", d.Where),
		fmt.Sprintf("  %-8s  %s", "tier", d.Tier),
		fmt.Sprintf("  %-8s  %s  (staged from %s, %s)", "plan", d.PlanPath, d.PlanFrom, HumanBytes(d.PlanBytes)),
		fmt.Sprintf("  %-8s  %s", "report", d.ReportPath),
		fmt.Sprintf("  %-8s  %s", "marker", d.DonePath),
	}
	first := ""
	if len(d.PromptHead) > 0 {
		first = d.PromptHead[0]
	}
	lines = append(lines, fmt.Sprintf("  %-8s  %s", "prompt", first))
	for _, cont := range d.PromptHead[1:] {
		lines = append(lines, "            "+cont)
	}
	return strings.Join(lines, "\n") + "\n"
}

// dryRunBuilderLine is the builder line's value: the mode and candidate, with
// the advisory gate note in parentheses when one applies.
func dryRunBuilderLine(d DryRun) string {
	line := d.Mode + " " + d.Candidate
	if d.GateNote != "" {
		line += "  (" + d.GateNote + ")"
	}
	return line
}

// UnbindText is what `relay unbind` says on success: one line for the
// binding, then at most one for its worktree, then at most one for a headless process.
func UnbindText(name string, res UnbindResult) string {
	var lines []string
	if res.ArchivedTo != "" {
		lines = append(lines, fmt.Sprintf("archived %s to %s", name, res.ArchivedTo))
	} else {
		lines = append(lines, fmt.Sprintf("unbound %s", name))
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
