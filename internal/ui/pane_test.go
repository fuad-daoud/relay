package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func paneModel(t *testing.T, b relevo.BindingStatus, active tab) Model {
	t.Helper()
	m := Model{width: 140, height: 40, ready: true, statusLoaded: true, sort: true,
		now: func() time.Time { return railNow }}
	m.report = relevo.Report{Bindings: []relevo.BindingStatus{b}}
	m.detail = detailModel{name: b.Name, round: b.Round - 1, active: active,
		vp: viewport.New(m.paneWidth(), m.viewportHeight())}
	return m
}

func TestPaneHeadRows(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		PlannerID: "planner-9f2", PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "channel",
		BuilderKind: "agy", BuilderStatus: "blocked", Consults: 2,
		Branch: "relevo/webshop", Dirty: true,
		LastClose: &relevo.CloseInfo{Round: 3, Commits: 2, Tree: "clean"},
		Last:      &relevo.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
	}
	m := paneModel(t, b, tabReport)
	head := m.paneHead(&b)
	if len(head) != paneHeadRows {
		t.Fatalf("%d head rows, want %d:\n%s", len(head), paneHeadRows, strings.Join(head, "\n"))
	}
	want := []string{
		"webshop  round 4   NEEDS YOU ",
		"planner  architect-1",
		"builder  agy",
		"tree     relevo/webshop · dirty · last close r3: 2 commits, clean",
	}
	for i, w := range want {
		if got := stripANSI(head[i]); !strings.HasPrefix(got, w) {
			t.Errorf("head[%d]:\n got %q\nwant prefix %q", i, got, w)
		}
	}
	if got := stripANSI(head[1]); !strings.Contains(got, "route channel") {
		t.Errorf("planner row must name the name and the route: %q", got)
	}
	if got := stripANSI(head[2]); !strings.Contains(got, "blocked · 2 consults") {
		t.Errorf("builder row = %q", got)
	}
	if !strings.Contains(stripANSI(head[0]), "question r4 · 2m ago") {
		t.Errorf("title row lacks the last event: %q", stripANSI(head[0]))
	}

	oneCommit := relevo.BindingStatus{
		Name: "ledger", Round: 2, Display: "ACTIVE",
		LastClose: &relevo.CloseInfo{Round: 1, Commits: 1, Tree: "dirty"},
	}
	if got := stripANSI(m.paneHead(&oneCommit)[3]); !strings.Contains(got, "last close r1: 1 commit, dirty") {
		t.Errorf("one-commit tree row = %q", got)
	}
}

func TestPaneHeadHeadlessAndCwd(t *testing.T) {
	b := relevo.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", CWD: "/home/x/api",
		BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1",
		Headless: &relevo.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
	}
	m := paneModel(t, b, tabReport)
	head := m.paneHead(&b)
	if got := stripANSI(head[2]); !strings.Contains(got, "pid 48211 since") || !strings.Contains(got, "`opencode-1`") {
		t.Errorf("builder row = %q", got)
	}
	if got := stripANSI(head[3]); !strings.HasPrefix(got, "tree     /home/x/api") {
		t.Errorf("--cwd tree row = %q", got)
	}
}

