// Package dash is the drawer's dashboard screen: a totals line, a grid of
// every round matching a query line, an optional regroup by axis with sums,
// sortable, over one db.Query per refresh
// (docs/specs/2026-09-21-dashboard-design.md §6).
//
// It is its own model, hosted by internal/ui's fleet Model the way the
// detail pane is. It must never import internal/ui: the host draws it and
// the host imports it, not the other way round (§2).
package dash

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

// Model is the dashboard screen's whole state (§3).
type Model struct {
	db *db.DB // nil -> the host never enters the screen
	// loc decides where a round's day starts (histq.Group's day axis) and
	// which zone the started column prints in.
	loc *time.Location
	now func() time.Time

	query histq.Query // the applied query
	text  string      // the applied query's text (prefs)

	input    textinput.Model // the / editor
	editing  bool
	parseErr string // "" when none

	rows   []db.RoundRow    // last good result after Apply
	groups []histq.GroupRow // when query.By != none
	tiles  histq.Tiles

	// expanded is the group keys whose rounds are shown beneath them.
	expanded map[string]bool

	cursor   int // index into the visible lines (see visible())
	sortKey  string
	sortDesc bool

	fetching  bool
	fetchErr  string
	lastFetch time.Time

	width, height int

	// vp is the grid's window: header lines 1-3 are fixed, the grid
	// scrolls under them, "the viewport follows" the cursor (§6).
	vp viewport.Model

	// fetchFn is the test seam Task 1 asks for: it defaults to the db path
	// (fetchDB below), so a test can hand the model literal rows.
	fetchFn func(ctx context.Context, q histq.Query) ([]db.RoundRow, error)

	// styles is the host's palette, set with SetStyles (§5).
	styles Styles

	// Notice is a host-owned line the host drops in at view time: the
	// dashboard has no footer, and §6 needs a notice on screen for a jump
	// whose name vanished. "" when there is none.
	Notice string

	// Names resolves a candidate token to its display name (§5). A nil value
	// means identity.
	Names func(token string) string

	// Embedded is true when the dashboard is hosted as a view inside the
	// cockpit shell rather than owning the whole screen (B1 round 2). The
	// shell draws the identity line itself, so View omits headerLine and
	// gridHeight reclaims its row.
	Embedded bool
}

// Styles is the handful of internal/ui styles the dashboard borrows rather
// than duplicating colours (§5). The host builds it from its own vars and
// hands it over with SetStyles: the dash must not import internal/ui.
type Styles struct {
	Fg        lipgloss.Style
	Dim       lipgloss.Style
	Faint     lipgloss.Style
	Error     lipgloss.Style
	Empty     lipgloss.Style
	Selected  lipgloss.Style
	Archived  lipgloss.Style
	Attention lipgloss.Style
	Live      lipgloss.Style
}

// RowsMsg is one successful refresh: the rows the query selected.
type RowsMsg struct {
	Rows []db.RoundRow
	At   time.Time
}

// ErrMsg is one failed refresh: the rows already on screen stay.
type ErrMsg struct{ Err error }

// JumpMsg is what the host receives when enter is pressed on a round row.
type JumpMsg struct {
	BindingName string
	BindingID   string
	Round       int
	Live        bool // unknown here; host decides
}

// New builds the screen for d, in loc, reading the clock through now.
// queryText and sortKey are the persisted prefs; an empty sortKey takes
// each level's default. An unparseable stored query is no query at all:
// the screen opens on every round rather than on a broken state.
func New(d *db.DB, loc *time.Location, now func() time.Time, queryText, sortKey string) Model {
	if loc == nil {
		loc = time.Local
	}
	if now == nil {
		now = time.Now
	}
	in := textinput.New()
	in.Prompt = ""
	m := Model{
		db:       d,
		loc:      loc,
		now:      now,
		input:    in,
		expanded: map[string]bool{},
		sortKey:  sortKey,
		sortDesc: true, // newest / biggest first
		vp:       viewport.New(0, 0),
	}
	if q, err := histq.ParseAt(queryText, now()); err == nil {
		m.query = q
		m.text = q.Raw
	} else {
		m.query = histq.Query{By: histq.AxisNone}
		m.query.Filter.Newest = true
	}
	m.groups = histq.Group(m.rows, m.query.By, m.loc)
	return m
}

// SetStyles hands the screen the host's palette (§5).
func (m *Model) SetStyles(s Styles) { m.styles = s }

// Init is the first fetch.
func (m Model) Init() tea.Cmd {
	_, cmd := m.startFetch()
	return cmd
}

// QueryText is the applied query's text, for prefs.
func (m Model) QueryText() string { return m.text }

// SortKey is the sort column, for prefs. It is never empty: an unset key
// reads as the round level's default ("started").
func (m Model) SortKey() string {
	if m.sortKey == "" {
		return roundSortKeys[0]
	}
	return m.sortKey
}

// Editing reports whether the / editor has the keyboard, so the host knows
// whether d/esc leave the screen or belong to the input.
func (m Model) Editing() bool { return m.editing }

// GroupAxis is the applied query's regroup axis when it regroups at all, and
// "" otherwise. The host's context line names it; the screen's own header
// line is omitted when embedded.
func (m Model) GroupAxis() string {
	if !m.grouped() {
		return ""
	}
	return string(m.query.By)
}

// SetSize fits the screen to the terminal. The three fixed lines are the
// header, the tiles and the editor/column header; the rest is the grid.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	if w > 3 {
		m.input.Width = w - 3
	}
	m.vp.Width = w
	m.vp.Height = m.gridHeight()
}

