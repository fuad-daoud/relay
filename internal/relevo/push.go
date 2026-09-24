package relevo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// MaxPushBytes caps the file text a push carries. Far above any report
// relevo has produced; the full text stays one `relevo show` command away,
// which every push path also carries as metadata.
const MaxPushBytes = 64 << 10

// showCommand renders the planner-facing command that prints one round's
// artifact (P4a round 2 §4.2). name is the binding, round the round the
// artifact belongs to, and section show's own flag that reads it. It replaces
// the state-dir path the payloads and hints used to name: a closed round's
// files may be sealed into the database (P3c), so the command is the durable
// way to name them.
func showCommand(name string, round int, section string) string {
	return fmt.Sprintf("relevo show %s --round %d --%s", name, round, section)
}

// findingsCommand is showCommand's findings form, which names the consult:
// `relevo show <name> --round <n> --findings <id>`.
func findingsCommand(name string, round int, id string) string {
	return fmt.Sprintf("relevo show %s --round %d --findings %s", name, round, id)
}

// findingsIDOf recovers the consult id from a findings path's basename
// (NNN-<id>-findings.md) -- the one identifier a findings entry carries, since
// LogEntry.Path stays the artifact's identifier (§3). It returns "" when the
// basename is not a findings name.
func findingsIDOf(path string) string {
	base := filepath.Base(path)
	const suffix = "-findings.md"
	if !strings.HasSuffix(base, suffix) {
		return ""
	}
	base = strings.TrimSuffix(base, suffix)
	i := strings.IndexByte(base, '-')
	if i < 0 {
		return ""
	}
	return base[i+1:]
}

// logRef renders the `relevo show` command that prints e's full artifact, or
// "" when e's kind names none (an edge's prompt file has no show section).
func logRef(name string, e store.LogEntry) string {
	switch e.Kind {
	case store.KindReport:
		return showCommand(name, e.Round, "report")
	case store.KindFindings:
		if id := findingsIDOf(e.Path); id != "" {
			return findingsCommand(name, e.Round, id)
		}
	case store.KindDiff:
		return showCommand(name, e.Round, "diff")
	case store.KindDrift:
		return showCommand(name, e.Round, "drift")
	}
	return ""
}

// PushText returns the text a push path should carry for e: the stored
// Payload (origin line included) for every kind that needs no file, and
// Payload + blank line + the file's contents for the kinds whose Path
// names a text artifact the planner would otherwise have to open. name is
// the binding the entry belongs to, threaded so a truncated text can name
// the `relevo show` command that prints the whole thing (§4.2).
//
// ok reports whether an expansion happened. A read error is not a
// delivery failure: PushText returns e.Payload and false, because an
// unreadable report is still a report that arrived.
//
// The origin line stays the first line of the result in every case;
// OpencodeDeliverer's confirmation query depends on it.
func PushText(e store.LogEntry, name string, read func(string) ([]byte, error)) (string, bool) {
	if e.Path == "" || !expandablePushKind(e.Kind) {
		return e.Payload, false
	}

	raw, err := read(e.Path)
	if err != nil {
		return e.Payload, false
	}

	ref := logRef(name, e)
	if ref == "" {
		// A kind with no show section (an edge's prompt file): fall back to
		// the path rather than printing an empty reference.
		ref = e.Path
	}

	return e.Payload + "\n\n" + truncatePushText(string(raw), ref), true
}

// expandablePushKind reports whether e.Kind is a kind PushText expands.
// KindDiff and KindDrift are patches the planner reads with `relevo show`;
// every other kind's payload is already its whole text.
func expandablePushKind(k store.Kind) bool {
	switch k {
	case store.KindReport, store.KindFindings, store.KindEdge:
		return true
	default:
		return false
	}
}

// truncatePushText keeps at most MaxPushBytes of text, cut back to the last
// newline within that budget (or at the budget itself when there is none),
// and appends a final line naming ref -- the `relevo show` command that
// prints the full text (§4.2) -- when it truncated.
func truncatePushText(text, ref string) string {
	if len(text) <= MaxPushBytes {
		return text
	}

	budget := text[:MaxPushBytes]
	kept := budget
	if cut := strings.LastIndexByte(budget, '\n'); cut >= 0 {
		kept = text[:cut+1]
	}
	if !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}

	return kept + fmt.Sprintf("[truncated at %d KiB -- full text: %s]", MaxPushBytes/1024, ref)
}
