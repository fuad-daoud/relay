package relay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrHeadlessNoDialog: a headless builder (#99) runs with stdin closed and
// no terminal; there is no dialog to answer. What it printed is in its log.
var ErrHeadlessNoDialog = errors.New("builder is headless and takes no dialogs")

// ErrRemoteNoDialog: a remote builder (#100) runs on someone else's server
// with no pane relay can send keys into. §4.6.
var ErrRemoteNoDialog = errors.New("remote builders take no dialogs")

// AnswerInput is how the planner answers a builder's dialog. Exactly one field
// must be set.
type AnswerInput struct {
	Keys   string
	Text   string
	Choice int
}

// logicalKeys are the key names a dialog answer accepts by name. Anything
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

// Answer used to send the planner's decision into a blocked pane builder as
// keystrokes. Headless builders run with stdin closed and remote ones on
// another machine, so no builder relay runs can take a dialog answer.
//
// SPIKE(decision): with pane builders gone, `relay answer`, the MCP answer
// tool and the picker's answer flow have no target. Delete them (and
// AnswerInput/ParseAnswer) or keep the verb as this refusal.
func Answer(ctx context.Context, rt Runtime, name string, in AnswerInput) error {
	if _, err := in.resolve(); err != nil {
		return err
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return err
	}
	if b.Builder.Remote() {
		return fmt.Errorf("%s: %w", name, ErrRemoteNoDialog)
	}
	where := "no round is running"
	if b.Builder.LogPath != "" {
		where = "read " + b.Builder.LogPath
	}
	return fmt.Errorf("%s's %w; %s", name, ErrHeadlessNoDialog, where)
}
