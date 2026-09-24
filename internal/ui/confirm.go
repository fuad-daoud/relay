package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// getwd is os.Getwd, to the repo path the bind confirm names, "" when the
// working directory cannot be read. It is replaceable so a test can pin the
// repo without changing the process's directory.
var getwd = func() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// overlay is a modal box the shell draws over the body's last rows (§3). It
// owns every key while it is set (rule 2.5, §5.2); update returns the next
// overlay, the command to run, and whether the overlay closed.
type overlay interface {
	update(tea.KeyMsg) (overlay, tea.Cmd, bool)
	view(width int) []string
}

// confirmBox is a one-line yes/no confirm: y runs onYes, n and esc cancel
// (§3, §4.3).
type confirmBox struct {
	title string
	lines []string
	onYes tea.Cmd
}

func (c confirmBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "y", "Y":
		return c, c.onYes, true
	case "n", "N", "esc":
		return c, nil, true
	}
	return c, nil, false
}

func (c confirmBox) view(width int) []string {
	out := []string{fit(accentStyle.Render(c.title), width)}
	for _, l := range c.lines {
		out = append(out, fit(dimStyle.Render(l), width))
	}
	return out
}

// promptBox is a single-line input, optionally with a choice list tab cycles
// through (§3). validate, when set, runs on enter: a non-nil error keeps the
// prompt open with the message under the input (§4.3). esc cancels.
//
// complete, when set, is the tab key's own source of choices: it is called
// with the value the first tab started from and returns what that value can
// become, so a path prompt lists and cycles the directory's entries (§4.5).
type promptBox struct {
	title   string
	input   textinput.Model
	choices []string
	onEnter func(value string) tea.Cmd

	complete func(value string) []string
	base     string // the value the first tab completed from

	validate func(value string) error
	err      string
	sel      int
	tabbed   bool
}

// newPromptInput builds the prompt's input, focused, with no prompt string of
// its own: the box's title is the question.
func newPromptInput() textinput.Model {
	in := textinput.New()
	in.Focus()
	return in
}

func (p promptBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return p, nil, true
	case "tab", "shift+tab", "back_tab":
		return p.completeTab(), nil, false
	case "enter":
		value := p.input.Value()
		if p.validate != nil {
			if err := p.validate(value); err != nil {
				p.err = err.Error()
				return p, nil, false
			}
		}
		p.err = ""
		if p.onEnter == nil {
			return p, nil, true
		}
		return p, p.onEnter(value), true
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	// A new value restarts the completion: the next tab lists against what
	// the human typed, not what a previous tab inserted.
	p.base, p.tabbed = "", false
	return p, cmd, false
}

// completeTab is tab inside a prompt: a completion function lists what the
// value the first tab started from can become and cycles the matches; a fixed
// choice list cycles as it always has (§4.5).
func (p promptBox) completeTab() promptBox {
	if p.complete != nil {
		if !p.tabbed {
			p.base = p.input.Value()
			p.sel = 0
		}
		matches := p.complete(p.base)
		if len(matches) == 0 {
			return p
		}
		p.tabbed = true
		p.sel %= len(matches)
		p.input.SetValue(matches[p.sel])
		p.input.CursorEnd()
		p.sel = (p.sel + 1) % len(matches)
		return p
	}
	if len(p.choices) > 0 {
		p.sel = (p.sel + 1) % len(p.choices)
		p.input.SetValue(p.choices[p.sel])
		p.input.CursorEnd()
	}
	return p
}

func (p promptBox) view(width int) []string {
	out := []string{fit(accentStyle.Render(p.title), width), fit(p.input.View(), width)}
	if p.err != "" {
		out = append(out, fit(errorStyle.Render(p.err), width))
	}
	return out
}

// plannerConfirmLine names the planner a confirm affects, when that planner
// is neither the human at the cockpit nor empty (§4.3). wait is the phrase
// that follows the name.
func plannerConfirmLine(b relevo.BindingStatus, wait string) string {
	if b.PlannerName == "" || b.PlannerName == "you" {
		return ""
	}
	return "planner " + b.PlannerName + " " + wait
}

// stopConfirmLines is x's confirm body: who else is affected, what is being
// stopped, and the keys. Pure, so it is tested directly (§4.3, §5).
func stopConfirmLines(b relevo.BindingStatus, now time.Time) []string {
	var lines []string
	if l := plannerConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, joinFacts(actorCell(b)+" on "+candidateText(b), nowCell(b, now), spendCell(b)))
	lines = append(lines, "y stop · n cancel")
	return lines
}

