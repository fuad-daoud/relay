package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/usage"
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

func TestCardLinesNameOrderShowsState(t *testing.T) {
	b := relay.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working"}
	got := plain(cardLines(b, false, true, railNow, true, railDefault)[1])
	if !strings.HasPrefix(got, "ACTIVE · working") {
		t.Errorf("line 2 = %q", got)
	}
	got = plain(cardLines(b, false, false, railNow, true, railDefault)[1])
	if strings.Contains(got, "ACTIVE") {
		t.Errorf("attention order must not repeat the state on the card: %q", got)
	}
}

func TestCardLinesTruncateLongName(t *testing.T) {
	b := relay.BindingStatus{Name: strings.Repeat("x", 60), Round: 1, Display: "ACTIVE", BuilderKind: "agy"}
	for i, l := range cardLines(b, false, false, railNow, true, railDefault) {
		if w := lipgloss.Width(l); w != railDefault {
			t.Errorf("line %d width %d", i, w)
		}
	}
}

// TestFactsLiveAndSpend pins the card's facts order (#234): spend first,
// the live figure last, separate cells, never summed; and the exact words
// the plan fixed, read at a rail width the line fits (at the default width
// the existing truncation cuts it, live last).
func TestFactsLiveAndSpend(t *testing.T) {
	b := relay.BindingStatus{
		Spend: &usage.Spend{Rounds: 1, Measured: 0.16, Tokens: usage.Tokens{In: 3_500_000}},
		LiveUsage: &usage.Usage{Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
			Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
			Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3},
	}
	if got := strings.Join(facts(b), " · "); got != "$0.16 · 3.5M tok · live $0.04 · 104k tok" {
		t.Errorf("facts = %q", got)
	}
	card := cardLines(b, false, false, railNow, true, 44)
	if !strings.Contains(stripANSI(card[2]), "$0.16 · 3.5M tok · live $0.04 · 104k tok") {
		t.Errorf("card facts line = %q", stripANSI(card[2]))
	}
}

