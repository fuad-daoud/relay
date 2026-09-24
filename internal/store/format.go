package store

import (
	"errors"
	"fmt"
)

// BindingFormat is the format of the Binding JSON this binary writes. Format 1
// is today's shape and is stored as an *absent* "format" field, so a binding
// saved today stays byte-identical to one saved before the field existed; from
// format 2 on the number is written. Format 2 adds "role".
//
// Bump it whenever Binding's JSON shape changes. An older relevo that meets a
// newer format refuses to save, because its rewrite would erase every field it
// does not know (#372).
const BindingFormat = 2

// recordFormat is the format to write b at: the lowest format that can hold
// the record (#382 §5.2). A binding whose Role is empty is format 1, so it is
// byte-identical to a binding written before the field existed; any other role
// is format 2. An older relevo then refuses to save exactly the bindings it
// would get wrong, and keeps working on every other one.
func recordFormat(b Binding) int {
	if b.Role != "" {
		return 2
	}
	return 1
}

// storedFormat is the number actually written for a known format: 1 is stored
// as 0 so that the "format" key is omitted, and every other format is written
// as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}

// ErrNewerFormat reports a binding or planner record written by a relevo that
// knows a newer format. Writing it back would erase the fields this relevo does
// not understand, so callers refuse instead, and nothing is written.
type ErrNewerFormat struct {
	// Kind names the record: "binding" or "planner record".
	Kind string
	Name string
	// Have is the format on disk; Know is the format this relevo writes.
	Have int
	Know int
}

// ErrNewerFormatSentinel is what errors.Is matches for every ErrNewerFormat,
// whatever its Kind, Name or formats.
var ErrNewerFormatSentinel = errors.New("relevo: newer format")

func (e *ErrNewerFormat) Error() string {
	return fmt.Sprintf("%s %q was written by a newer relevo (format %d; this relevo knows %d): upgrade relevo; a planner session reconnects relevo mcp with /mcp",
		e.Kind, e.Name, e.Have, e.Know)
}

// Is makes errors.Is(err, ErrNewerFormatSentinel) match any ErrNewerFormat.
func (e *ErrNewerFormat) Is(target error) bool {
	return target == ErrNewerFormatSentinel
}

// KnownState reports whether s is a state this relevo defines. A false answer
// means a newer relevo wrote it, so the daemon leaves the binding alone rather
// than reconciling an unknown state as live.
func KnownState(s State) bool {
	switch s {
	case StateActive, StateNeedsYou, StateBroken, StateDone, StatePaused:
		return true
	}
	return false
}
