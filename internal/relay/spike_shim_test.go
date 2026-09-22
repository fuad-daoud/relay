package relay

// Test-only stand-ins for the types the deleted multiplexer package exported
// (SPIKE #303). Nothing in production reads them: tests that still build
// these values assert only on headless/remote behaviour, or were pruned.

type stubSession struct{ Agent, Kind, Value string }

type stubAgent struct {
	Name, Kind, Status, CWD string
	Focused                 bool
	PaneID, TabID           string
	WorkspaceID, Title      string
	Session                 stubSession
}

const (
	stubIdle    = "idle"
	stubWorking = "working"
	stubBlocked = "blocked"
	stubDone    = "done"
	stubUnknown = "unknown"
)

type integrationState struct {
	Installed, Outdated bool
	Detail              string
}

// plannerWith was deliver_test.go's planner fixture (SPIKE #303: the file
// tested pane delivery and is gone; the fixture is still used as filler).
func plannerWith(status string, focused bool) stubAgent {
	a := plannerAgent()
	a.Status = status
	a.Focused = focused
	return a
}
