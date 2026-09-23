package relevo

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// MaxPushBytes caps the file text a push carries. Far above any report
// relevo has produced; the full text stays one read away at the entry's
// Path, which every push path also carries as metadata.
const MaxPushBytes = 64 << 10

// PushText returns the text a push path should carry for e: the stored
// Payload (origin line included) for every kind that needs no file, and
// Payload + blank line + the file's contents for the kinds whose Path
// names a text artifact the planner would otherwise have to open.
//
// ok reports whether an expansion happened. A read error is not a
// delivery failure: PushText returns e.Payload and false, because an
// unreadable report is still a report that arrived.
//
// The origin line stays the first line of the result in every case;
// OpencodeDeliverer's confirmation query depends on it.
func PushText(e store.LogEntry, read func(string) ([]byte, error)) (string, bool) {
	if e.Path == "" || !expandablePushKind(e.Kind) {
		return e.Payload, false
	}

	raw, err := read(e.Path)
	if err != nil {
		return e.Payload, false
	}

	return e.Payload + "\n\n" + truncatePushText(string(raw), e.Path), true
}

// expandablePushKind reports whether e.Kind is a kind PushText expands.
// KindDiff and KindDrift are patches the planner reads with `relevo diff`;
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
// and appends a final line naming path when it truncated.
func truncatePushText(text, path string) string {
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

	return kept + fmt.Sprintf("[truncated at %d KiB -- full text at %s]", MaxPushBytes/1024, path)
}