// ShouldRefresh reports whether the host's tick owes the screen a fetch: at
// most every 10s, and never one already in flight.
func (m Model) ShouldRefresh(now time.Time) bool {
	if m.fetching {
		return false
	}
	return m.lastFetch.IsZero() || now.Sub(m.lastFetch) >= 10*time.Second
}

// Refresh starts a fetch now (the `r` key, and the host's tick once
// ShouldRefresh says so). It is a no-op while one is in flight.
func (m *Model) Refresh() tea.Cmd {
	next, cmd := m.startFetch()
	*m = next
	return cmd
}

// Update is the screen's whole message handling. It returns dash.Model, not
// tea.Model: the host wraps it (§4).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateKeys(msg)

	case RowsMsg:
		m.fetching = false
		m.fetchErr = ""
		m.rows = msg.Rows
		m.groups = histq.Group(m.rows, m.query.By, m.loc)
		m.tiles = histq.Totals(m.rows)
		m.lastFetch = msg.At
		m.clampCursor()
		return m, nil

	case ErrMsg:
		m.fetching = false
		m.fetchErr = msg.Err.Error()
		return m, nil
	}
	return m, nil
}

// updateKeys is the un-editing key map (§4).
func (m Model) updateKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		m.editing = true
		m.parseErr = ""
		m.input.SetValue(m.text)
		m.input.Focus()
		m.input.CursorEnd()
		return m, nil

	case "b":
		m.query.By = nextAxis(m.query.By)
		m.groups = histq.Group(m.rows, m.query.By, m.loc)
		m.cursor = 0
		return m, nil

	case "s":
		m.cycleSort()
		return m, nil

	case "S":
		m.sortDesc = !m.sortDesc
		return m, nil

	case "enter":
		lines := m.visible()
		if m.cursor < 0 || m.cursor >= len(lines) {
			return m, nil // an empty grid: enter is a no-op (§6)
		}
		switch l := lines[m.cursor]; l.kind {
		case lineGroup:
			m.expanded[l.group.Key] = !m.expanded[l.group.Key]
			return m, nil
		default:
			r := l.row
			return m, func() tea.Msg {
				return JumpMsg{BindingName: r.BindingName, BindingID: r.BindingID, Round: r.Number}
			}
		}

	case "r":
		return m.startFetch()

	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(+1)
	case "pgup":
		m.moveCursor(-m.pageSize())
	case "pgdown":
		m.moveCursor(m.pageSize())
	case "home":
		m.moveCursorTo(0)
	case "end":
		m.moveCursorTo(len(m.visible()) - 1)
	}
	return m, nil
}

// moveCursor moves the cursor delta lines, clamped to the visible list.
func (m *Model) moveCursor(delta int) { m.moveCursorTo(m.cursor + delta) }

func (m *Model) moveCursorTo(c int) {
	n := len(m.visible())
	if n == 0 {
		m.cursor = 0
		return
	}
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	m.cursor = c
}

// clampCursor keeps the cursor inside the visible list after a refresh.
func (m *Model) clampCursor() {
	n := len(m.visible())
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor > n-1 {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// pageSize is one screenful of grid.
func (m Model) pageSize() int {
	n := m.gridHeight()
	if n < 1 {
		return 1
	}
	return n
}

// gridHeight is the rows left for the grid under the fixed lines: three
// when the screen owns its identity line, two when it is embedded and the
// host draws that line.
func (m Model) gridHeight() int {
	header := dashHeaderRows
	if m.Embedded {
		header--
	}
	h := m.height - header
	if h < 0 {
		return 0
	}
	return h
}

// nextAxis is `b`'s cycle: none -> binding -> ... -> outcome -> none, the
// order histq lists them in.
func nextAxis(a histq.Axis) histq.Axis {
	axes := histq.Axes()
	for i, v := range axes {
		if v == a {
			return axes[(i+1)%len(axes)]
		}
	}
	return axes[1%len(axes)] // an unknown axis restarts the walk at binding
}

// cycleSort advances the sort column for the level under the cursor: the
// next key after the level's effective key now, wrapping at the end.
func (m *Model) cycleSort() {
	keys := roundSortKeys
	cur := m.roundSortKey()
	if m.groupedLevel() {
		keys = groupSortKeys
		cur = m.groupSortKey()
	}
	m.sortKey = keys[(indexOf(keys, cur)+1)%len(keys)]
}

// groupedLevel reports whether the cursor is on a group row, so `s` cycles
// the group keys rather than the round keys.
func (m Model) groupedLevel() bool {
	if !m.grouped() {
		return false
	}
	lines := m.visible()
	if m.cursor < 0 || m.cursor >= len(lines) {
		return true
	}
	return lines[m.cursor].kind == lineGroup
}

// grouped reports whether the applied query regroups at all.
func (m Model) grouped() bool {
	return m.query.By != histq.AxisNone && m.query.By != ""
}

// View draws the three fixed lines and the scrolled grid (§5). The host
// sets Notice before calling when it has one to show.
func (m Model) View() string {
	m.vp.Width = m.width
	m.vp.Height = m.gridHeight()
	m.vp.SetContent(m.gridContent())
	m.vp.SetYOffset(m.windowTop())
	body := m.tilesLine() + "\n" + m.thirdLine() + "\n" + m.vp.View()
	if m.Embedded {
		return body
	}
	return m.headerLine() + "\n" + body
}
