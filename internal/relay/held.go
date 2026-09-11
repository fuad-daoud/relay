package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// DefaultHeldGrace is the quiet period after which a held payload is injected
// into a focused planner whose input box relay cannot prove empty.
const DefaultHeldGrace = 60 * time.Second

// heldScreenLines bounds the visible-screen read. The input box is at the
// bottom, and a small window keeps scrolled-off transcript out of the
// fingerprint so old output cannot mask a change in the box.
const heldScreenLines = 40

// inputEmpty reports whether the planner's input box, as drawn on its visible
// screen, has nothing typed in it. known is false when relay has no marker
// for this harness kind, or the marker is not on screen; an unknown answer
// must fall through to the grace path, never be read as empty.
func inputEmpty(kind, screen string) (empty, known bool) {
	if kind != "claude" {
		// agy and opencode idle screens have not been captured, so guessing a
		// marker is worse than none: an unknown answer takes the grace path.
		return false, false
	}

	// Scan from the bottom: the input box sits at the foot of the screen, and
	// transcript above it can contain markdown quotes that begin with `>`.
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimLeft(lines[i], " \t")

		var rest string
		switch {
		case strings.HasPrefix(trimmed, "❯"):
			rest = strings.TrimPrefix(trimmed, "❯")
		case strings.HasPrefix(trimmed, ">"): // older claude builds
			rest = strings.TrimPrefix(trimmed, ">")
		default:
			continue
		}
		return strings.TrimSpace(rest) == "", true
	}

	return false, false
}

// fingerprint hashes one terminal snapshot so two reads can be compared
// without keeping the text.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// clearPlannerScreen ends a fingerprint hold. Every DeliverPending return
// that is not a fingerprint hold goes through here, so PlannerScreen is
// never stale. HeldGrace goes with them: it describes the clock, and there is
// no clock.
func clearPlannerScreen(b store.Binding) store.Binding {
	b.PlannerScreen = ""
	b.PlannerScreenAt = time.Time{}
	b.HeldGrace = 0
	return b
}

// plannerHold decides whether a payload held on a FOCUSED, idle planner may
// be injected now. It reads the planner's visible screen and applies the
// two signals in order: an input box known to be empty injects at once;
// otherwise the screen is fingerprinted and injection waits until it has
// been unchanged for HeldGrace. The returned binding carries the fingerprint
// state and must be persisted whether or not inject is true.
//
// A failed read holds with the fingerprint untouched: a read failure is not
// evidence of anything, and the next tick retries.
func plannerHold(ctx context.Context, rt Runtime, b store.Binding, planner herdr.Agent) (next store.Binding, inject bool, reason string) {
	grace := rt.HeldGrace
	if grace <= 0 {
		grace = DefaultHeldGrace
	}

	screen, err := rt.Herdr.ReadAgentSource(ctx, planner.PaneID, "visible", heldScreenLines)
	if err != nil {
		// A read failure is not evidence of anything, so hold with the
		// fingerprint untouched and let the next tick retry.
		return b, false, "planner pane is focused; screen unreadable"
	}

	empty, known := inputEmpty(planner.Kind, screen)
	if known && empty {
		// An empty box has nothing to clobber: proof beats inference, so the
		// detector short-circuits ahead of the grace clock.
		return b, true, "planner focused, input empty"
	}

	// The fingerprint hashes the last heldScreenLines lines of the visible
	// source: recent-unwrapped is scrollback and alternate-screen rows never
	// reach it, and detection is herdr's matching buffer, not a stable image.
	// Known limitation: a harness that redraws a clock inside that window
	// resets the grace every tick. The detector is the primary path; if this
	// bites, narrow the hashed region to the lines from the marker down
	// rather than widen the design.
	fp := fingerprint(screen)
	now := rt.Now().UTC()
	if b.PlannerScreen == "" || fp != b.PlannerScreen {
		// First look, or the screen moved since it: the human may be typing,
		// so (re)start the grace clock from now.
		b.PlannerScreen, b.PlannerScreenAt = fp, now
		b.HeldGrace = grace
		return b, false, "planner pane is focused; screen changing"
	}

	quiet := now.Sub(b.PlannerScreenAt)
	if quiet >= grace {
		return b, true, fmt.Sprintf("planner focused, quiet for %s", grace)
	}
	return b, false, fmt.Sprintf("planner pane is focused; quiet %s of %s",
		quiet.Truncate(time.Second), grace)
}