// doneConfirmLines is D's confirm body (§4.3).
func doneConfirmLines(b relevo.BindingStatus) []string {
	var lines []string
	if l := plannerConfirmLine(b, "owns this binding"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "y done · n cancel")
	return lines
}

// unbindConfirmLines is u's confirm body (§4.3).
func unbindConfirmLines(b relevo.BindingStatus) []string {
	var lines []string
	if l := plannerConfirmLine(b, "owns this binding"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "y unbind · n cancel")
	return lines
}

// joinFacts joins the non-empty facts with the confirm's middot.
func joinFacts(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// stopCmd is the x key's confirm on b (§4.3, §5).
func stopCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		title: fmt.Sprintf("Stop %s round %d?", key, b.Round),
		lines: stopConfirmLines(b, env.Now),
		onYes: runAction(env.Ctx, "stop", key, func(ctx context.Context) Result {
			return env.Actions.Stop(ctx, key)
		}),
	})
}

// doneCmd is the D key's confirm on b (§4.3).
func doneCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		title: fmt.Sprintf("Mark %s done?", key),
		lines: doneConfirmLines(b),
		onYes: runAction(env.Ctx, "done", key, func(ctx context.Context) Result {
			return env.Actions.Done(ctx, key)
		}),
	})
}

// unbindCmd is the u key's confirm on b (§4.3).
func unbindCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		title: fmt.Sprintf("Unbind %s? The binding is archived; its branch %s is kept.", key, b.Branch),
		lines: unbindConfirmLines(b),
		onYes: runAction(env.Ctx, "unbind", key, func(ctx context.Context) Result {
			return env.Actions.Unbind(ctx, key)
		}),
	})
}

// gateCmd is the g key's two prompts on b: the duration, then the reason. The
// duration must parse and be positive, or the prompt stays open (§4.3).
func gateCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	subject := b.BuilderCandidate

	dur := promptBox{
		title: fmt.Sprintf("gate %s for (e.g. 2h; empty = until cleared):", subject),
		input: newPromptInput(),
	}
	dur.validate = func(value string) error {
		s := strings.TrimSpace(value)
		if s == "" {
			return nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		if d <= 0 {
			return errors.New("duration must be positive")
		}
		return nil
	}
	dur.onEnter = func(value string) tea.Cmd {
		forDur, _ := time.ParseDuration(strings.TrimSpace(value))
		reason := promptBox{title: "reason (optional):", input: newPromptInput()}
		reason.onEnter = func(r string) tea.Cmd {
			return runAction(env.Ctx, "gate", key, func(ctx context.Context) Result {
				return env.Actions.Gate(ctx, subject, forDur, r)
			})
		}
		return openOverlay(reason)
	}
	return openOverlay(dur)
}

// sendCmd is the s key: the plan file prompt, then the send confirm (§4.5).
// The prompt is pre-filled with the binding tree's docs/plans/ when that
// directory exists, tab lists and cycles the directory's entries, and enter
// checks the file is really there before anything is sent.
func sendCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	p := promptBox{title: "plan file:", input: newPromptInput(), complete: pathCompletion}
	if d := plansDir(b); d != "" {
		p.input.SetValue(d)
		p.input.CursorEnd()
	}
	p.validate = func(value string) error {
		file := strings.TrimSpace(value)
		if file == "" {
			return errors.New("plan file is required")
		}
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", file)
		}
		return nil
	}
	p.onEnter = func(value string) tea.Cmd {
		file := strings.TrimSpace(value)
		return openOverlay(confirmBox{
			title: sendConfirmTitle(b, file),
			lines: sendConfirmLines(b),
			onYes: runAction(env.Ctx, "send", key, func(ctx context.Context) Result {
				return env.Actions.Send(ctx, key, file)
			}),
		})
	}
	return openOverlay(p)
}

// plansDir is <binding tree>/docs/plans/ when that directory exists, "" when
// it does not. It is the send prompt's pre-fill (§4.5).
func plansDir(b relevo.BindingStatus) string {
	if b.CWD == "" {
		return ""
	}
	dir := filepath.Join(b.CWD, "docs", "plans")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir + string(filepath.Separator)
}

// pathCompletion is the plan prompt's tab: the entries of the directory the
// value names, filtered by its last segment, so tab lists a directory and
// cycles what is in it (§4.5). A value ending in the separator lists that
// directory's entries whole.
func pathCompletion(value string) []string {
	if value == "" {
		return nil
	}
	dir, prefix := value, ""
	if !strings.HasSuffix(value, string(filepath.Separator)) {
		dir, prefix = filepath.Dir(value), filepath.Base(value)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || (prefix != "" && !strings.HasPrefix(name, prefix)) {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			full += string(filepath.Separator)
		}
		out = append(out, full)
	}
	sort.Strings(out)
	return out
}

