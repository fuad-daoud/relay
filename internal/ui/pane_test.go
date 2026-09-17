package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

func paneModel(t *testing.T, b relay.BindingStatus, active tab) Model {
	t.Helper()
	m := Model{width: 140, height: 40, ready: true, statusLoaded: true, sort: true,
		now: func() time.Time { return railNow }}
	m.report = relay.Report{Bindings: []relay.BindingStatus{b}}
	m.detail = detailModel{name: b.Name, round: b.Round - 1, active: active,
		vp: viewport.New(m.paneWidth(), m.viewportHeight())}
	return m
}

func TestPaneHeadRows(t *testing.T) {
	b := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		PlannerPane: "%1", PlannerKind: "claude", PlannerStatus: "idle", PlannerFocus: true,
		BuilderPane: "%7", BuilderKind: "agy", BuilderStatus: "blocked", Consults: 2,
		Branch: "relay/webshop", Dirty: true,
		LastClose: &relay.CloseInfo{Round: 3, Commits: 2, Tree: "a1c9f0e1234567"},
		Last:      &relay.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
	}
	m := paneModel(t, b, tabReport)
	head := m.paneHead(&b)
	if len(head) != paneHeadRows {
		t.Fatalf("%d head rows, want %d:\n%s", len(head), paneHeadRows, strings.Join(head, "\n"))
	}
	want := []string{
		"webshop  round 4   NEEDS YOU ",
		"planner  %1   claude    idle · focused",
		"builder  %7   agy       blocked · 2 consults",
		"tree     relay/webshop · dirty · last close a1c9f0e (2 commits)",
	}
	for i, w := range want {
		if got := stripANSI(head[i]); !strings.HasPrefix(got, w) {
			t.Errorf("head[%d]:\n got %q\nwant prefix %q", i, got, w)
		}
	}
	if !strings.Contains(stripANSI(head[0]), "question r4 · 2m ago") {
		t.Errorf("title row lacks the last event: %q", stripANSI(head[0]))
	}
	b.Foreign = []relay.ForeignAgent{{PaneID: "%9", Kind: "claude", Status: "working", Title: "reviewer"}}
	if got := len(m.paneHead(&b)); got != paneHeadRows+1 {
		t.Errorf("with a foreign agent: %d rows, want %d", got, paneHeadRows+1)
	}
}

func TestPaneHeadHeadlessAndCwd(t *testing.T) {
	b := relay.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", CWD: "/home/x/api",
		BuilderPane: "headless", BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1",
		Headless: &relay.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
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

func TestTabBarMarksActive(t *testing.T) {
	m := paneModel(t, relay.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE"}, tabDiff)
	bar := m.tabBar()
	if len(bar) != tabRows {
		t.Fatalf("%d tab rows", len(bar))
	}
	if got := stripANSI(bar[0]); !strings.Contains(got, " 1 report ") || !strings.Contains(got, " 3 diff ") {
		t.Errorf("tab bar = %q", got)
	}
	if !strings.Contains(bar[0], activeTabStyle.Render(" 3 diff ")) {
		t.Errorf("diff tab not styled active: %q", bar[0])
	}
	if !strings.HasPrefix(stripANSI(bar[1]), "───") {
		t.Errorf("rule row = %q", stripANSI(bar[1]))
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
	b := relay.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE", BuilderPane: "%7"}
	m := paneModel(t, b, tabReport)
	m.detail.cache[tabReport] = tabContent{loaded: true, body: "x", round: 2, at: railNow.Add(-time.Hour)}
	if got := stripANSI(m.sourceLine()); got != "report r2 · 13:02" {
		t.Errorf("report source = %q", got)
	}
	m.detail.active = tabTerminal
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second)}
	if got := stripANSI(m.sourceLine()); got != "%7 · captured 1s ago · 3 lines" {
		t.Errorf("terminal source = %q", got)
	}
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
	b := relay.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &relay.Waiting{Cause: "blocked", Hint: "relay answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	line, ok := m.hintLine(&b)
	if !ok || stripANSI(line) != "relay: relay answer --name webshop" {
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
	b := relay.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderPane: "%7",
		Waiting: &relay.Waiting{Cause: "blocked", Hint: "relay answer --name webshop"}}
	m := paneModel(t, b, tabTerminal)
	m.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("screen line\n", 50)}
	m.detail.vp.SetContent(bodyOf(tabTerminal, m.detail.cache[tabTerminal]))
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
	if got := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(got, "relay: relay answer") {
		t.Errorf("last pane row must be the hint, got %q", got)
	}
}
