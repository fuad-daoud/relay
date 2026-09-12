package pick

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relay/internal/relay"
)

type screen int

const (
	screenList screen = iota
	screenAnswer
	screenResult
	screenConfirm
)

// statusMsg is the list's rows, or why there are none.
type statusMsg struct {
	report relay.Report
	err    error
}

// verbDoneMsg is what the verb said, or why it failed.
type verbDoneMsg struct {
	text string
	err  error
}

// Model is the whole picker: one of four screens at a time. Every herdr
// and store call runs in a tea.Cmd and comes back as a message, so Update
// is pure and tests drive it with messages.
type Model struct {
	ctx  context.Context
	rt   relay.Runtime
	opts Options

	screen        screen
	width, height int

	// list screen
	rows   []relay.BindingStatus
	loaded bool // false until the first statusMsg
	cursor int
	top    int

	result resultModel
	answer answerModel
	// confirm is the row a done/unbind is waiting on a `y` for (#103).
	// Meaningful only while screen == screenConfirm.
	confirm relay.BindingStatus

	// outcome is what Run returns: nil after a verb succeeded, else one of
	// the sentinels in verb.go. Set exactly once, by quit.
	outcome error
}

func newModel(ctx context.Context, rt relay.Runtime, opts Options) Model {
	return Model{ctx: ctx, rt: rt, opts: opts, screen: screenList}
}

func (m Model) Init() tea.Cmd {
	return fetchStatus(m.ctx, m.rt)
}

func fetchStatus(ctx context.Context, rt relay.Runtime) tea.Cmd {
	return func() tea.Msg {
		rep, err := relay.Status(ctx, rt)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// runVerb runs the chosen verb and reports its text, which is the same text
// the CLI prints (spec §5). in is only read by answer.
func runVerb(ctx context.Context, rt relay.Runtime, opts Options, name string, in relay.AnswerInput) tea.Cmd {
	return func() tea.Msg {
		switch opts.Verb {
		case VerbDone:
			if err := relay.Done(ctx, rt, name); err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.DoneText(name)}
		case VerbUnbind:
			res, err := relay.Unbind(ctx, rt, name, opts.Archive)
			if err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.UnbindText(name, res)}
		case VerbAnswer:
			if err := relay.Answer(ctx, rt, name, in); err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relay.AnswerText(name)}
		}
		return verbDoneMsg{err: fmt.Errorf("unknown verb %q", opts.Verb)}
	}
}

// quit records the outcome and ends the program. It is the only place
// outcome is written.
func (m Model) quit(outcome error) (tea.Model, tea.Cmd) {
	m.outcome = outcome
	return m, tea.Quit
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
		m.answer.vp.Width = msg.Width
		m.answer.vp.Height = bodyHeight(msg.Height)
		return m, nil

	case statusMsg:
		return m.onStatus(msg)

	case verbDoneMsg:
		m.result = resultModel{text: msg.text, err: msg.err}
		if msg.err != nil {
			m.result.outcome = ErrVerbFailed
		}
		return m, nil

	case dialogMsg:
		return m.onDialog(msg)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.quit(ErrCancelled)
		}
		switch m.screen {
		case screenList:
			return m.listKeys(msg)
		case screenAnswer:
			return m.answerKeys(msg)
		case screenResult:
			return m.resultKeys(msg)
		case screenConfirm:
			return m.confirmKeys(msg)
		}
	default:
		if m.screen == screenAnswer {
			var cmd tea.Cmd
			m.answer.input, cmd = m.answer.input.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) onStatus(msg statusMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenResult
		m.result = resultModel{err: msg.err, outcome: ErrVerbFailed}
		return m, nil
	}
	m.rows = rowsFor(m.opts.Verb, msg.report)
	m.loaded = true
	if len(m.rows) == 0 {
		m.screen = screenResult
		m.result = resultModel{text: emptyText(m.opts.Verb), outcome: ErrNothingToPick}
		return m, nil
	}
	if m.opts.Verb == VerbAnswer && len(m.rows) == 1 {
		return m.enterAnswer(m.rows[0].Name)
	}
	return m, nil
}

func (m Model) listKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m.quit(ErrCancelled)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "enter":
		if !m.loaded || len(m.rows) == 0 {
			return m, nil
		}
		return m.pick(m.rows[m.cursor])
	}
	m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
	return m, nil
}

// pick acts on the chosen row. done and unbind run at once on a DONE row and
// stop for a `y` on any other (#103, needsConfirm); answer needs its own
// screen first (spec §6).
func (m Model) pick(r relay.BindingStatus) (tea.Model, tea.Cmd) {
	switch m.opts.Verb {
	case VerbDone, VerbUnbind:
		if needsConfirm(m.opts.Verb, r) {
			m.screen = screenConfirm
			m.confirm = r
			return m, nil
		}
		return m.run(r.Name)
	case VerbAnswer:
		return m.enterAnswer(r.Name)
	}
	return m, nil
}

// run moves to a pending result screen and starts the verb on name. It is
// what Enter did before #103; the confirm screen's `y` reaches it now.
func (m Model) run(name string) (tea.Model, tea.Cmd) {
	m.screen = screenResult
	m.result = resultModel{pending: true}
	return m, runVerb(m.ctx, m.rt, m.opts, name, relay.AnswerInput{})
}

// confirmKeys: only a lowercase y proceeds. Every other key -- Enter
// included, since a stray Enter is the whole reason this screen exists --
// returns to the list with the cursor where it was. ctrl+c is handled
// before this in Update and still cancels the picker.
func (m Model) confirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "y" {
		return m.run(m.confirm.Name)
	}
	m.screen = screenList
	return m, nil
}

func (m Model) resultKeys(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.result.pending {
		return m, nil
	}
	return m.quit(m.result.outcome)
}

func (m Model) View() string {
	switch m.screen {
	case screenAnswer:
		return m.answerView()
	case screenResult:
		return m.resultView()
	case screenConfirm:
		return m.confirmView()
	default:
		return m.listView()
	}
}
