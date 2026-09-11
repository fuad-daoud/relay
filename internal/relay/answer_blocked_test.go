package relay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
)

// setBuilderStatus moves the seeded builder's pane to status. seedBound leaves
// it `working`, which is the whole problem this file is about.
func setBuilderStatus(f *fakeHerdr, status string) {
	for i := range f.agents {
		if f.agents[i].PaneID == "w2:p4" {
			f.agents[i].Status = status
		}
	}
}

// TestAnswerRefusesABuilderThatIsNotBlocked pins #55. Answer located the
// builder, verified its identity with SameAgent, and sent keystrokes without
// ever asking whether it was at a dialog. FindAgent returns an agent carrying
// Status and Answer threw it away.
//
// The damage is not a stray keystroke. When herdr's screen detection
// false-positives, relay captures a question, sets NEEDS YOU, and prints
// "answer with: relay answer --keys ...". The planner is a model following
// relay's own instruction, so relay is telling it to type into an agent that
// is mid-turn.
func TestAnswerRefusesABuilderThatIsNotBlocked(t *testing.T) {
	for _, status := range []string{
		herdr.StatusWorking,
		herdr.StatusIdle,
		herdr.StatusDone,
		herdr.StatusUnknown,
	} {
		t.Run(status, func(t *testing.T) {
			f := &fakeHerdr{}
			rt, b := seedBound(t, f)
			setBuilderStatus(f, status)
			f.keys = nil

			err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter"})

			if !errors.Is(err, ErrBuilderNotBlocked) {
				t.Fatalf("Answer against a %q builder = %v, want ErrBuilderNotBlocked", status, err)
			}
			if len(f.keys) != 0 {
				t.Errorf("relay typed %+v into a builder that was %q, not at a dialog", f.keys, status)
			}
		})
	}
}

// TestAnswerStillWorksOnABlockedBuilder is the other half: the guard must
// refuse the wrong case without breaking the case relay answer exists for.
func TestAnswerStillWorksOnABlockedBuilder(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	setBuilderStatus(f, herdr.StatusBlocked)
	f.keys = nil

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter"}); err != nil {
		t.Fatalf("Answer against a blocked builder: %v", err)
	}
	if len(f.keys) != 1 || f.keys[0].Keys != "enter" || f.keys[0].Target != "w2:p4" {
		t.Fatalf("keys = %+v, want one \"enter\" to w2:p4", f.keys)
	}
}

// TestAnswerNamesTheStatusItSaw keeps the refusal diagnosable. "nothing to
// answer" without the observed status sends the reader back to herdr to work
// out why, which is the moment they are most likely to reach for send-keys.
func TestAnswerNamesTheStatusItSaw(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	setBuilderStatus(f, herdr.StatusWorking)

	err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter"})
	if err == nil {
		t.Fatal("Answer against a working builder succeeded")
	}
	if got := err.Error(); !strings.Contains(got, herdr.StatusWorking) || !strings.Contains(got, b.Name) {
		t.Errorf("error %q must name the binding and the status it observed", got)
	}
}