func TestTabBarWordsAndUnderline(t *testing.T) {
	m := paneModel(t, relevo.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE"}, tabDiff)
	bar := m.tabBar()
	if len(bar) != tabRows {
		t.Fatalf("%d tab rows", len(bar))
	}
	words := stripANSI(bar[0])
	if strings.ContainsAny(words, "1234") {
		t.Errorf("tabs must not carry numbers: %q", words)
	}
	if !strings.Contains(words, " report ") || !strings.Contains(words, " diff ") {
		t.Errorf("tab words = %q", words)
	}
	if !strings.Contains(bar[0], lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Render(" diff ")) {
		t.Errorf("active tab not bold white: %q", bar[0])
	}
	rule := stripANSI(bar[1])
	if lipgloss.Width(rule) != m.paneWidth() {
		t.Errorf("rule is %d wide, pane is %d", lipgloss.Width(rule), m.paneWidth())
	}
	// The heavy segment sits exactly under the active word.
	start := strings.Index(words, " diff ")
	seg := []rune(rule)[start : start+lipgloss.Width(" diff ")]
	if string(seg) != strings.Repeat("━", len(seg)) {
		t.Errorf("underline under diff = %q", string(seg))
	}
	before := []rune(rule)[:start]
	if strings.ContainsRune(string(before), '━') {
		t.Errorf("heavy rule outside the active word: %q", rule)
	}
	if !strings.Contains(bar[1], accentStyle.Render(strings.Repeat("━", len(seg)))) {
		t.Errorf("underline not in accent: %q", bar[1])
	}
}

func TestDiffStatAndColour(t *testing.T) {
	patch := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"
	files, add, del := diffStat(patch)
	if files != 2 || add != 3 || del != 2 {
		t.Errorf("diffStat = %d files +%d -%d", files, add, del)
	}
	out := strings.Split(colourDiff(patch), "\n")
	if out[0] != diffFileStyle.Render("diff --git a/x.go b/x.go") {
		t.Errorf("file header not styled: %q", out[0])
	}
	if out[3] != diffHunkStyle.Render("@@ -1,2 +1,3 @@") {
		t.Errorf("hunk not styled: %q", out[3])
	}
	if out[5] != diffDelStyle.Render("-old") || out[6] != diffAddStyle.Render("+new") {
		t.Errorf("+/- not styled: %q %q", out[5], out[6])
	}
	if out[1] != diffFileStyle.Render("--- a/x.go") || out[2] != diffFileStyle.Render("+++ b/x.go") {
		t.Errorf("---/+++ must be file headers, not del/add: %q %q", out[1], out[2])
	}
	if out[4] != " context" {
		t.Errorf("context line altered: %q", out[4])
	}
}

func TestSourceLinePerTab(t *testing.T) {
	b := relevo.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	m := paneModel(t, b, tabReport)
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "x", round: 2, at: railNow.Add(-time.Hour)}
	if got := stripANSI(m.sourceLine()); got != "report r2 · 13:02" {
		t.Errorf("report source = %q", got)
	}
	m.detail.active = tabTerminal
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second)}
	if got := stripANSI(m.sourceLine()); got != "remote · captured 1s ago · 3 lines" {
		t.Errorf("terminal source = %q", got)
	}
	b.Headless = &relevo.HeadlessInfo{LogPath: "/x/002-builder.log"}
	m.report = relevo.Report{Bindings: []relevo.BindingStatus{b}}
	m.detail.headless = true
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second),
		transcript: true, logName: "002-builder.log"}
	m.detail.follow = true
	if got := stripANSI(m.sourceLine()); got != "headless · 002-builder.log · 3 lines · following" {
		t.Errorf("headless following source = %q", got)
	}
	m.detail.follow = false
	if got := stripANSI(m.sourceLine()); got != "headless · 002-builder.log · 3 lines · scrolled" {
		t.Errorf("headless scrolled source = %q", got)
	}
	m.detail.headless = false
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second),
		transcript: true, logName: "002-builder.log"}
	m.detail.follow = true
	if got := stripANSI(m.sourceLine()); got != "pane · 002-builder.log · 3 lines · following" {
		t.Errorf("pane transcript source = %q", got)
	}
	m.detail.follow = false
	m.detail.active = tabDiff
	m.detail.cache[tabDiff] = tabContent{loaded: true, body: "diff --git a/x b/x\n+a\n-b\n"}
	if got := stripANSI(m.sourceLine()); got != "round 2 · 1 file · +1 −1" {
		t.Errorf("diff source = %q", got)
	}
	m.detail.active = tabLog
	m.detail.cache[tabLog] = tabContent{loaded: true, body: "e1\ne2"}
	if got := stripANSI(m.sourceLine()); got != "2 entries" {
		t.Errorf("log source = %q", got)
	}
	m.detail.active = tabReport
	m.detail.cache[tabReport] = tabContent{}
	if got := stripANSI(m.sourceLine()); got != "loading…" {
		t.Errorf("unloaded source = %q", got)
	}
}

func TestHintLineOnlyForBlockedTerminal(t *testing.T) {
	b := relevo.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &relevo.Waiting{Cause: "blocked", Hint: "relevo answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	line, ok := m.hintLine(&b)
	if !ok || stripANSI(line) != "relevo: relevo answer --name webshop" {
		t.Errorf("hint = %q ok=%v", stripANSI(line), ok)
	}
	m.detail.active = tabReport
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must only show on the terminal tab")
	}
	m.detail.active = tabTerminal
	b.Waiting.Cause = "halted"
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must only show for a blocked builder")
	}
	b.Waiting = nil
	if _, ok := m.hintLine(&b); ok {
		t.Error("hint must not show without Waiting")
	}
}

