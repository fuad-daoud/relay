package relay

import (
	"context"
	"testing"
)

func TestAnswerRequiresExactlyOneInput(t *testing.T) {
	f := &fakePanes{}
	rt, b := seedBound(t, f)

	if err := Answer(context.Background(), rt, b.Name, AnswerInput{}); err == nil {
		t.Error("empty answer must be rejected")
	}
	if err := Answer(context.Background(), rt, b.Name, AnswerInput{Keys: "enter", Text: "yes"}); err == nil {
		t.Error("two inputs at once must be rejected")
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
