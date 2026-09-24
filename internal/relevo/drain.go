package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Pusher is the channel's outbound side; internal/mcp implements it by
// writing a notifications/claude/channel line to its stdio transport.
type Pusher interface {
	Push(ctx context.Context, content string, meta map[string]string) error
}

// DrainState is one relevo mcp poll loop's memory across polls: which planner
// it drains, and each of that planner's bindings' last-seen State, so Drain
// knows when a state event is a transition rather than a repeat.
type DrainState struct {
	Planner string
	Last    map[string]store.State // binding name -> last-seen state; nil until first poll
}

// DrainResult is what one Drain call did.
type DrainResult struct {
	Pushed int      // payload events pushed and confirmed
	States int      // state events pushed
	Failed []string // binding names whose push returned an error (left pending)
}

// Drain runs one poll over one planner's bindings (spec
// docs/specs/2026-09-21-planner-channel-design.md §3.4-§3.6, #303 §4.5): for
// each binding whose planner is st.Planner and which is not remote-owned, it
// pushes the oldest pending planner payload (confirming only after the push
// succeeds), then pushes a state event on selected state transitions.
func Drain(ctx context.Context, rt Runtime, st *DrainState, p Pusher) (DrainResult, error) {
	if st.Planner == "" {
		return DrainResult{}, fmt.Errorf("drain: empty planner")
	}
	if rt.Store == nil {
		return DrainResult{}, fmt.Errorf("drain: nil store")
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return DrainResult{}, err
	}

	var mine []store.Binding
	for _, b := range bindings {
		if b.PlannerID == st.Planner && b.Owner == "" {
			mine = append(mine, b)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })

	if st.Last == nil {
		st.Last = map[string]store.State{}
	}

	var res DrainResult
	seen := make(map[string]bool, len(mine))

	for _, b := range mine {
		seen[b.Name] = true

		var (
			entry store.LogEntry
			idx   int
			found bool
		)
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			entry, idx, found, err = tx.PendingForPlanner(b.Name)
			return err
		}); err != nil {
			return res, err
		}

		if found {
			meta := map[string]string{
				"binding": b.Name,
				"round":   strconv.Itoa(entry.Round),
				"kind":    string(entry.Kind),
				"seq":     strconv.Itoa(entry.Seq),
			}
			if entry.Path != "" {
				meta["path"] = entry.Path
			}

			content, _ := PushText(entry, rt.Store.ReadFile)
			if err := p.Push(ctx, content, meta); err != nil {
				res.Failed = append(res.Failed, b.Name)
				slog.Info("channel push failed; entry stays pending", "binding", b.Name, "round", entry.Round, "error", err)
			} else {
				if err := rt.Store.WithLock(func(tx *store.Tx) error {
					return tx.ConfirmIndex(b.Name, idx, "channel")
				}); err != nil {
					return res, err
				}
				res.Pushed++
			}
		}

		prev, wasSeen := st.Last[b.Name]
		if (!wasSeen || prev != b.State) && isChannelState(b.State) {
			oldState := ""
			if wasSeen {
				oldState = string(prev)
			}
			meta := map[string]string{
				"binding":   b.Name,
				"round":     strconv.Itoa(b.Round),
				"kind":      "state",
				"state":     string(b.State),
				"old_state": oldState,
			}
			if err := p.Push(ctx, stateEventContent(b), meta); err != nil {
				slog.Info("channel state push failed", "binding", b.Name, "state", b.State, "error", err)
			} else {
				res.States++
			}
		}
		st.Last[b.Name] = b.State
	}

	// Bindings that disappeared from the store are dropped from memory: a
	// deleted-and-recreated binding of the same name announces its state
	// fresh, the same as a restarted relevo mcp would.
	for name := range st.Last {
		if !seen[name] {
			delete(st.Last, name)
		}
	}

	return res, nil
}

// isChannelState reports whether s is one of the states worth a push
// (spec §3.6): needs_you and broken.
func isChannelState(s store.State) bool {
	switch s {
	case store.StateNeedsYou, store.StateBroken:
		return true
	default:
		return false
	}
}

// stateEventContent is the human-readable body of a state event (spec
// §3.5): the state in the reader's words, plus the binding's Halt reason
// when it has one, plus the next steps.
func stateEventContent(b store.Binding) string {
	var reason string
	if b.Halt != "" {
		reason = " -- " + b.Halt
	}
	return fmt.Sprintf("%s round %d: %s%s\nrun relevo status --name %s, then send or stop.",
		b.Name, b.Round, channelStateLabel(b.State), reason, b.Name)
}

func channelStateLabel(s store.State) string {
	switch s {
	case store.StateNeedsYou:
		return "NEEDS YOU"
	case store.StateBroken:
		return "BROKEN"
	default:
		return strings.ToUpper(string(s))
	}
}