// sendConfirmTitle is the send confirm's question: the file, the binding, the
// round the send opens, and nothing else (§4.5). The round is
// BindingStatus.Round: the daemon advances b.Round when a round closes
// (internal/relevo/reconcile.go), so relevo.Send opens round b.Round, not
// b.Round+1 (W3). A binding with a round open is refused by Send itself, so
// the label stays and the refusal comes back as the action's error.
func sendConfirmTitle(b relevo.BindingStatus, file string) string {
	return fmt.Sprintf("Send %s to %s as round %d?", file, b.Key(), b.Round)
}

// sendConfirmLines names who else is affected, then the keys (§4.3, §4.5).
func sendConfirmLines(b relevo.BindingStatus) []string {
	var lines []string
	if l := plannerConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "y send · n cancel")
	return lines
}

// editorCmd is the E key: draft a plan in the state directory's tui-plans/,
// open $EDITOR on it with the terminal released, and, when the human left
// something new and non-empty in it, ask the same send confirm s asks (§4.5).
func editorCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	dir, ok := tuiPlansDir(env)
	if !ok {
		return notice("no state directory to draft a plan in; write one and use s")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return notice(err.Error())
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.md", key, env.Now.Unix()))
	header := []byte(editorPlanHeader(key))
	if err := os.WriteFile(path, header, 0o644); err != nil {
		return notice(err.Error())
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		after, rerr := os.ReadFile(path)
		if rerr != nil {
			return noticeMsg{text: rerr.Error()}
		}
		if !planSent(header, after) {
			return noticeMsg{text: "nothing sent"}
		}
		return openOverlayMsg{ov: confirmBox{
			title: sendConfirmTitle(b, path),
			lines: sendConfirmLines(b),
			onYes: runAction(env.Ctx, "send", key, func(ctx context.Context) Result {
				return env.Actions.Send(ctx, key, path)
			}),
		}}
	})
}

// tuiPlansDir is <state dir>/tui-plans, where the E key drafts a plan. The
// state dir is the store's root: the database sits inside it.
func tuiPlansDir(env Env) (string, bool) {
	st := env.Src.Base().Store
	if st == nil {
		return "", false
	}
	return filepath.Join(filepath.Dir(st.DBPath()), "tui-plans"), true
}

// editorPlanHeader is the one-line comment the draft file starts with, so a
// human sees what the file is for before writing in it (§4.5).
func editorPlanHeader(name string) string {
	return fmt.Sprintf("<!-- relevo: plan for %s -->\n", name)
}

// planSent reports whether an edited draft is worth sending: something
// non-empty was written and the file is no longer the header alone (§4.5).
func planSent(before, after []byte) bool {
	return len(bytes.TrimSpace(after)) > 0 && !bytes.Equal(before, after)
}

// bindCmd is the b key: name, candidate and feature, in that order, then the
// bind confirm (§4.5). It takes no binding: the key creates one.
func bindCmd(env Env) tea.Cmd {
	name := promptBox{title: "name:", input: newPromptInput()}
	name.validate = func(value string) error {
		v := strings.TrimSpace(value)
		if v == "" {
			return errors.New("name is required")
		}
		return store.ValidName(v)
	}
	name.onEnter = func(value string) tea.Cmd {
		binding := strings.TrimSpace(value)
		cand := promptBox{
			title:   "candidate (tab: …; empty = policy pick):",
			input:   newPromptInput(),
			choices: env.Actions.Candidates("builder"),
		}
		cand.onEnter = func(c string) tea.Cmd {
			feature := promptBox{title: "feature (optional):", input: newPromptInput()}
			feature.onEnter = func(f string) tea.Cmd {
				in := BindInput{Name: binding, Candidate: strings.TrimSpace(c), Feature: strings.TrimSpace(f)}
				return openOverlay(confirmBox{
					title: bindConfirmTitle(in),
					lines: []string{"y bind · n cancel"},
					onYes: runAction(env.Ctx, "bind", binding, func(ctx context.Context) Result {
						return env.Actions.Bind(ctx, in)
					}),
				})
			}
			return openOverlay(feature)
		}
		return openOverlay(cand)
	}
	return openOverlay(name)
}

