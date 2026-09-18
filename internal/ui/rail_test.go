package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/muesli/termenv"
)

// railNow is in the local zone on purpose: the ui formats clocks with
// .Local(), and a UTC fixture would make "13:02" depend on the machine.
var railNow = time.Date(2026, 9, 17, 14, 2, 0, 0, time.Local)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// plain strips ANSI and collapses runs of spaces, so a card assertion
// pins the words and their order, not the padding fit() adds.
func plain(s string) string { return strings.Join(strings.Fields(stripANSI(s)), " ") }

func TestCardLinesShapes(t *testing.T) {
	blocked := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked", Branch: "relay/webshop", Dirty: true, Consults: 2,
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)},
	}
	held := relay.BindingStatus{
		Name: "ledger", Round: 3, Display: "HELD", BuilderKind: "agy", Branch: "relay/ledger",
		Pending: &relay.PendingInfo{Round: 3, Kind: store.KindReport, Hold: &relay.HoldInfo{QuietMS: 23000, GraceMS: 60000}},
	}
	headless := relay.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "opencode", BuilderStatus: "working",
		Branch: "relay/api", Headless: &relay.HeadlessInfo{PID: 48211},
	}
	cwd := relay.BindingStatus{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy",
		Last: &relay.LastEvent{TS: railNow.Add(-3 * time.Hour)}}

	cases := []struct {
		name string
		b    relay.BindingStatus
		want []string // plain text, trailing spaces trimmed
	}{
		{"blocked", blocked, []string{
			"▎ webshop r4",
			"question · 2m",
			"dirty · 2 consults",
			"agy · relay/webshop",
		}},
		{"held", held, []string{
			"ledger r3",
			"report r3 · quiet 23s of 1m0s",
			"agy · relay/ledger",
		}},
		{"headless", headless, []string{
			"api r2",
			"working",
			"opencode · headless · relay/api",
		}},
		{"cwd done", cwd, []string{
			"docs r1",
			"done · 3h",
			"agy",
		}},
	}
	for _, tc := range cases {
		got := cardLines(tc.b, tc.name == "blocked", false, railNow, true)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d lines, want %d:\n%s", tc.name, len(got), len(tc.want), strings.Join(got, "\n"))
		}
		for i := range got {
			if w := lipgloss.Width(got[i]); w != railWidth {
				t.Errorf("%s line %d width %d, want %d: %q", tc.name, i, w, railWidth, got[i])
			}
			if p := plain(got[i]); p != tc.want[i] {
				t.Errorf("%s line %d:\n got %q\nwant %q", tc.name, i, p, tc.want[i])
			}
		}
	}
}

func TestCardLinesNameOrderShowsState(t *testing.T) {
	b := relay.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"}
	got := plain(cardLines(b, false, true, railNow, true)[1])
	if !strings.HasPrefix(got, "ACTIVE · working") {
		t.Errorf("line 2 = %q", got)
	}
	got = plain(cardLines(b, false, false, railNow, true)[1])
	if strings.Contains(got, "ACTIVE") {
		t.Errorf("attention order must not repeat the state on the card: %q", got)
	}
}

func TestCardLinesTruncateLongName(t *testing.T) {
	b := relay.BindingStatus{Name: strings.Repeat("x", 60), Round: 1, Display: "ACTIVE", BuilderKind: "agy"}
	for i, l := range cardLines(b, false, false, railNow, true) {
		if w := lipgloss.Width(l); w != railWidth {
			t.Errorf("line %d width %d", i, w)
		}
	}
}

func TestRailLinesGroupsAndTags(t *testing.T) {
	rows := []relay.BindingStatus{
		{Name: "n", Display: "NEEDS YOU", BuilderKind: "agy"},
		{Name: "a1", Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "a2", Display: "ACTIVE", BuilderKind: "agy"},
	}
	lines := railLines(rows, 1, true, railNow, true)
	// header, card n (3 lines), gap, header, card a1 (3), card a2 (3), gap
	if len(lines) != 1+3+1+1+3+3+1 {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0].binding != -1 || plain(lines[0].text) != "NEEDS YOU 1" {
		t.Errorf("first line = %+v", lines[0])
	}
	if lines[5].binding != -1 || plain(lines[5].text) != "ACTIVE 2" {
		t.Errorf("second header = %+v", lines[5])
	}
	first, last := railSpan(lines, 1)
	if first != 6 || last != 8 {
		t.Errorf("span of a1 = [%d,%d]", first, last)
	}
	// Name order: no headers, no gaps, every line tagged.
	for _, l := range railLines(rows, 0, false, railNow, true) {
		if l.binding < 0 {
			t.Errorf("name order emitted an untagged line %q", plain(l.text))
		}
	}
}

func TestCardGutterDimsWhenRailUnfocused(t *testing.T) {
	// accentStyle and dimStyle render identically (plain text) under the
	// Ascii profile go test's non-tty output gets by default; force real
	// colour so the two are actually distinguishable, as detail_test.go and
	// list_test.go already do for the same reason.
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	b := relay.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"}
	lit := cardLines(b, true, false, railNow, true)[0]
	dim := cardLines(b, true, false, railNow, false)[0]
	if !strings.Contains(lit, accentStyle.Render("▎")) {
		t.Errorf("focused rail: gutter must be accent: %q", lit)
	}
	if !strings.Contains(dim, dimStyle.Render("▎")) || strings.Contains(dim, accentStyle.Render("▎")) {
		t.Errorf("unfocused rail: gutter must be dim: %q", dim)
	}
}

func TestRailWindowSpan(t *testing.T) {
	cases := []struct{ top, first, last, rows, n, want int }{
		{0, 0, 0, 5, 3, 0},   // everything fits
		{0, 8, 10, 5, 20, 6}, // span below the window: pull down so last is visible
		{10, 2, 4, 5, 20, 2}, // span above: pull up to first
		{0, 3, 12, 5, 20, 3}, // span taller than rows: pin to first
		{18, 0, 0, 5, 20, 0}, // clamp then follow
	}
	for _, c := range cases {
		if got := railWindow(c.top, c.first, c.last, c.rows, c.n); got != c.want {
			t.Errorf("railWindow(%d,%d,%d,%d,%d) = %d, want %d", c.top, c.first, c.last, c.rows, c.n, got, c.want)
		}
	}
}
