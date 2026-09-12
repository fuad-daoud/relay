package pick

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

// dialogMsg is the builder's dialog text, or why it could not be read. text
// may be set alongside err: the live read failed and the round's question
// file stood in, which the human should know is possibly stale.
type dialogMsg struct {
	round int
	text  string
	err   error
}

// answerModel is the answer screen (spec §6): the dialog above, one input
// line below. It parses nothing from the dialog.
type answerModel struct {
	name   string
	round  int
	loaded bool
	err    error  // read failure, shown above the body
	refuse string // inline refusal after an empty submit; cleared on typing
	vp     viewport.Model
	input  textinput.Model
}

// answerChrome is what the answer screen spends outside the body: header,
// two rules, the input line, and the refusal/blank line.
const answerChrome = 5

func bodyHeight(h int) int {
	n := h - answerChrome
	if n < 1 {
		n = 1
	}
	return n
}

func newAnswerModel(name string, width, height int) answerModel {
	ti := textinput.New()
	ti.Prompt = "answer> "
	ti.CharLimit = 200
	ti.Focus()
	return answerModel{name: name, vp: viewport.New(width, bodyHeight(height)), input: ti}
}

// fetchDialog reads the builder's dialog the way the daemon does, from the
// detection source, and falls back to the question file the daemon wrote
// for this round. Both failing is reported with no text; the human can still
// see the builder pane and type.
func fetchDialog(ctx context.Context, rt relay.Runtime, name string) tea.Cmd {
	return func() tea.Msg {
		b, err := rt.Store.Load(name)
		if err != nil {
			return dialogMsg{err: err}
		}
		text, err := rt.Herdr.ReadAgentSource(ctx, relay.Target(b.Builder), relay.DialogSource, relay.DialogLines)
		if err == nil {
			return dialogMsg{round: b.Round, text: text}
		}
		stale, ferr := os.ReadFile(rt.Store.QuestionPath(name, b.Round))
		if ferr != nil {
			return dialogMsg{round: b.Round, err: err}
		}
		return dialogMsg{round: b.Round, text: string(stale), err: err}
	}
}

func (m Model) enterAnswer(name string) (tea.Model, tea.Cmd) {
	m.screen = screenAnswer
	m.answer = newAnswerModel(name, m.width, m.height)
	return m, tea.Batch(fetchDialog(m.ctx, m.rt, name), textinput.Blink)
}

func (m Model) onDialog(msg dialogMsg) (tea.Model, tea.Cmd) {
	m.answer.round = msg.round
	m.answer.err = msg.err
	m.answer.loaded = true
	m.answer.vp.SetContent(msg.text)
	return m, nil
}

// answerKeys: arrows and page keys scroll the dialog; Enter submits; Esc
// cancels; everything else is typed. j/k are not scroll keys here because
// they are letters the human may need to type.
func (m Model) answerKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.quit(ErrCancelled)
	case "up":
		m.answer.vp.LineUp(1)
		return m, nil
	case "down":
		m.answer.vp.LineDown(1)
		return m, nil
	case "pgup":
		m.answer.vp.ViewUp()
		return m, nil
	case "pgdown":
		m.answer.vp.ViewDown()
		return m, nil
	case "enter":
		s := strings.TrimSpace(m.answer.input.Value())
		if s == "" {
			m.answer.refuse = "type a key name, a number or text"
			return m, nil
		}
		m.screen = screenResult
		m.result = resultModel{pending: true}
		return m, runVerb(m.ctx, m.rt, m.opts, m.answer.name, relay.ParseAnswer(s))
	}
	m.answer.refuse = ""
	var cmd tea.Cmd
	m.answer.input, cmd = m.answer.input.Update(msg)
	return m, cmd
}

func (m Model) answerView() string {
	a := m.answer
	width := m.width
	if width <= 0 {
		width = 80
	}
	rule := hintStyle.Render(strings.Repeat("─", width))
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("answer %s (round %d)", a.name, a.round)))
	sb.WriteString("  ")
	sb.WriteString(hintStyle.Render("↑/↓ scroll  Enter send  Esc cancel"))
	sb.WriteString("\n" + rule + "\n")
	if a.err != nil {
		sb.WriteString(errorStyle.Render("could not read dialog: ") + a.err.Error() + "\n")
	}
	if !a.loaded {
		sb.WriteString(hintStyle.Render("reading the dialog…"))
	} else {
		sb.WriteString(a.vp.View())
	}
	sb.WriteString("\n" + rule + "\n")
	sb.WriteString(a.input.View())
	if a.refuse != "" {
		sb.WriteString("\n" + errorStyle.Render(a.refuse))
	}
	return sb.String()
}
