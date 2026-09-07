package relay

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestAnswerSendsKeysNotAPrompt(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	f.prompts = nil

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter"}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(f.keys) != 1 || f.keys[0].Keys != "enter" || f.keys[0].Target != "webshop-builder" {
		t.Fatalf("keys = %+v", f.keys)
	}
	if len(f.prompts) != 0 {
		t.Error("herdr rejects prompts against a blocked agent; answers must be keystrokes")
	}
}

func TestAnswerChoiceBecomesADigitKey(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Choice: 2}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if f.keys[0].Keys != "2" {
		t.Errorf("keys = %q, want \"2\"", f.keys[0].Keys)
	}
}

func TestAnswerLogsTheAnswer(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "esc"}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindAnswer || last.Direction != store.DirToBuilder {
		t.Fatalf("last entry = %+v", last)
	}
}

func TestAnswerRequiresExactlyOneInput(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{}); err == nil {
		t.Error("empty answer must be rejected")
	}
	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter", Text: "yes"}); err == nil {
		t.Error("two inputs at once must be rejected")
	}
}
