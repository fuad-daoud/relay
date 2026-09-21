package relay

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// Token names relay reports (#129). README's sidebar snippet uses them.
const (
	TokenName  = "relay"
	TokenRound = "relay_round"
	TokenState = "relay_state"
)

// appliedMeta is what the daemon last reported to herdr for one binding
// (#129): the pane it wrote to, the token fingerprint, and when. In memory
// only: a daemon restart re-applies everything on its first tick, and
// metadataRefresh bounds how stale a herdr restart can leave a pane.
type appliedMeta struct {
	pane string
	fp   string
	at   time.Time
}

// metadataRefresh is how often an unchanged token set is re-reported anyway.
const metadataRefresh = 60 * time.Second

// tokenState maps a stored state onto the relay_state token: the display
// state lower-cased with spaces as hyphens, so the README rules read like
// `relay status` does. Total: every store.State maps.
func tokenState(s store.State) string {
	switch s {
	case store.StateHeld:
		return "held"
	case store.StateNeedsYou, store.StateBroken, store.StateOrphaned:
		return "needs-you"
	case store.StatePaused:
		return "paused"
	case store.StateDone:
		return "done"
	default:
		return "active"
	}
}

// PaneTokens is the token set for a binding, or (nil, false) when the
// binding has no pane to tag: a headless or remote builder, a served
// binding (b.Serve != nil), or an empty Builder.PaneID.
func PaneTokens(b store.Binding) (tokens map[string]string, ok bool) {
	if b.Builder.Headless() || b.Builder.Remote() || b.Serve != nil || b.Builder.PaneID == "" {
		return nil, false
	}
	return map[string]string{
		TokenName:  b.Name,
		TokenRound: fmt.Sprintf("%03d", b.Round),
		TokenState: tokenState(b.State),
	}, true
}

// TokenFingerprint is a stable string of a token set, for the dedup map.
func TokenFingerprint(tokens map[string]string) string {
	names := make([]string, 0, len(tokens))
	for name := range tokens {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+tokens[name])
	}
	return strings.Join(parts, "\x1f")
}

// clearTokens is the ReportMetadata that removes relay's three tokens.
func clearTokens(seq int64) herdr.PaneMetadata {
	return herdr.PaneMetadata{
		ClearTokens: []string{TokenName, TokenRound, TokenState},
		Seq:         seq,
	}
}

// syncPaneMetadata reports tokens for every binding whose set changed (or
// whose last report is older than metadataRefresh), clears tokens for a
// DONE binding and for every name in applied that bindings no longer
// contains, and updates applied. Errors are slog.Warn per binding and never
// fail the tick. Seq is rt.Now().UnixMilli().
func syncPaneMetadata(ctx context.Context, rt Runtime, applied map[string]appliedMeta, bindings []store.Binding) {
	now := rt.Now()
	seq := now.UnixMilli()
	seen := make(map[string]struct{}, len(bindings))

	for _, b := range bindings {
		seen[b.Name] = struct{}{}

		tokens, ok := PaneTokens(b)
		if !ok {
			continue
		}

		if b.State == store.StateDone {
			if prev, had := applied[b.Name]; had {
				if err := rt.Herdr.ReportMetadata(ctx, prev.pane, clearTokens(seq)); err != nil {
					slog.Warn("clear pane tokens", "binding", b.Name, "pane", prev.pane, "err", err)
				}
				delete(applied, b.Name)
			}
			continue
		}

		fp := TokenFingerprint(tokens)
		prev, had := applied[b.Name]
		if had && prev.pane == b.Builder.PaneID && prev.fp == fp && now.Sub(prev.at) < metadataRefresh {
			continue
		}

		if had && prev.pane != b.Builder.PaneID {
			// The builder switched panes: the old pane keeps stale tokens
			// forever unless we clear them ourselves.
			if err := rt.Herdr.ReportMetadata(ctx, prev.pane, clearTokens(seq)); err != nil {
				slog.Warn("clear pane tokens on switch", "binding", b.Name, "pane", prev.pane, "err", err)
			}
		}

		if err := rt.Herdr.ReportMetadata(ctx, b.Builder.PaneID, herdr.PaneMetadata{Tokens: tokens, Seq: seq}); err != nil {
			slog.Warn("report pane tokens", "binding", b.Name, "pane", b.Builder.PaneID, "err", err)
			continue // not remembered: retried next tick
		}
		applied[b.Name] = appliedMeta{pane: b.Builder.PaneID, fp: fp, at: now}
	}

	for name, prev := range applied {
		if _, ok := seen[name]; ok {
			continue
		}
		if err := rt.Herdr.ReportMetadata(ctx, prev.pane, clearTokens(seq)); err != nil {
			slog.Warn("clear pane tokens on vanish", "binding", name, "pane", prev.pane, "err", err)
		}
		delete(applied, name)
	}
}
