package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// This file holds the round pane's head -- the tokens line and the tab bar --
// and the reader round's card, which re-spreads the tokens line inside its
// border and adds the artifact facts and the reader's keys (round 5b).

// tokensLine renders the tokens and facts row at the top of the body.
func (p roundPane) tokensLine(b *view.BindingStatus) string {
	left, right := p.tokensParts(b)
	return spread(left, right, p.width)
}

// tokensParts is the tokens line's two halves, the left indented three
// spaces as the body's first line is. The reader round's card re-spreads them
// inside its border.
func (p roundPane) tokensParts(b *view.BindingStatus) (string, string) {
	if b == nil || !p.detail.live {
		left := "   " + faintStyle.Render("no live facts for a released binding")
		return left, ""
	}

	var rawParts []string
	if b.LiveUsage != nil {
		rawParts = usage.LiveParts(*b.LiveUsage)
	} else if b.LastUsage != nil {
		rawParts = usage.Parts(*b.LastUsage)
	}

	body := "tokens "
	if len(rawParts) > 0 {
		var filtered []string
		for i, part := range rawParts {
			isLast := i == len(rawParts)-1
			if isLast && strings.HasPrefix(part, "unknown") {
				part = "no price"
			}
			if strings.HasPrefix(part, "in ") ||
				strings.HasPrefix(part, "cache ") ||
				strings.HasPrefix(part, "out ") ||
				(strings.HasPrefix(part, "write ") && part != "write 0") ||
				isLast {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) > 0 {
			body += strings.Join(filtered, " · ")
		} else {
			body += "no usage yet"
		}
	} else {
		body += "no usage yet"
	}

	if b.LastClose != nil && b.LastClose.Commits > 0 {
		unit := "commits"
		if b.LastClose.Commits == 1 {
			unit = "commit"
		}
		body += fmt.Sprintf(" · +%d %s", b.LastClose.Commits, unit)
	}
	if s := spendCell(*b); s != "" {
		body += " · spend " + s
	}

	left := "   " + faintStyle.Render(body)

	var right string
	if b.Headless != nil && b.Headless.PID != 0 {
		right = faintStyle.Render(fmt.Sprintf("pid %d since %s", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))) + "  "
	}

	return left, right
}

// tabsRow renders the pill tabs and the round stepper on the right. A
// reader round draws its own tabs: plan, artifacts N, log and transcript.
func (p roundPane) tabsRow() string {
	tabs := p.tabs()
	words := make([]string, len(tabs))
	for i, t := range tabs {
		if t == p.detail.active {
			words[i] = chip(chipAccentStyle, p.tabLabel(t))
		} else {
			words[i] = mutedStyle.Render(chip(normalStyle, p.tabLabel(t)))
		}
	}
	left := "  " + strings.Join(words, "   ")

	rightText := fmt.Sprintf("  r%d of %d  ", p.detail.round, p.detail.rounds)
	right := faintStyle.Render("round  ") + chip(kbdStyle, "[") + textStyle.Bold(true).Render(rightText) + chip(kbdStyle, "]") + "  "

	return spread(left, right, p.width)
}

// readerCard is the reader round's card (round 5b), the board's box: the
// state chip and time, the artifacts count and total size, the tokens line and
// the reader's own keys. A writer round draws no card.
func (p roundPane) readerCard(b *view.BindingStatus) []string {
	title := accentStyle.Bold(true).Render(p.detail.name) + "  " +
		faintStyle.Render(fmt.Sprintf("round %d", p.detail.round))
	inner := p.width - 4
	if inner < 20 {
		inner = 20
	}
	left, tokensRight := p.tokensParts(b)
	left = "  " + strings.TrimPrefix(left, "   ")
	return renderCard(p.width, title, faintStyle.Render(p.cardWord(b)), []string{
		p.stateLine(b),
		spread(left, tokensRight, inner),
		"",
		cardKeysRow(p.readerKeys()),
	})
}

// readerKeys is the reader card's keys: no g gate; send next and retry on as
// for a writer. Without Actions a reader has none.
func (p roundPane) readerKeys() []KeyHelp {
	if !p.actions {
		return nil
	}
	return []KeyHelp{
		{"s", "send next"},
		{"r", "retry on…"},
	}
}

// cardWord is the card's top-right word: "live" while the round on screen is
// the binding's open round, "sealed" once it has closed.
func (p roundPane) cardWord(b *view.BindingStatus) string {
	if b == nil || !p.detail.live {
		return "sealed"
	}
	if p.detail.round == b.Round && b.RoundEnd.IsZero() {
		return "live"
	}
	return "sealed"
}

