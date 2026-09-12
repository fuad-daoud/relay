package relay

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestAnswerSendsKeysNotAPrompt(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	setBuilderStatus(f, herdr.StatusBlocked)
	f.prompts = nil

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter"}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if len(f.keys) != 1 || f.keys[0].Keys != "enter" || f.keys[0].Target != "w2:p4" {
		t.Fatalf("keys = %+v", f.keys)
	}
	if len(f.prompts) != 0 {
		t.Error("herdr rejects prompts against a blocked agent; answers must be keystrokes")
	}
}

func TestAnswerChoiceBecomesADigitKey(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := seedBound(t, f)
	setBuilderStatus(f, herdr.StatusBlocked)

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
	setBuilderStatus(f, herdr.StatusBlocked)

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

func TestAnswerAddressesTheLocatedPane(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	setBuilderStatus(f, herdr.StatusBlocked)

	if err := Answer(context.Background(), rt, "webshop", AnswerInput{Choice: 2}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if len(f.keys) != 1 {
		t.Fatalf("key calls = %d, want 1", len(f.keys))
	}
	if f.keys[0].Target != "w2:p4" {
		t.Fatalf("target = %q, want the located pane w2:p4", f.keys[0].Target)
	}
}

func TestAnswerFailsWhenBuilderIsGone(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)
	f.agents = []herdr.Agent{plannerAgent()} // the builder pane is gone

	err := Answer(context.Background(), rt, "webshop", AnswerInput{Choice: 2})
	if !errors.Is(err, ErrBuilderGone) {
		t.Fatalf("err = %v, want ErrBuilderGone", err)
	}
	if len(f.keys) != 0 {
		t.Fatal("no keys may be sent when the builder is gone")
	}
}

// TestParseAnswer pins spec §6: a positive integer is a dialog option, one of
// herdr's logical key names is a key, anything else is text. Whitespace and
// case never change the outcome.
func TestParseAnswer(t *testing.T) {
	cases := []struct {
		in   string
		want AnswerInput
	}{
		{"12", AnswerInput{Choice: 12}},
		{" 3 ", AnswerInput{Choice: 3}},
		{"0", AnswerInput{Text: "0"}},
		{"-1", AnswerInput{Text: "-1"}},
		{"enter", AnswerInput{Keys: "enter"}},
		{"ENTER", AnswerInput{Keys: "enter"}},
		{" esc ", AnswerInput{Keys: "esc"}},
		{"tab", AnswerInput{Keys: "tab"}},
		{"up", AnswerInput{Keys: "up"}},
		{"down", AnswerInput{Keys: "down"}},
		{"space", AnswerInput{Keys: "space"}},
		{"yes please", AnswerInput{Text: "yes please"}},
		{"y", AnswerInput{Text: "y"}},
		{"", AnswerInput{}},
	}
	for _, c := range cases {
		if got := ParseAnswer(c.in); got != c.want {
			t.Errorf("ParseAnswer(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
