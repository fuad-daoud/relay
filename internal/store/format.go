package store

import (
	"errors"
	"fmt"
)

// BindingFormat is the format of the Binding JSON this binary writes. Format 1
// is stored as an *absent* "format" field, so a binding saved today is
// byte-identical to one saved before the field existed; from format 2 on the
// number is written.
//
// Later fields (builder.remote_live, builder.stream_start,
// builder.stream_segments, abandoned_sessions, oom_requeue,
// round_oom_kills) are fields recordFormat never stamps: an older relevo that
// drops builder.remote_live, builder.stream_start or builder.stream_segments
// loses nothing, while one that drops abandoned_sessions loses only pending
// session deletes, and one that drops oom_requeue or round_oom_kills loses
// only oom-kill tracking for in-flight rounds. Stamping a field would lock
// that older relevo out of loading the binding. Bump BindingFormat whenever
// Binding's JSON shape changes; an older relevo that meets a newer format
// refuses to save, because its rewrite would erase fields it does not know.
const BindingFormat = 7

// recordFormat is the format to write b at: the lowest format that holds the
// record. A binding whose Role is empty is format 1; any other role is format
// 2, so an older relevo refuses exactly the bindings it would get wrong.
func recordFormat(b Binding) int {
	if b.Role != "" {
		return 2
	}
	return 1
}

// storedFormat is the number written for a known format: 1 becomes 0 so the
// "format" key is omitted, and every other format is written as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}

// ErrNewerFormat reports a binding or planner record written by a relevo that
// knows a newer format; writing it back would erase fields this relevo does
// not understand, so callers refuse instead.
type ErrNewerFormat struct {
	Kind string
	Name string
	Have int
	Know int
}

var ErrNewerFormatSentinel = errors.New("relevo: newer format")

func (e *ErrNewerFormat) Error() string {
	return fmt.Sprintf("%s %q was written by a newer relevo (format %d; this relevo knows %d): upgrade relevo; a planner session reconnects relevo mcp with /mcp",
		e.Kind, e.Name, e.Have, e.Know)
}

func (e *ErrNewerFormat) Is(target error) bool {
	return target == ErrNewerFormatSentinel
}

// KnownState reports whether s is a state this relevo defines; false means a
// newer relevo wrote it, so the daemon leaves the binding alone.
func KnownState(s State) bool {
	switch s {
	case StateActive, StateNeedsYou, StateBroken, StateDone, StatePaused:
		return true
	}
	return false
}
