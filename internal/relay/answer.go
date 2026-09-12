package relay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// AnswerInput is how the planner answers a builder's dialog. Exactly one field
// must be set.
type AnswerInput struct {
	Keys   string
	Text   string
	Choice int
}

// DialogSource and DialogLines are how relay reads a blocking dialog. A TUI
// approval prompt is drawn on the alternate screen, which never reaches the
// scrollback recent-unwrapped reads, so the dialog has to come from detection
// instead. The daemon's blocked handler and the answer picker use the same
// pair so the human sees what the daemon saw.
const (
	DialogSource = "detection"
	DialogLines  = 200
)

// logicalKeys are the key names herdr send-keys accepts by name. Anything
// else typed at the answer picker is literal text.
var logicalKeys = map[string]bool{
	"enter": true, "esc": true, "tab": true, "up": true, "down": true, "space": true,
}

// ParseAnswer turns one typed line into an AnswerInput (spec §6). A positive
// integer is a numbered dialog option; a logical key name is a key; anything
// else is text as typed, trimmed. Zero and negatives are not options and fall
// through to text -- AnswerInput.resolve treats Choice 0 as unset.
func ParseAnswer(s string) AnswerInput {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return AnswerInput{Choice: n}
	}
	if k := strings.ToLower(s); logicalKeys[k] {
		return AnswerInput{Keys: k}
	}
	return AnswerInput{Text: s}
}

func (a AnswerInput) resolve() (string, error) {
	set := 0
	if a.Keys != "" {
		set++
	}
	if a.Text != "" {
		set++
	}
	if a.Choice != 0 {
		set++
	}

	switch {
	case set == 0:
		return "", errors.New("answer needs one of --keys, --text or --choice")
	case set > 1:
		return "", errors.New("answer takes exactly one of --keys, --text or --choice")
	case a.Keys != "":
		return a.Keys, nil
	case a.Choice != 0:
		return strconv.Itoa(a.Choice), nil
	default:
		return a.Text, nil
	}
}

// Answer sends the planner's decision into a blocked builder as keystrokes.
// herdr refuses agent prompt against a blocked agent, so send-keys is the only
// channel that works here.
func Answer(ctx context.Context, rt Runtime, name string, in AnswerInput) error {
	keys, err := in.resolve()
	if err != nil {
		return err
	}

	var builder herdr.Agent
	var locatedBuilder bool
	if hint, err := rt.Store.Load(name); err == nil {
		agents, err := rt.Herdr.ListAgents(ctx)
		if err != nil {
			return fmt.Errorf("list agents: %w", err)
		}
		var ok bool
		builder, ok = FindAgent(agents, hint.Builder)
		if !ok {
			return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, hint.Builder.PaneID, hint.BuilderCandidate, ErrBuilderGone)
		}

		// Refuse unless herdr still reports the builder blocked. relay sets
		// NEEDS YOU and prints an answer instruction whenever herdr's screen
		// detection fires, including on a false positive (#55), and the
		// planner is a model following that instruction -- so the guard has to
		// be here, where the keystrokes are, not in the prose.
		//
		// This deliberately uses the list fetched above rather than the
		// daemon's snapshot: the window between the notification and the
		// answer is unbounded, and a genuine block may have resolved itself
		// while the human was reading.
		//
		// No --force. Anyone who really means to type into a running agent has
		// `herdr agent send-keys <pane> <keys>`; relay does not need an escape
		// hatch whose only purpose is to defeat the guard it just added.
		if builder.Status != herdr.StatusBlocked {
			return fmt.Errorf("binding %q (pane %s): herdr reports the builder %s: %w",
				name, builder.PaneID, builder.Status, ErrBuilderNotBlocked)
		}
		locatedBuilder = true
	}

	// Load-modify-save, so it runs inside the state lock: the daemon rewrites
	// this binding on every tick and would otherwise clobber the state change
	// that records the builder is no longer waiting on a human.
	return rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		// The pre-lock load and this locked load are two separate acquisitions
		// of the state lock, so a binding can appear between them. An unlocated
		// builder must never fall through to an empty target.
		if !locatedBuilder {
			return fmt.Errorf("binding %q: %w", name, ErrBuilderGone)
		}
		if !SameAgent(builder, b.Builder) {
			return fmt.Errorf("binding %q (pane %s, candidate %s): %w", name, b.Builder.PaneID, b.BuilderCandidate, ErrBuilderGone)
		}

		if err := rt.Herdr.SendKeys(ctx, builder.PaneID, keys); err != nil {
			return fmt.Errorf("send keys to builder: %w", err)
		}

		entry := store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round,
			Direction: store.DirToBuilder, Kind: store.KindAnswer,
			Payload: keys, Confirmed: true,
		}
		if err := tx.AppendLog(name, entry); err != nil {
			return err
		}

		b.State = store.StateActive

		return tx.Save(b)
	})
}
