package relay

import (
	"context"
	"errors"
	"fmt"
	"strconv"

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
			return fmt.Errorf("binding %q (pane %s, alias %s): %w", name, hint.Builder.PaneID, hint.BuilderAlias, ErrBuilderGone)
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

		if locatedBuilder && !SameAgent(builder, b.Builder) {
			return fmt.Errorf("binding %q (pane %s, alias %s): %w", name, b.Builder.PaneID, b.BuilderAlias, ErrBuilderGone)
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