func TestRailLinesGroupsAndTags(t *testing.T) {
	rows := []relay.BindingStatus{
		// #143: an unread report marks the card's name line with "●".
		{Name: "n", Display: "NEEDS YOU", BuilderKind: "agy", Unread: true},
		{Name: "a1", Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "a2", Display: "ACTIVE", BuilderKind: "agy"},
		// #137: PAUSED groups between ACTIVE and DONE.
		{Name: "p", Display: "PAUSED", BuilderKind: "agy",
			Last: &relay.LastEvent{TS: railNow.Add(-time.Hour), Kind: "pause"}},
		// #143: a live diff fact adds a fourth line to d's card, tacked on
		// last so it does not shift any of the indices this test already
		// asserts on.
		{Name: "d", Display: "DONE", BuilderKind: "agy",
			Live: &relay.LiveDiff{Files: 3, Added: 5, Removed: 2}},
	}
	lines := railLines(rows, 1, true, railNow, true, railDefault, false)
	// header, card n (3 lines), gap, header, card a1 (3), card a2 (3), gap,
	// header, card p (3), gap, header, card d (4: the live-diff fact line), gap
	if len(lines) != 1+3+1+1+3+3+1+1+3+1+1+4+1 {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0].binding != -1 || plain(lines[0].text) != "NEEDS YOU 1" {
		t.Errorf("first line = %+v", lines[0])
	}
	if !strings.Contains(plain(lines[1].text), "●") {
		t.Errorf("unread card's name line must carry the ● marker: %q", plain(lines[1].text))
	}
	if got, want := plain(lines[21].text), "+5 −2 in 3"; got != want {
		t.Errorf("d's live-diff fact line = %q, want %q", got, want)
	}
	if lines[5].binding != -1 || plain(lines[5].text) != "ACTIVE 2" {
		t.Errorf("second header = %+v", lines[5])
	}
	if lines[13].binding != -1 || plain(lines[13].text) != "PAUSED 1" {
		t.Errorf("PAUSED header = %+v", lines[13])
	}
	if lines[18].binding != -1 || plain(lines[18].text) != "DONE 1" {
		t.Errorf("DONE header = %+v", lines[18])
	}
	first, last := railSpan(lines, 1)
	if first != 6 || last != 8 {
		t.Errorf("span of a1 = [%d,%d]", first, last)
	}
	// The PAUSED card sits between the ACTIVE and DONE groups, and its age
	// comes from the pause entry's TS.
	if pf, pl := railSpan(lines, 3); pf != 14 || pl != 16 {
		t.Errorf("span of p = [%d,%d]", pf, pl)
	}
	// Name order: no headers, no gaps, every line tagged.
	for _, l := range railLines(rows, 0, false, railNow, true, railDefault, false) {
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
	lit := cardLines(b, true, false, railNow, true, railDefault)[0]
	dim := cardLines(b, true, false, railNow, false, railDefault)[0]
	if !strings.Contains(lit, accentStyle.Render("▎")) {
		t.Errorf("focused rail: gutter must be accent: %q", lit)
	}
	if !strings.Contains(dim, dimStyle.Render("▎")) || strings.Contains(dim, accentStyle.Render("▎")) {
		t.Errorf("unfocused rail: gutter must be dim: %q", dim)
	}
}

func TestClipName(t *testing.T) {
	if got := clipName("webshop", 10); got != "webshop" {
		t.Errorf("fits: %q", got)
	}
	if got := clipName("spaceapi-ingest", 10); got != "spaceapi-…" || lipgloss.Width(got) != 10 {
		t.Errorf("clipped: %q (%d)", got, lipgloss.Width(got))
	}
	if got := clipName("ab", 2); got != "ab" {
		t.Errorf("exact fit: %q", got)
	}
}

func TestCompactLineShape(t *testing.T) {
	// accentStyle and dimStyle render identically (plain text) under the
	// Ascii profile go test's non-tty output gets by default; force real
	// colour so the two are actually distinguishable, as
	// TestCardGutterDimsWhenRailUnfocused already does for the same reason.
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	b := relay.BindingStatus{Name: "spaceapi-ingest", Round: 12, Display: "NEEDS YOU", BuilderKind: "agy",
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)}}
	l := compactLine(b, true, false, railNow, true, railCompact)
	if w := lipgloss.Width(l); w != railCompact {
		t.Errorf("width %d, want %d", w, railCompact)
	}
	p := plain(l)
	if !strings.HasPrefix(p, "▎ spaceapi-") || !strings.HasSuffix(p, "r12") || !strings.Contains(p, "…") {
		t.Errorf("compact = %q", p)
	}
	if strings.Contains(p, "question") || strings.Contains(p, "2m") {
		t.Errorf("compact carries no qualifier: %q", p)
	}
	short := plain(compactLine(relay.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE"}, false, false, railNow, true, railCompact))
	if short != "api r2" {
		t.Errorf("short name = %q", short)
	}
	// Name order: the state colours the name, since there is no header.
	styled := compactLine(b, false, true, railNow, true, railCompact)
	if !strings.Contains(styled, stateStyle("NEEDS YOU").Bold(true).Render(clipName("spaceapi-ingest", railCompact-1-2-1-3-1))) {
		t.Errorf("name order: the name must take the state colour: %q", styled)
	}
}

func TestRailLinesCompact(t *testing.T) {
	rows := threeRows()
	full := railLines(rows, 0, true, railNow, true, railDefault, false)
	compact := railLines(rows, 0, true, railNow, true, railDefault, true)
	// 3 headers + 3 gaps + 3 one-line cards.
	if len(compact) != 9 {
		t.Errorf("compact rail has %d lines, want 9 (full has %d)", len(compact), len(full))
	}
	for i, l := range compact {
		if l.binding >= 0 && lipgloss.Width(l.text) != railDefault {
			t.Errorf("line %d width %d", i, lipgloss.Width(l.text))
		}
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

func TestFactsSpend(t *testing.T) {
	b := relay.BindingStatus{Name: "x", Spend: &usage.Spend{Rounds: 4, Measured: 1.23, Estimated: 0.40, Unknown: 2}}
	joined := stripANSI(strings.Join(facts(b), " · "))
	if !strings.Contains(joined, "$1.23 · ~$0.40 · 2 unknown") {
		t.Errorf("facts = %q", joined)
	}
	b.Spend = &usage.Spend{Rounds: 2}
	if f := facts(b); len(f) != 0 {
		t.Errorf("a spend with nothing to say adds no fact: %q", f)
	}
	b.Spend = nil
	if f := facts(b); len(f) != 0 {
		t.Errorf("nil spend adds no fact: %q", f)
	}
}

// TestRailArchivedRowFacts pins a hist row's rendering (#172, §5.8): the
// state slot reads "archived <date>", the facts line reads
// "rN · age · feature X", and the name is styled archivedStyle.
func TestRailArchivedRowFacts(t *testing.T) {
	h := relay.HistoryBinding{
		Name: "old-feature", Rounds: 3, Feature: "auth",
		LastActivity: railNow.Add(-2 * time.Hour),
		Archived:     true, ArchivedAt: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	}

	if got := histStateText(h); got != "archived 2026-08-30" {
		t.Errorf("state = %q, want %q", got, "archived 2026-08-30")
	}
	if got := histFacts(h, railNow); got != "r3 · 2h · feature auth" {
		t.Errorf("facts = %q, want %q", got, "r3 · 2h · feature auth")
	}

	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	lines := histCardLines(h, false, false, railNow, 60)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	stylePrefix := strings.SplitN(archivedStyle.Render("X"), "X", 2)[0]
	if !strings.Contains(lines[0], stylePrefix) {
		t.Errorf("name must be rendered with archivedStyle: %q", lines[0])
	}
	if !strings.Contains(plain(lines[0]), "old-feature") || !strings.Contains(plain(lines[0]), "archived 2026-08-30") {
		t.Errorf("line 1 = %q, want the name and the archived date", plain(lines[0]))
	}
	if got := plain(lines[1]); got != "r3 · 2h · feature auth" {
		t.Errorf("line 2 = %q, want %q", got, "r3 · 2h · feature auth")
	}
}

// TestRailArchivedRowNotArchivedReadsDone pins the "done" fallback: a hist
// row the database recorded but never archived (ArchivedAt unset) reads
// "done" in the state slot, not a zero-value date.
func TestRailArchivedRowNotArchivedReadsDone(t *testing.T) {
	h := relay.HistoryBinding{Name: "x", Rounds: 1}
	if got := histStateText(h); got != "done" {
		t.Errorf("state = %q, want %q", got, "done")
	}
}

// TestCardLinesShowsStaleLabel pins #135's rail surface: a NEEDS YOU row
// carries its stale age on the card's second line.
func TestCardLinesShowsStaleLabel(t *testing.T) {
	b := relay.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy",
		Stale:   "stale 4h 0m",
		Waiting: &relay.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute)},
	}
	got := plain(cardLines(b, false, false, railNow, true, railDefault)[1])
	if !strings.Contains(got, "· stale 4h 0m") {
		t.Errorf("card line = %q, want it to carry %q", got, "· stale 4h 0m")
	}
}