func TestPaneViewRowsAndWidth(t *testing.T) {
	b := relevo.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &relevo.Waiting{Cause: "blocked", Hint: "relevo answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("screen line\n", 50)}
	m.detail.vp.SetContent(bodyOf(tabTerminal, m.detail.cache[tabTerminal], false))
	view := m.paneView(m.paneWidth())
	lines := strings.Split(view, "\n")
	if len(lines) != m.bodyRows() {
		t.Fatalf("%d pane rows, want bodyRows %d", len(lines), m.bodyRows())
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > m.paneWidth() {
			t.Errorf("row %d is %d wide, pane is %d: %q", i, w, m.paneWidth(), stripANSI(l))
		}
	}
	if got := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(got, "relevo: relevo answer") {
		t.Errorf("last pane row must be the hint, got %q", got)
	}
}

func TestWrapBodyMakesEveryLineReachable(t *testing.T) {
	long := "alpha " + strings.Repeat("word ", 40) + "omega"
	body := long + "\nsecond\nthird"
	wrapped := wrapBody(body, 50)
	for i, l := range strings.Split(wrapped, "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("line %d is %d wide: %q", i, w, l)
		}
	}
	if !strings.Contains(wrapped, "omega") || !strings.Contains(wrapped, "third") {
		t.Errorf("wrapping lost text:\n%s", wrapped)
	}
	// A path with no spaces still breaks rather than overflowing.
	path := strings.Repeat("/abcdefghij", 12)
	for i, l := range strings.Split(wrapBody(path, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("path line %d is %d wide", i, w)
		}
	}
	// Styled input keeps its styling and its width.
	styled := colourDiff("+" + strings.Repeat("x", 120))
	for i, l := range strings.Split(wrapBody(styled, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("styled line %d is %d wide", i, w)
		}
	}
}

func TestColourTranscript(t *testing.T) {
	body := "Now running the tests.\n● Bash go test ./...\n  ⎿ ok: ok  github.com/x 0.4s\n● Read\n  ⎿ error: no such file\n[system]"
	out := strings.Split(colourTranscript(body), "\n")
	if out[0] != "Now running the tests." {
		t.Errorf("prose must be untouched: %q", out[0])
	}
	if p := stripANSI(out[1]); p != "● Bash(go test ./...)" {
		t.Errorf("call = %q", p)
	}
	if !strings.Contains(out[1], stateActiveStyle.Render("●")) || !strings.Contains(out[1], lipgloss.NewStyle().Bold(true).Render("Bash")) {
		t.Errorf("call not styled: %q", out[1])
	}
	if p := stripANSI(out[2]); p != "  ⎿ ok: ok  github.com/x 0.4s" {
		t.Errorf("ok result text changed: %q", p)
	}
	if out[2] != dimStyle.Render("  ⎿ ok: ok  github.com/x 0.4s") {
		t.Errorf("ok result not dim: %q", out[2])
	}
	if p := stripANSI(out[3]); p != "● Read" {
		t.Errorf("call without argument = %q (no empty parens)", p)
	}
	if out[4] != errorStyle.Render("  ⎿ error: no such file") {
		t.Errorf("error result not styled: %q", out[4])
	}
	if out[5] != "[system]" {
		t.Errorf("unknown-event line must be untouched: %q", out[5])
	}
}

func TestBodyOfStylesOnlyHeadlessTerminal(t *testing.T) {
	c := tabContent{loaded: true, body: "● Bash ls"}
	if got := bodyOf(tabTerminal, c, false); got != "● Bash ls" {
		t.Errorf("a pane capture must never be restyled: %q", got)
	}
	if got := bodyOf(tabTerminal, c, true); stripANSI(got) != "● Bash(ls)" {
		t.Errorf("headless terminal = %q", stripANSI(got))
	}
	if got := bodyOf(tabLog, c, true); got != "● Bash ls" {
		t.Errorf("only the terminal tab styles transcript lines: %q", got)
	}
	// A pane builder's rendered session record (#184) colours markers too,
	// even though the builder itself is not headless.
	pc := tabContent{loaded: true, body: "● Bash ls", transcript: true}
	if got := bodyOf(tabTerminal, pc, false); stripANSI(got) != "● Bash(ls)" {
		t.Errorf("pane transcript terminal = %q", stripANSI(got))
	}
}

func TestViewportReachesBottomOfLongLines(t *testing.T) {
	b := relevo.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	m := paneModel(t, b, tabLog)
	m.detail.vp.Width = 40
	m.detail.vp.Height = 3
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("entry %d %s END%d", i, strings.Repeat("w ", 30), i))
	}
	m.detail.cache[tabLog] = tabContent{loaded: true, body: strings.Join(lines, "\n")}
	m.fillViewport()
	m.detail.vp.GotoBottom()
	if v := stripANSI(m.detail.vp.View()); !strings.Contains(v, "END4") {
		t.Errorf("the last line must be reachable at the bottom, got:\n%s", v)
	}
	if m.detail.vp.TotalLineCount() <= 5 {
		t.Errorf("wrapped content must have more logical lines than raw (%d)", m.detail.vp.TotalLineCount())
	}
}
