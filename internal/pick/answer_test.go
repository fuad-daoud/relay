package pick

import (
	"context"
	"errors"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
)

func typeString(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// answerModelFor puts the model on the answer screen for name, with the
// dialog already read, the way the list would after Enter.
func answerModelFor(t *testing.T, fh *fakeHerdr, name string) (Model, relay.Runtime) {
	t.Helper()
	rt := testRuntime(t, fh, testBinding(name))
	m := newModel(context.Background(), rt, Options{Verb: VerbAnswer})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(t, m, rowsMsg(row(name, "NEEDS YOU", "blocked"), row("other", "NEEDS YOU", "blocked")))
	m, _ = update(t, m, key("enter")) // picks name (cursor 0)
	return m, rt
}

func TestSingleBlockedRowSkipsTheList(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readOut = "Allow rm?\n 1. Yes\n 2. No"
	rt := testRuntime(t, fh, testBinding("webshop"))
	m := newModel(context.Background(), rt, Options{Verb: VerbAnswer})
	m, cmd := update(t, m, rowsMsg(row("webshop", "NEEDS YOU", "blocked")))
	if m.screen != screenAnswer || m.answer.name != "webshop" {
		t.Fatalf("screen=%v name=%q; one blocked row should open the answer screen directly", m.screen, m.answer.name)
	}
	if cmd == nil {
		t.Fatal("entering the answer screen must fetch the dialog")
	}
}

func TestTwoBlockedRowsShowTheList(t *testing.T) {
	m := newModel(context.Background(), relay.Runtime{}, Options{Verb: VerbAnswer})
	m, _ = update(t, m, rowsMsg(row("a", "NEEDS YOU", "blocked"), row("b", "NEEDS YOU", "blocked")))
	if m.screen != screenList {
		t.Fatalf("screen=%v, want the list", m.screen)
	}
}

func TestFetchDialogReadsTheBuilderLive(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readOut = "Allow rm?\n 1. Yes\n 2. No"
	rt := testRuntime(t, fh, testBinding("webshop"))
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.err != nil || msg.text != fh.readOut || msg.round != 2 {
		t.Fatalf("msg = %+v", msg)
	}
	if len(fh.reads) != 1 || fh.reads[0] != relay.Target(testBinding("webshop").Builder) {
		t.Fatalf("reads = %v, want one read of the builder", fh.reads)
	}
}

func TestFetchDialogFallsBackToTheQuestionFile(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readErr = errors.New("herdr: read timed out")
	rt := testRuntime(t, fh, testBinding("webshop"))
	path := rt.Store.QuestionPath("webshop", 2)
	if err := os.WriteFile(path, []byte("stale dialog"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.text != "stale dialog" {
		t.Fatalf("text = %q, want the question file", msg.text)
	}
	if msg.err == nil {
		t.Fatal("the read error must still be reported so the human knows the text may be stale")
	}
}

func TestFetchDialogReportsWhenNeitherSourceWorks(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.readErr = errors.New("herdr: read timed out")
	rt := testRuntime(t, fh, testBinding("webshop"))
	msg := fetchDialog(context.Background(), rt, "webshop")().(dialogMsg)
	if msg.err == nil || msg.text != "" {
		t.Fatalf("msg = %+v, want an error and no text", msg)
	}
}

func TestAnswerRefusesEmptyInput(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m, cmd := update(t, m, key("enter"))
	if cmd != nil || m.screen != screenAnswer {
		t.Fatal("empty input must not submit")
	}
	if m.answer.refuse != "type a key name, a number or text" {
		t.Fatalf("refusal = %q", m.answer.refuse)
	}
	m = typeString(t, m, "2")
	if m.answer.refuse != "" {
		t.Fatal("typing must clear the refusal")
	}
}

func TestAnswerEnterSendsTheParsedAnswer(t *testing.T) {
	fh := newFakeHerdr(t)
	fh.agents = []herdr.Agent{blockedAgent("webshop")}
	m, _ := answerModelFor(t, fh, "webshop")
	m = typeString(t, m, "2")
	m, cmd := update(t, m, key("enter"))
	if m.screen != screenResult || !m.result.pending || cmd == nil {
		t.Fatalf("enter with input should run the verb: screen=%v pending=%v", m.screen, m.result.pending)
	}
	m, _ = update(t, m, cmd())
	if m.result.err != nil {
		t.Fatalf("answer: %v", m.result.err)
	}
	if m.result.text != relay.AnswerText("webshop") {
		t.Fatalf("text = %q", m.result.text)
	}
	if len(fh.keys) != 1 || fh.keys[0].Keys != "2" || fh.keys[0].Target != "w1:p2" {
		t.Fatalf("keys = %+v, want \"2\" to the builder pane", fh.keys)
	}
}

func TestAnswerEscCancels(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m = typeString(t, m, "half typed")
	m, cmd := update(t, m, key("esc"))
	wantQuit(t, m, cmd, ErrCancelled)
}

func TestAnswerViewShowsTheReadErrorAndStillTakesInput(t *testing.T) {
	m, _ := answerModelFor(t, newFakeHerdr(t), "webshop")
	m, _ = update(t, m, dialogMsg{round: 2, err: errors.New("herdr: read timed out")})
	if !contains(m.View(), "could not read dialog: herdr: read timed out") {
		t.Fatalf("view lacks the read error:\n%s", m.View())
	}
	m = typeString(t, m, "enter")
	if m.answer.input.Value() != "enter" {
		t.Fatalf("input = %q", m.answer.input.Value())
	}
}