// bindConfirmTitle is the bind confirm's question: the name, the repo the
// worktree is cut from, and the candidate or the policy pick (§4.5).
func bindConfirmTitle(in BindInput) string {
	on := in.Candidate
	if on == "" {
		on = "the policy pick"
	}
	return fmt.Sprintf("Bind %s on a new worktree of %s as builder on %s?", in.Name, getwd(), on)
}

// retryCmd is the r key: which candidate to retry on, then the confirm (§4.5).
// The choice list is the role's candidates in their order, without the one the
// binding runs now.
func retryCmd(env Env, b relevo.BindingStatus) tea.Cmd {
	key := b.Key()
	choices := excludeCandidate(env.Actions.Candidates(actorCell(b)), candidateText(b))
	p := promptBox{title: "retry on (tab cycles):", input: newPromptInput(), choices: choices}
	p.validate = func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("a candidate is required")
		}
		return nil
	}
	p.onEnter = func(value string) tea.Cmd {
		candidate := strings.TrimSpace(value)
		return openOverlay(confirmBox{
			title: retryConfirmTitle(b, candidate),
			lines: retryConfirmLines(b, candidate),
			onYes: runAction(env.Ctx, "retry", key, func(ctx context.Context) Result {
				return env.Actions.Retry(ctx, key, candidate)
			}),
		})
	}
	return openOverlay(p)
}

// retryConfirmTitle is the retry confirm's question (§4.5): an open round is
// stopped first and the question names it; with nothing open the last plan is
// simply resent as a new round.
func retryConfirmTitle(b relevo.BindingStatus, candidate string) string {
	if roundOpen(b) {
		return fmt.Sprintf("Stop %s round %d and resend its plan on %s?", b.Key(), b.Round, candidate)
	}
	return fmt.Sprintf("Resend %s's last plan as a new round on %s?", b.Key(), candidate)
}

// retryConfirmLines names who else is affected, that the candidate persists as
// the binding's builder, then the keys (§4.5).
func retryConfirmLines(b relevo.BindingStatus, candidate string) []string {
	var lines []string
	if l := plannerConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "the binding keeps "+candidate+" for later rounds")
	lines = append(lines, "y retry · n cancel")
	return lines
}

// roundOpen reports whether the row has a round in flight, the fact that
// decides which retry confirm is shown (§4.5). ACTIVE is the one display word
// that means a round is open.
func roundOpen(b relevo.BindingStatus) bool { return b.Display == "ACTIVE" }

// excludeCandidate drops current from names, so a retry prompt never offers
// the candidate the binding already runs (§4.5).
func excludeCandidate(names []string, current string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" && n != current {
			out = append(out, n)
		}
	}
	return out
}

// shellCmd is the o key: a shell in the binding's tree, with the terminal
// released and restored. A failure to start one is a notice (§4.3, §6).
func shellCmd(env Env, key string) tea.Cmd {
	cmd, err := env.Actions.Shell(key)
	if err != nil {
		return notice(err.Error())
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		return nil
	})
}

// actionKey is the action keys' one dispatch, shared by the fleet and the
// round view (§4.3, §4.5): the command the key returns, and whether the key
// was an action key at all. Without Actions every action key does nothing; a
// second action on a binding that already carries one is refused with a
// notice. 'b' is deliberately not here: only the fleet binds (fleetActionKey).
func actionKey(env Env, b relevo.BindingStatus, k string) (tea.Cmd, bool) {
	if env.Actions == nil {
		return nil, false
	}
	switch k {
	case "x", "D", "u", "g", "o", "s", "E", "r":
	default:
		return nil, false
	}
	if verb, ok := env.Running[b.Key()]; ok {
		return notice(fmt.Sprintf("%s: %s still running", b.Key(), verb)), true
	}
	switch k {
	case "x":
		return stopCmd(env, b), true
	case "D":
		return doneCmd(env, b), true
	case "u":
		return unbindCmd(env, b), true
	case "g":
		return gateCmd(env, b), true
	case "s":
		return sendCmd(env, b), true
	case "E":
		return editorCmd(env, b), true
	case "r":
		return retryCmd(env, b), true
	}
	return shellCmd(env, b.Key()), true
}

// fleetActionKey is actionKey plus the fleet's own 'b': a bind names no
// binding yet, so it has no row to act on and no in-flight action to collide
// with (§4.5).
func fleetActionKey(env Env, b relevo.BindingStatus, k string) (tea.Cmd, bool) {
	if k == "b" {
		if env.Actions == nil {
			return nil, false
		}
		return bindCmd(env), true
	}
	return actionKey(env, b, k)
}