// stateLine is the card's first row: the round's state chip and age as the
// context row shows them, then the artifacts count and total size, and
// "repository unchanged" once the round has closed.
func (p roundPane) stateLine(b *view.BindingStatus) string {
	if b == nil || !p.detail.live {
		return "  " + faintStyle.Render("no live facts for a released binding")
	}
	g := groupOf(*b)
	var pStyle lipgloss.Style
	var pWord string
	switch g {
	case groupNeedsYou:
		pStyle, pWord = chipWarnStyle.Bold(true), "needs you"
	case groupWorking:
		pStyle, pWord = chipGreenStyle.Bold(true), "working"
	case groupIdle:
		pStyle, pWord = kbdStyle, "idle"
	case groupHeld:
		pStyle, pWord = kbdStyle, "on hold"
	case groupDone:
		pStyle, pWord = kbdStyle, "done"
	default:
		pStyle, pWord = kbdStyle, strings.ToLower(b.Display)
	}

	var age string
	if g == groupWorking {
		age = ago(b.RoundStart, p.now())
	} else {
		age = strings.TrimPrefix(rowNow(*b, p.now()), fmt.Sprintf("r%d · ", b.Round))
	}

	s := "  " + chip(pStyle, pWord)
	if age != "" {
		s += "   " + textStyle.Render(age)
	}
	if c := p.detail.cache[tabArtifacts]; artifactCount(c) > 0 {
		s += faintStyle.Render("  ·  ") + mutedStyle.Render(artifactsWord(artifactCount(c)))
		s += faintStyle.Render("  ·  ") + mutedStyle.Render(relevo.ArtifactSizeText(artifactTotalSize(c)))
	}
	if p.cardWord(b) != "live" {
		s += faintStyle.Render("  ·  ") + mutedStyle.Render("repository unchanged")
	}
	return s
}

// moveArtifact moves the artifacts cursor by delta and refetches the newly
// selected file. At either edge it is a no-op with no notice.
func (p roundPane) moveArtifact(delta int) (roundPane, tea.Cmd) {
	next := p.artifactSel + delta
	if next < 0 || next >= artifactCount(p.detail.cache[tabArtifacts]) {
		return p, nil
	}
	p.artifactSel = next
	if p.tabInFlight {
		return p, nil
	}
	p.tabInFlight = true
	return p, fetchFor(p.ctx, p.src, tabArtifacts, p.detail.name, p.detail.round, 1, p.artifactSel, p.detail.live)
}

// openCmd is enter on an artifacts row: the selected file in the pager, the
// browser or the editor, through the Actions seam, so no test ever runs one.
// A file still on disk opens where it is; a sealed file -- one the seal pass
// moved into round_file -- is first written to a temp file under os.TempDir()
// whose name starts with its own, and that is what opens.
func (p roundPane) openCmd(env Env) tea.Cmd {
	c := p.detail.cache[tabArtifacts]
	if !c.loaded || c.artifactRel == "" || env.Actions == nil {
		return nil
	}
	rt, name, ok := env.Src.Runtime(p.detail.name)
	if !ok || rt.Store == nil {
		return notice("cannot open " + c.artifactRel)
	}
	rel := c.artifactRel
	path := filepath.Join(rt.Store.ArtifactDir(name, p.detail.round, c.artifactActor), filepath.FromSlash(rel))
	if _, err := os.Stat(path); err != nil {
		if c.artifactErr != nil {
			return notice(c.artifactErr.Error())
		}
		tmp, terr := os.CreateTemp(os.TempDir(), filepath.Base(rel)+"-*")
		if terr != nil {
			return notice(terr.Error())
		}
		if _, werr := tmp.WriteString(c.artifactBody); werr != nil {
			tmp.Close()
			return notice(werr.Error())
		}
		if cerr := tmp.Close(); cerr != nil {
			return notice(cerr.Error())
		}
		path = tmp.Name()
	}

	kind := artifactOpenKind(rel)
	cmd, err := env.Actions.OpenArtifact(path, kind)
	if err != nil {
		return notice(err.Error())
	}
	if kind == "browser" {
		// The browser is started detached, without waiting.
		return func() tea.Msg {
			if serr := cmd.Start(); serr != nil {
				return noticeMsg{text: serr.Error()}
			}
			if cmd.Process != nil {
				cmd.Process.Release()
			}
			return nil
		}
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		return nil
	})
}
