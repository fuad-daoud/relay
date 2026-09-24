package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// command is one entry of the ':' command table (§4.7).
type command struct{ name, args, help string }

// commands is the table §4.7 fixes, in display order.
var commands = []command{
	{"fleet", "", "bindings on this machine"},
	{"rounds", "[query…]", "every round, filtered"},
	{"round", "<binding> [N]", "open one binding's round"},
	{"help", "", "keys"},
	{"quit", "", "leave"},
}

// cmdLine is the ':' command line: an open flag, its text input and the
// index of the selected completion (§4.7).
type cmdLine struct {
	open  bool
	input textinput.Model
	sel   int
}

// newCmdLine builds the command line with its ':' prompt.
func newCmdLine() cmdLine {
	in := textinput.New()
	in.Prompt = ":"
	return cmdLine{input: in}
}

// opened shows the command line, empty, with the cursor in the input.
func (c cmdLine) opened() cmdLine {
	c.open = true
	c.sel = 0
	c.input.SetValue("")
	c.input.Focus()
	c.input.CursorEnd()
	return c
}

// typed is the command line's current text.
func (c cmdLine) typed() string { return strings.TrimSpace(c.input.Value()) }

// matches is every completion candidate whose name has the typed text as a
// case-insensitive subsequence, ranked by prefix, then length, then
// alphabetical, capped at 8. Pure: the tests drive it directly.
func (c cmdLine) matches(env Env) []command {
	typed := strings.ToLower(c.typed())
	cands := make([]command, 0, len(commands)+len(env.Report.Bindings))
	cands = append(cands, commands...)
	for i := range env.Report.Bindings {
		key := env.Report.Bindings[i].Key()
		cands = append(cands, command{name: "round " + key, help: "open " + key})
	}
	var out []command
	for _, cand := range cands {
		if subsequence(typed, strings.ToLower(cand.name)) {
			out = append(out, cand)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi := strings.HasPrefix(strings.ToLower(out[i].name), typed)
		pj := strings.HasPrefix(strings.ToLower(out[j].name), typed)
		if pi != pj {
			return pi
		}
		if len(out[i].name) != len(out[j].name) {
			return len(out[i].name) < len(out[j].name)
		}
		return out[i].name < out[j].name
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// subsequence reports whether sub's runes appear, in order, in s.
func subsequence(sub, s string) bool {
	if sub == "" {
		return true
	}
	rs := []rune(sub)
	i := 0
	for _, r := range s {
		if r == rs[i] {
			i++
			if i == len(rs) {
				return true
			}
		}
	}
	return false
}

// update handles one key while the command line is open: up/down move the
// selection, tab completes, enter executes, esc closes, everything else goes
// to the input (§4.7, §6).
func (c cmdLine) update(k tea.KeyMsg, env Env, p prefs) (cmdLine, tea.Cmd) {
	ms := c.matches(env)
	switch k.String() {
	case "up", "ctrl+p":
		if len(ms) > 0 {
			c.sel = (c.sel - 1 + len(ms)) % len(ms)
		}
		return c, nil
	case "down", "ctrl+n":
		if len(ms) > 0 {
			c.sel = (c.sel + 1) % len(ms)
		}
		return c, nil
	case "tab":
		if len(ms) > 0 {
			if c.sel >= len(ms) {
				c.sel = 0
			}
			c.input.SetValue(ms[c.sel].name + " ")
			c.input.CursorEnd()
		}
		return c, nil
	case "enter":
		return c.execute(env, p)
	case "esc":
		c.open = false
		c.input.Blur()
		return c, nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	if len(ms) > 0 && c.sel >= len(ms) {
		c.sel = 0
	}
	return c, cmd
}

// execute runs the typed command: an exact name wins, otherwise the
// selected completion (§6).
func (c cmdLine) execute(env Env, p prefs) (cmdLine, tea.Cmd) {
	value := c.input.Value()
	c.open = false
	c.input.Blur()
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return c, nil
	}
	for _, cmd := range commands {
		if cmd.name == fields[0] {
			return c, execLine(value, env, p)
		}
	}
	ms := c.matches(env)
	if len(ms) == 0 {
		return c, notice(fmt.Sprintf("unknown command %q (try :help)", ":"+fields[0]))
	}
	sel := c.sel
	if sel >= len(ms) {
		sel = 0
	}
	return c, execLine(ms[sel].name, env, p)
}
