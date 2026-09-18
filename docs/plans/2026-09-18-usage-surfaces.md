# Usage surfaces: round line, binding total, `relay tab` (#142, slice 2)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-18-usage-surfaces-design.md` -- read
it first. Slice 1's spec, `docs/specs/2026-09-18-round-usage-design.md`,
defines the `usage.Usage` you are printing.
**Issue:** #142 (this slice closes it). Follow-ups: #186.

**Goal:** print what slice 1 recorded, in one vocabulary, on every surface
a human reads a round: `relay log`, `relay status` (text and `--json`),
`relay ui` (rail card, header, log tab), and a new `relay tab` that sums
across bindings -- archived ones included -- by binding, model or
provider.

**Architecture:** every rendering rule is a pure function in
`internal/usage` (`Money`, `Line`, `Sum`, `SpendLine`, `MoneyShort`).
`relay.LogLine` is the one text form of a log entry, shared by the CLI and
the ui. `Status` gains `LastUsage` and `Spend` next to `LastClose`. `tab`'s
core is `relay.TabRows` over entries `cmd/relay` collects from live logs
and archive tarballs.

**Tech stack:** Go standard library; `archive/tar` + `compress/gzip` for
archives (already used by `internal/store`). No new module dependency.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/usage-surfaces` | **the git worktree. Every source edit goes here.** Your shell's cwd, branch `relay/usage-surfaces`. |
| `~/.local/state/relay/usage-surfaces` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**. Do not bend a test to fit.

## Running commands

`make` is intercepted on this machine; run the constituents directly and
say so in the report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr` or any harness. No test under `cmd/relay`.

## Global constraints

- No new `go.mod` dependencies.
- The cost vocabulary is exactly: `$0.41` measured, `~$0.41` estimated,
  `plan`, `unknown`. Measured and estimated dollars are never added into
  one figure. A plan lane's dollars are not summed as cash. `unknown` is a
  count, never `$0`.
- `relay.LogLine`'s first line is byte-identical to today's
  `relay log` line; the second line exists only when `Usage != nil`.
- `statusline.go` is untouched.
- `relay tab` is #114 freeze exception #2 -- recorded in the spec, so it
  needs no further justification in code; just add it.
- One commit per task, prefixed `feat(usage):` (1-2), `feat(relay):`
  (3-4, 7), `feat(ui):` (5), `feat(store):` (6).

---

### Task 1: `internal/usage` formatters -- `Money`, `ShortTokens`, `ShortDuration`, `Line`

**Files:**
- Create: `internal/usage/format.go`
- Test: `internal/usage/format_test.go`
- Copy in (Step 0): the two design docs

**Interfaces:**
- Consumes: `Usage`, `Cost`, `Tokens`, `Basis` (slice 1).
- Produces:

```go
func Money(c Cost) string
func ShortTokens(n int64) string
func ShortDuration(ms int64) string
func Line(u Usage) string
```

- [ ] **Step 0: Bring the design docs into the branch.**

```bash
mkdir -p docs/specs docs/plans
cp /home/fuad/projects/relay/docs/specs/2026-09-18-usage-surfaces-design.md docs/specs/
cp /home/fuad/projects/relay/docs/plans/2026-09-18-usage-surfaces.md docs/plans/
```

- [ ] **Step 1: Write the failing tests.**

`internal/usage/format_test.go`:

```go
package usage

import "testing"

func TestMoney(t *testing.T) {
	cases := []struct {
		c    Cost
		want string
	}{
		{Cost{USD: 0.41, Basis: Measured}, "$0.41"},
		{Cost{USD: 0.41, Basis: Estimated}, "~$0.41"},
		{Cost{USD: 0.41, Basis: Measured, Plan: true}, "plan"},
		{Cost{USD: 0, Basis: Unknown}, "unknown"},
		{Cost{USD: 0, Basis: Measured}, "$0.00"},
		{Cost{USD: 0.004, Basis: Measured}, "<$0.01"},
		{Cost{USD: 0.005, Basis: Estimated}, "~$0.01"},
		{Cost{USD: 12.346, Basis: Measured}, "$12.35"},
	}
	for _, c := range cases {
		if got := Money(c.c); got != c.want {
			t.Errorf("Money(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestShortTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1k", 1500: "2k", 182_400: "182k", 999_499: "999k", 999_500: "1.0M", 2_340_000: "2.3M"}
	for in, want := range cases {
		if got := ShortTokens(in); got != want {
			t.Errorf("ShortTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[int64]string{0: "", 1: "<1m", 59_999: "<1m", 60_000: "1m", 14 * 60_000: "14m", 65 * 60_000: "1h05m", 3 * 3_600_000: "3h00m"}
	for in, want := range cases {
		if got := ShortDuration(in); got != want {
			t.Errorf("ShortDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLine(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want string
	}{
		{
			name: "estimated, the issue's example",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 14 * 60_000,
				Tokens: Tokens{In: 16_000, CacheRead: 166_000, CacheWrite: 400, Out: 12_000},
				Cost:   Cost{USD: 0.41, Basis: Estimated}, Samples: 3},
			want: "claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41",
		},
		{
			name: "measured",
			u: Usage{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash", DurationMS: 90_000,
				Tokens: Tokens{In: 465_339, CacheRead: 6_656_256, Out: 57_612}, Cost: Cost{USD: 0.2983, Basis: Measured}, Samples: 95},
			want: "opencode/openrouter/z-ai/glm-5.3-flash  1m  in 7.1M (cache 93%)  out 58k  $0.30",
		},
		{
			name: "unknown with tokens",
			u: Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000,
				Tokens: Tokens{In: 680_097, CacheRead: 2_333_950, Out: 41_527}, Cost: Cost{Basis: Unknown}, Samples: 1,
				Note: "no price for google/gemini-3.8-flash-high"},
			want: "agy/google/gemini-3.8-flash-high  9m  in 3.0M (cache 77%)  out 42k  unknown (no price for google/gemini-3.8-flash-high)",
		},
		{
			name: "unknown, no samples",
			u:    Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000, Cost: Cost{Basis: Unknown}, Note: "agy keeps no usage record"},
			want: "agy/google/gemini-3.8-flash-high  9m  unknown (agy keeps no usage record)",
		},
		{
			name: "adopted builder, nothing known",
			u:    Usage{Harness: "claude", Cost: Cost{Basis: Unknown}, Note: "no stream"},
			want: "claude  unknown (no stream)",
		},
		{
			name: "plan lane",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5", DurationMS: 60_000,
				Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 1, Basis: Estimated, Plan: true}, Samples: 1},
			want: "claude/anthropic/claude-opus-5  1m  in 100 (cache 0%)  out 50  plan",
		},
		{
			name: "no prompt tokens, no cache parenthetical",
			u:    Usage{Harness: "claude", Provider: "anthropic", Model: "m", Tokens: Tokens{Out: 5}, Cost: Cost{USD: 0.01, Basis: Measured}, Samples: 1},
			want: "claude/anthropic/m  in 0  out 5  $0.01",
		},
	}
	for _, c := range cases {
		if got := Line(c.u); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/usage/ -run 'TestMoney|TestShort|TestLine'`
Expected: build failure, undefined functions.

- [ ] **Step 3: Write `format.go`.**

```go
package usage

import (
	"fmt"
	"math"
	"strings"
)

// Money is the cost word. Plan wins over basis: a subscription lane never
// shows dollars. Under a cent prints "<$0.01" so a tiny round is not
// mistaken for a free one.
func Money(c Cost) string {
	switch {
	case c.Plan:
		return "plan"
	case c.Basis == Unknown:
		return "unknown"
	}
	prefix := "$"
	if c.Basis == Estimated {
		prefix = "~$"
	}
	if c.USD > 0 && c.USD < 0.005 {
		return "<" + prefix + "0.01"
	}
	return fmt.Sprintf("%s%.2f", prefix, c.USD)
}

// ShortTokens: < 1000 as-is, < 1M "182k", else "2.3M". Rounds half up.
func ShortTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 999_500:
		return fmt.Sprintf("%dk", int64(math.Round(float64(n)/1000)))
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// ShortDuration: "" for 0, "<1m" under a minute, "14m", "1h05m".
func ShortDuration(ms int64) string {
	switch {
	case ms <= 0:
		return ""
	case ms < 60_000:
		return "<1m"
	}
	minutes := ms / 60_000
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

// Line is the round line, issue #142's format. Parts are joined by two
// spaces and omitted when empty; see the spec §2 for the exact rules.
func Line(u Usage) string {
	var parts []string
	var id []string
	for _, s := range []string{u.Harness, u.Provider, u.Model} {
		if s != "" {
			id = append(id, s)
		}
	}
	if len(id) > 0 {
		parts = append(parts, strings.Join(id, "/"))
	}
	if d := ShortDuration(u.DurationMS); d != "" {
		parts = append(parts, d)
	}
	if u.Samples > 0 {
		prompt := u.Tokens.In + u.Tokens.CacheRead + u.Tokens.CacheWrite
		in := "in " + ShortTokens(prompt)
		if prompt > 0 {
			in += fmt.Sprintf(" (cache %.0f%%)", u.Tokens.CacheRatio()*100)
		}
		parts = append(parts, in, "out "+ShortTokens(u.Tokens.Out))
	}
	money := Money(u.Cost)
	if u.Cost.Basis == Unknown && !u.Cost.Plan && u.Note != "" {
		money += " (" + u.Note + ")"
	}
	parts = append(parts, money)
	return strings.Join(parts, "  ")
}
```

Check the "plan lane" case: `in 100 (cache 0%)` -- prompt is 100 > 0, so
the parenthetical prints `0%`. That is the spec's rule (omit only when
the prompt is 0); the test pins it.

- [ ] **Step 4: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add docs/specs/2026-09-18-usage-surfaces-design.md docs/plans/2026-09-18-usage-surfaces.md internal/usage/
git commit -m "feat(usage): Money, ShortTokens, ShortDuration and the round Line (#142)"
```

---

### Task 2: `Spend`, `Sum`, `SpendLine`, `MoneyShort`

**Files:**
- Create: `internal/usage/spend.go`
- Test: `internal/usage/spend_test.go`

**Interfaces:**
- Produces:

```go
type Spend struct {
	Rounds    int     `json:"rounds"`
	Consults  int     `json:"consults"`
	Measured  float64 `json:"measured"`
	Estimated float64 `json:"estimated"`
	Plan      int     `json:"plan"`
	Unknown   int     `json:"unknown"`
	Tokens    Tokens  `json:"tokens"`
}
func Sum(us []Usage, isConsult []bool) Spend
func (s Spend) Add(o Spend) Spend
func SpendLine(s Spend) string
func MoneyShort(s Spend) string
```

- [ ] **Step 1: Write the failing tests.**

`internal/usage/spend_test.go`:

```go
package usage

import "testing"

func TestSumRoutesByBasis(t *testing.T) {
	us := []Usage{
		{Tokens: Tokens{In: 10}, Cost: Cost{USD: 1.00, Basis: Measured}},
		{Tokens: Tokens{In: 20}, Cost: Cost{USD: 0.25, Basis: Estimated}},
		{Tokens: Tokens{In: 30}, Cost: Cost{USD: 0.15, Basis: Estimated}},
		{Tokens: Tokens{In: 40}, Cost: Cost{USD: 0, Basis: Unknown}},
		{Tokens: Tokens{In: 50}, Cost: Cost{USD: 9.99, Basis: Measured, Plan: true}},
	}
	s := Sum(us, []bool{false, false, true, false, false})
	if s.Rounds != 4 || s.Consults != 1 {
		t.Errorf("rounds/consults = %d/%d, want 4/1", s.Rounds, s.Consults)
	}
	if s.Measured != 1.00 {
		t.Errorf("Measured = %v, want 1.00 (plan dollars excluded)", s.Measured)
	}
	if s.Estimated < 0.399 || s.Estimated > 0.401 {
		t.Errorf("Estimated = %v, want 0.40", s.Estimated)
	}
	if s.Plan != 1 || s.Unknown != 1 {
		t.Errorf("plan/unknown = %d/%d, want 1/1", s.Plan, s.Unknown)
	}
	if s.Tokens.In != 150 {
		t.Errorf("tokens must sum regardless of basis: %+v", s.Tokens)
	}
}

func TestSumPlanIsNotCash(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{USD: 5, Basis: Measured, Plan: true}}}, nil)
	if s.Measured != 0 || s.Estimated != 0 || s.Plan != 1 {
		t.Errorf("a plan round is a count, never dollars: %+v", s)
	}
}

func TestSumNilIsConsult(t *testing.T) {
	s := Sum([]Usage{{Cost: Cost{Basis: Unknown}}, {Cost: Cost{Basis: Unknown}}}, nil)
	if s.Rounds != 2 || s.Consults != 0 {
		t.Errorf("nil isConsult means every entry is a round: %+v", s)
	}
}

func TestSpendAdd(t *testing.T) {
	a := Spend{Rounds: 1, Measured: 1, Tokens: Tokens{In: 1}}
	b := Spend{Rounds: 2, Consults: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{Out: 2}}
	got := a.Add(b)
	want := Spend{Rounds: 3, Consults: 1, Measured: 1, Estimated: 2, Plan: 1, Unknown: 1, Tokens: Tokens{In: 1, Out: 2}}
	if got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestSpendLine(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Consults: 2, Measured: 1.23, Estimated: 0.40, Plan: 1, Unknown: 2}, "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown"},
		{Spend{Rounds: 1, Measured: 0.30}, "1 round · $0.30"},
		{Spend{Rounds: 3, Unknown: 3}, "3 rounds · 3 unknown"},
		{Spend{Consults: 1, Estimated: 0.02}, "0 rounds +1c · ~$0.02"},
		{Spend{}, "no rounds"},
		{Spend{Rounds: 1, Measured: 0.004}, "1 round · <$0.01"},
	}
	for _, c := range cases {
		if got := SpendLine(c.s); got != c.want {
			t.Errorf("SpendLine(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestMoneyShort(t *testing.T) {
	cases := []struct {
		s    Spend
		want string
	}{
		{Spend{Rounds: 4, Measured: 1.23, Estimated: 0.40, Unknown: 2}, "$1.23 · ~$0.40 · 2 unknown"},
		{Spend{Rounds: 2, Plan: 2}, "2 plan"},
		{Spend{Rounds: 2}, ""},
		{Spend{}, ""},
	}
	for _, c := range cases {
		if got := MoneyShort(c.s); got != c.want {
			t.Errorf("MoneyShort(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/usage/ -run 'TestSum|TestSpend|TestMoneyShort'`
Expected: build failure.

- [ ] **Step 3: Write `spend.go`.**

```go
package usage

import (
	"fmt"
	"strings"
)

// Spend is a binding's, or a group's, total. Measured and Estimated are
// separate sums so an estimate never hides inside a measurement; Plan and
// Unknown are round counts, never dollars.
type Spend struct {
	Rounds    int     `json:"rounds"`
	Consults  int     `json:"consults"`
	Measured  float64 `json:"measured"`
	Estimated float64 `json:"estimated"`
	Plan      int     `json:"plan"`
	Unknown   int     `json:"unknown"`
	Tokens    Tokens  `json:"tokens"`
}

// Sum folds usages. isConsult[i] marks entry i as a consult rather than a
// round; nil means every entry is a round. Dollars route by basis; a plan
// lane counts under Plan and its dollars are not cash.
func Sum(us []Usage, isConsult []bool) Spend {
	var s Spend
	for i, u := range us {
		if isConsult != nil && i < len(isConsult) && isConsult[i] {
			s.Consults++
		} else {
			s.Rounds++
		}
		s.Tokens = s.Tokens.Add(u.Tokens)
		switch {
		case u.Cost.Plan:
			s.Plan++
		case u.Cost.Basis == Measured:
			s.Measured += u.Cost.USD
		case u.Cost.Basis == Estimated:
			s.Estimated += u.Cost.USD
		default:
			s.Unknown++
		}
	}
	return s
}

// Add is the field-wise sum.
func (s Spend) Add(o Spend) Spend {
	return Spend{
		Rounds: s.Rounds + o.Rounds, Consults: s.Consults + o.Consults,
		Measured: s.Measured + o.Measured, Estimated: s.Estimated + o.Estimated,
		Plan: s.Plan + o.Plan, Unknown: s.Unknown + o.Unknown,
		Tokens: s.Tokens.Add(o.Tokens),
	}
}

func moneyParts(s Spend) []string {
	var p []string
	if s.Measured > 0 {
		p = append(p, Money(Cost{USD: s.Measured, Basis: Measured}))
	}
	if s.Estimated > 0 {
		p = append(p, Money(Cost{USD: s.Estimated, Basis: Estimated}))
	}
	if s.Plan > 0 {
		p = append(p, fmt.Sprintf("%d plan", s.Plan))
	}
	if s.Unknown > 0 {
		p = append(p, fmt.Sprintf("%d unknown", s.Unknown))
	}
	return p
}

// SpendLine: "4 rounds +2c · $1.23 · ~$0.40 · 1 plan · 2 unknown".
func SpendLine(s Spend) string {
	if s.Rounds == 0 && s.Consults == 0 {
		return "no rounds"
	}
	head := fmt.Sprintf("%d rounds", s.Rounds)
	if s.Rounds == 1 {
		head = "1 round"
	}
	if s.Consults > 0 {
		head += fmt.Sprintf(" +%dc", s.Consults)
	}
	return strings.Join(append([]string{head}, moneyParts(s)...), " · ")
}

// MoneyShort is SpendLine without the rounds part, for the rail card.
func MoneyShort(s Spend) string {
	return strings.Join(moneyParts(s), " · ")
}
```

- [ ] **Step 4: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 5: Mutation check.** In `Sum`, make the `u.Cost.Plan` case
also do `s.Measured += u.Cost.USD`. Run `go test ./internal/usage/ -run TestSumPlanIsNotCash`.
Expected: FAIL. Revert.

- [ ] **Step 6: Commit.**

```bash
git add internal/usage/
git commit -m "feat(usage): Spend, Sum, SpendLine and MoneyShort (#142)"
```

---

### Task 3: `relay.LogLine`, shared by `relay log` and the ui log tab

**Files:**
- Create: `internal/relay/logline.go`
- Modify: `cmd/relay/main.go` (`cmdLog`, the `fmt.Printf` loop),
  `internal/ui/fetch.go` (`fetchLog`, the `fmt.Fprintf` loop)
- Test: `internal/relay/logline_test.go`

**Interfaces:**
- Consumes: `usage.Line` (Task 1).
- Produces: `func LogLine(e store.LogEntry) string` -- no trailing newline.

- [ ] **Step 1: Write the failing test.**

`internal/relay/logline_test.go`:

```go
package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

func TestLogLineWithoutUsageIsTodaysFormat(t *testing.T) {
	ts := time.Date(2026, 9, 18, 14, 31, 7, 0, time.UTC)
	e := store.LogEntry{TS: ts, Round: 4, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/p/004-report.md", Note: "scraped"}
	want := ts.Local().Format("2006-01-02 15:04:05") + "  round 4   to_planner report    /p/004-report.md scraped"
	if got := LogLine(e); got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
	if strings.Contains(LogLine(e), "\n") {
		t.Error("no second line without usage")
	}
}

func TestLogLineWithUsageAddsSecondLine(t *testing.T) {
	e := store.LogEntry{TS: time.Now(), Round: 4, Direction: store.DirToPlanner, Kind: store.KindReport, Path: "/p/004-report.md",
		Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 60_000,
			Tokens: usage.Tokens{In: 100, Out: 10}, Cost: usage.Cost{USD: 0.5, Basis: usage.Measured}, Samples: 1}}
	got := LogLine(e)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %q", got)
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", 21)+"⎿ ") {
		t.Errorf("second line must be indented under the round column: %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], usage.Line(*e.Usage)) {
		t.Errorf("second line must end with usage.Line: %q", lines[1])
	}
}
```

- [ ] **Step 2: Run to verify it fails.**

Run: `go test ./internal/relay/ -run TestLogLine`
Expected: build failure.

- [ ] **Step 3: Write `logline.go`.**

```go
package relay

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// logLineIndent is the width of the timestamp column plus its two-space
// gap, so the usage line sits under the "round" column.
const logLineIndent = 21

// LogLine is the one text form of a log entry, shared by `relay log` and
// the ui's log tab. Line one is the format both have always printed; a
// second line, indented and marked ⎿ like the status log tail, carries the
// round's usage when the entry has one (#142).
func LogLine(e store.LogEntry) string {
	first := fmt.Sprintf("%s  round %-3d %-10s %-9s %s %s",
		e.TS.Local().Format("2006-01-02 15:04:05"), e.Round, e.Direction, e.Kind, e.Path, e.Note)
	if e.Usage == nil {
		return first
	}
	return first + "\n" + strings.Repeat(" ", logLineIndent) + "⎿ " + usage.Line(*e.Usage)
}
```

- [ ] **Step 4: Use it in both places.** In `cmd/relay/main.go` `cmdLog`,
replace the `fmt.Printf(...)` loop body with `fmt.Println(relay.LogLine(e))`.
In `internal/ui/fetch.go` `fetchLog`, replace the `fmt.Fprintf(&b, ...)`
with `b.WriteString(relay.LogLine(e)); b.WriteByte('\n')`. Delete the
now-unused format strings; `fmt` may still be imported elsewhere in each
file -- let `go vet`/the compiler tell you.

- [ ] **Step 5: Run.**

Run: `go build ./... && go test -race -count=1 ./internal/relay/ ./internal/ui/`
Expected: PASS. The ui's existing log-tab tests see the same first line.

- [ ] **Step 6: Commit.**

```bash
git add internal/relay/logline.go internal/relay/logline_test.go cmd/relay/main.go internal/ui/fetch.go
git commit -m "feat(relay): LogLine shared by relay log and the ui, with the round's usage (#142)"
```

---

### Task 4: `status` -- `LastUsage`, `Spend`, two text rows

**Files:**
- Modify: `internal/relay/status.go` (`BindingStatus` after `LastClose`;
  the scan near `row.LastClose = ...`; `RenderStatus` after the `last`
  row)
- Test: `internal/relay/status_test.go`

**Interfaces:**
- Consumes: `usage.Sum`, `usage.Line`, `usage.SpendLine` (Tasks 1-2).
- Produces on `BindingStatus`:

```go
LastUsage *usage.Usage `json:"last_usage,omitempty"`
Spend     *usage.Spend `json:"spend,omitempty"`
```

- [ ] **Step 1: Write the failing tests.** Append to `status_test.go`:

```go
func seedUsageLog(t *testing.T) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedClosedRound(t, "clean", 1)
	mk := func(usd float64, basis usage.Basis) *usage.Usage {
		return &usage.Usage{Harness: "agy", Provider: "test", Model: "m", DurationMS: 60_000,
			Tokens: usage.Tokens{In: 100, Out: 10}, Cost: usage.Cost{USD: usd, Basis: basis}, Samples: 1}
	}
	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "r1", Confirmed: true, Usage: mk(0.10, usage.Measured)},
		{Round: 1, Direction: store.DirToPlanner, Kind: store.KindFindings, Payload: "f1", Confirmed: true, Usage: mk(0.02, usage.Estimated)},
		{Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "r2", Confirmed: true, Usage: mk(0.30, usage.Measured)},
	}
	for _, e := range entries {
		if err := rt.Store.AppendLog(b.Name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return rt, b
}

func TestStatusLastUsageAndSpend(t *testing.T) {
	rt, _ := seedUsageLog(t)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.LastUsage == nil || got.LastUsage.Cost.USD != 0.30 {
		t.Fatalf("LastUsage = %+v, want the round-2 report's", got.LastUsage)
	}
	if got.Spend == nil {
		t.Fatal("Spend = nil")
	}
	if got.Spend.Rounds != 2 || got.Spend.Consults != 1 {
		t.Errorf("rounds/consults = %d/%d, want 2/1", got.Spend.Rounds, got.Spend.Consults)
	}
	if got.Spend.Measured < 0.399 || got.Spend.Measured > 0.401 || got.Spend.Estimated != 0.02 {
		t.Errorf("measured/estimated = %v/%v, want 0.40/0.02", got.Spend.Measured, got.Spend.Estimated)
	}
	text := RenderStatus(rep)
	if !strings.Contains(text, "  usage    "+usage.Line(*got.LastUsage)) {
		t.Errorf("text lacks the usage row:\n%s", text)
	}
	if !strings.Contains(text, "  spend    2 rounds +1c · $0.40 · ~$0.02") {
		t.Errorf("text lacks the spend row:\n%s", text)
	}
}

func TestStatusNoUsageNoRows(t *testing.T) {
	rt, _ := seedClosedRound(t, "clean", 1)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].LastUsage != nil || rep.Bindings[0].Spend != nil {
		t.Errorf("no usage entries: both must be nil, got %+v / %+v", rep.Bindings[0].LastUsage, rep.Bindings[0].Spend)
	}
	text := RenderStatus(rep)
	if strings.Contains(text, "  usage ") || strings.Contains(text, "  spend ") {
		t.Errorf("no rows without usage:\n%s", text)
	}
	raw, _ := json.Marshal(rep.Bindings[0])
	if strings.Contains(string(raw), "last_usage") || strings.Contains(string(raw), "spend") {
		t.Errorf("json must omit both when nil: %s", raw)
	}
}
```

(Add `encoding/json`, `strings` and the `usage` import if missing.)

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/relay/ -run 'TestStatusLastUsage|TestStatusNoUsage'`
Expected: build failure on the unknown fields.

- [ ] **Step 3: Add the fields and the scan.** In `BindingStatus`, after
`LastClose`:

```go
	// LastUsage is the newest report entry's usage (#142); nil when no
	// report entry carries one -- every round closed before #185, and
	// every round not yet closed. Spend sums every report and findings
	// entry that carries usage; nil when none does. Both omitted from JSON
	// when nil so a consumer that never learned them sees the document it
	// always did.
	LastUsage *usage.Usage `json:"last_usage,omitempty"`
	Spend     *usage.Spend `json:"spend,omitempty"`
```

After the `LastClose` scan loop:

```go
	var usages []usage.Usage
	var consults []bool
	for _, e := range entries {
		if e.Usage == nil || (e.Kind != store.KindReport && e.Kind != store.KindFindings) {
			continue
		}
		usages = append(usages, *e.Usage)
		consults = append(consults, e.Kind == store.KindFindings)
		if e.Kind == store.KindReport {
			u := *e.Usage
			row.LastUsage = &u
		}
	}
	if len(usages) > 0 {
		s := usage.Sum(usages, consults)
		row.Spend = &s
	}
```

In `RenderStatus`, after the `if b.Last != nil { ... }` block:

```go
		if b.LastUsage != nil {
			fmt.Fprintf(&sb, "  usage    %s\n", usage.Line(*b.LastUsage))
		}
		if b.Spend != nil {
			fmt.Fprintf(&sb, "  spend    %s\n", usage.SpendLine(*b.Spend))
		}
```

- [ ] **Step 4: Run.**

Run: `go test -race -count=1 ./internal/relay/`
Expected: PASS, including every existing status test.

- [ ] **Step 5: Commit.**

```bash
git add internal/relay/status.go internal/relay/status_test.go
git commit -m "feat(relay): status carries last_usage and spend, text prints both rows (#142)"
```

---

### Task 5: ui -- rail fact, split header, goldens

**Files:**
- Modify: `internal/ui/rail.go` (`facts`), `internal/ui/model.go`
  (`headerView`), `internal/ui/golden_test.go` (`allStatesRows`),
  `internal/ui/testdata/*.golden` (regenerated)
- Test: `internal/ui/rail_test.go`, `internal/ui/split_test.go`

**Interfaces:**
- Consumes: `BindingStatus.Spend` (Task 4), `usage.MoneyShort`,
  `usage.SpendLine` (Task 2).

- [ ] **Step 1: Write the failing tests.** Append to `rail_test.go`:

```go
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
```

Append to `split_test.go`:

```go
func TestHeaderShowsSelectedSpend(t *testing.T) {
	rows := threeRows()
	rows[0].Spend = &usage.Spend{Rounds: 3, Measured: 1.23, Unknown: 1}
	m := splitModel(t, 140, 40, rows...)
	h := stripANSI(m.headerView())
	if !strings.Contains(h, "spend 3 rounds · $1.23 · 1 unknown") {
		t.Errorf("header = %q", h)
	}
	if strings.Count(h, "\n") != headerRows-1 {
		t.Errorf("header must stay %d rows: %q", headerRows, h)
	}
}
```

(`threeRows()` and `splitModel` exist in `split_test.go`; `stripANSI` and
`sep` exist in the package. Confirm the selected binding after
`splitModel` is `rows[0]` -- if the helper selects differently, set the
spend on whichever row `m.detail.name` names.) Add the `usage` import.

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/ui/ -run 'TestFactsSpend|TestHeaderShowsSelectedSpend'`
Expected: FAIL (no fact, no header text).

- [ ] **Step 3: Implement.** In `rail.go` `facts`, after the `ForkedFrom`
fact:

```go
	if b.Spend != nil {
		if s := usage.MoneyShort(*b.Spend); s != "" {
			out = append(out, dimStyle.Render(s))
		}
	}
```

In `model.go` `headerView`, before the clock is appended to `right`:

```go
	if m.layout() == layoutSplit && m.detail.name != "" {
		for _, b := range m.report.Bindings {
			if b.Name == m.detail.name && b.Spend != nil {
				right = append(right, dimStyle.Render("spend "+usage.SpendLine(*b.Spend)))
				break
			}
		}
	}
```

- [ ] **Step 4: Goldens.** In `golden_test.go` `allStatesRows`, give the
`ledger` row `Spend: &usage.Spend{Rounds: 3, Measured: 1.23, Estimated: 0.40, Unknown: 1},`.
Regenerate and inspect:

```bash
go test ./internal/ui/ -run TestGoldenViews -update
git diff --stat internal/ui/testdata/
git diff internal/ui/testdata/ | grep '^[+-]' | grep -v '^+++\|^---' | head -40
```

Expected: only goldens that render the `ledger` card or its header
change, and the changed lines contain `$1.23 · ~$0.40 · 1 unknown` (card)
and/or `spend 3 rounds · $1.23 · ~$0.40 · 1 unknown` (header when
`ledger` is selected). If any other line changed, stop and say so.

- [ ] **Step 5: Run.**

Run: `go test -race -count=1 ./internal/ui/`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/ui/
git commit -m "feat(ui): spend on the rail card and the split header (#142)"
```

---

### Task 6: `store.ListArchives`, `store.ReadArchivedLog`

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/archive_test.go` (new)

**Interfaces:**
- Produces:

```go
type Archive struct {
	Name string
	At   time.Time
	Path string
}
func (s *Store) ListArchives() ([]Archive, error)
func (s *Store) ReadArchivedLog(path string) ([]LogEntry, error)
```

- [ ] **Step 1: Write the failing tests.**

`internal/store/archive_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// archiveFixture creates a binding dir with a log and tars it the way gc
// does, returning the archive path.
func archiveFixture(t *testing.T, s *Store, name, stamp string, entries []LogEntry) string {
	t.Helper()
	dir := s.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := s.appendLog(name, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(s.ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(s.ArchiveDir(), name+"-"+stamp+".tar.gz")
	if err := tarGzDir(dir, name, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestListArchivesParsesNameAndStamp(t *testing.T) {
	s := New(t.TempDir())
	if got, err := s.ListArchives(); err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v, %v", got, err)
	}
	archiveFixture(t, s, "webshop", "20260911-215319", nil)
	archiveFixture(t, s, "api-v2", "20260905-135037", nil)
	if err := os.WriteFile(filepath.Join(s.ArchiveDir(), "junk.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArchives()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("archives = %+v, want 2", got)
	}
	if got[0].Name != "api-v2" || !got[0].At.Equal(time.Date(2026, 9, 5, 13, 50, 37, 0, time.UTC)) {
		t.Errorf("oldest first: %+v", got[0])
	}
	if got[1].Name != "webshop" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestReadArchivedLog(t *testing.T) {
	s := New(t.TempDir())
	entries := []LogEntry{
		{TS: time.Unix(1, 0).UTC(), Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "p"},
		{TS: time.Unix(2, 0).UTC(), Round: 1, Direction: DirToPlanner, Kind: KindDiff},
	}
	path := archiveFixture(t, s, "webshop", "20260911-215319", entries)
	got, err := s.ReadArchivedLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != KindReport || got[1].Round != 1 {
		t.Errorf("got %+v", got)
	}
	empty := archiveFixture(t, s, "nolog", "20260911-215320", nil)
	if got, err := s.ReadArchivedLog(empty); err != nil || got != nil {
		t.Errorf("archive without a log: got %v, %v; want nil, nil", got, err)
	}
	if _, err := s.ReadArchivedLog(filepath.Join(s.ArchiveDir(), "missing.tar.gz")); err == nil {
		t.Error("missing archive must error")
	}
}
```

Note `appendLog` is the unexported, lock-free writer (`AppendLog` takes
the lock; either works from inside the package -- if `appendLog` has a
different name, use the one `Tx.AppendLog` calls). If a binding dir
without `log.jsonl` makes `tarGzDir` fail, create an empty `bind.json`
instead of nothing; the test only needs "an archive with no log member".

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/store/ -run 'TestListArchives|TestReadArchivedLog'`
Expected: build failure.

- [ ] **Step 3: Implement** in `store.go`, near `ArchiveDir`:

```go
// Archive is one gc'd binding under ArchiveDir, named <name>-<stamp>.tar.gz.
type Archive struct {
	Name string
	At   time.Time
	Path string
}

// archiveStampLayout is the stamp archive() writes into the file name.
const archiveStampLayout = "20060102-150405"

// ListArchives returns every archive, oldest first. Files that do not
// match the name pattern are ignored.
func (s *Store) ListArchives() ([]Archive, error) {
	names, err := filepath.Glob(filepath.Join(s.ArchiveDir(), "*.tar.gz"))
	if err != nil {
		return nil, err
	}
	var out []Archive
	for _, p := range names {
		base := strings.TrimSuffix(filepath.Base(p), ".tar.gz")
		// The stamp is the last 15 bytes: YYYYMMDD-HHMMSS, preceded by '-'.
		if len(base) < len(archiveStampLayout)+2 {
			continue
		}
		cut := len(base) - len(archiveStampLayout)
		if base[cut-1] != '-' {
			continue
		}
		at, err := time.Parse(archiveStampLayout, base[cut:])
		if err != nil {
			continue
		}
		out = append(out, Archive{Name: base[:cut-1], At: at.UTC(), Path: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// ReadArchivedLog returns the entries of the <name>/log.jsonl member of an
// archive, nil when the archive has no such member. It reads the tarball
// only as far as that member.
func (s *Store) ReadArchivedLog(path string) ([]LogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if filepath.Base(h.Name) != "log.jsonl" || strings.Count(strings.Trim(h.Name, "/"), "/") != 1 {
			continue
		}
		return decodeLog(tr)
	}
}
```

`decodeLog(r io.Reader) ([]LogEntry, error)` is the line decoder
`readLog` uses; if `readLog` opens the file and scans inline, factor its
scanning loop (the `bufio.Scanner`, the `maxLogEntries` guard, the
`json.Unmarshal` per line) into `decodeLog` and call it from both. Add
imports `compress/gzip`, `io`, `sort`, `strings` as needed
(`archive/tar` is already imported).

- [ ] **Step 4: Run.**

Run: `go test -race -count=1 ./internal/store/`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/store/
git commit -m "feat(store): list archives and read an archived log (#142)"
```

---

### Task 7: `relay tab`

**Files:**
- Create: `internal/relay/tab.go`, `cmd/relay/tab.go`
- Modify: `cmd/relay/main.go` (dispatch `case "tab":` next to `"log"`;
  the verb list in the help text after the `log` line), `README.md`
  (`### Round usage` gains the `tab` paragraph; the command surface
  section gains the verb)
- Test: `internal/relay/tab_test.go`

**Interfaces:**
- Consumes: `usage.Sum`, `usage.Spend`, `usage.Money`,
  `usage.ShortTokens` (Tasks 1-2); `Store.List`, `Store.ReadLog`,
  `Store.ListArchives`, `Store.ReadArchivedLog` (Task 6).
- Produces:

```go
type TabEntry struct {
	Binding string
	Entry   store.LogEntry
}
type TabRow struct {
	Group string      `json:"group"`
	Spend usage.Spend `json:"spend"`
}
type TabReport struct {
	Since *time.Time  `json:"since"`
	By    string      `json:"by"`
	Rows  []TabRow    `json:"rows"`
	Total usage.Spend `json:"total"`
}
var ErrBadSince = errors.New("--since wants 24h, 7d or YYYY-MM-DD")
var ErrBadBy = errors.New("--by wants binding, model or provider")
func ParseSince(s string, now time.Time) (time.Time, error)
func TabRows(entries []TabEntry, by string, since time.Time) ([]TabRow, usage.Spend, error)
func RenderTab(r TabReport) string
```

- [ ] **Step 1: Write the failing tests.**

`internal/relay/tab_test.go`:

```go
package relay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

var tabNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func tabEntry(binding string, age time.Duration, kind store.Kind, provider, model string, usd float64, basis usage.Basis) TabEntry {
	return TabEntry{Binding: binding, Entry: store.LogEntry{
		TS: tabNow.Add(-age), Round: 1, Direction: store.DirToPlanner, Kind: kind,
		Usage: &usage.Usage{Harness: "h", Provider: provider, Model: model, Tokens: usage.Tokens{In: 1000, CacheRead: 3000, Out: 100},
			Cost: usage.Cost{USD: usd, Basis: basis}, Samples: 1},
	}}
}

func tabFixture() []TabEntry {
	return []TabEntry{
		tabEntry("api", 1*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.50, usage.Measured),
		tabEntry("api", 2*time.Hour, store.KindFindings, "anthropic", "claude-opus-5", 0.10, usage.Estimated),
		tabEntry("api", 10*24*time.Hour, store.KindReport, "anthropic", "claude-sonnet-5", 0.20, usage.Measured),
		tabEntry("web", 3*time.Hour, store.KindReport, "google", "gemini-3.8-flash-high", 0, usage.Unknown),
		{Binding: "web", Entry: store.LogEntry{TS: tabNow, Kind: store.KindReport}}, // no usage: ignored
		{Binding: "web", Entry: store.LogEntry{TS: tabNow, Kind: store.KindDiff, Usage: &usage.Usage{}}}, // wrong kind: ignored
	}
}

func TestParseSince(t *testing.T) {
	if got, err := ParseSince("", tabNow); err != nil || !got.IsZero() {
		t.Errorf("empty: %v, %v", got, err)
	}
	if got, _ := ParseSince("24h", tabNow); !got.Equal(tabNow.Add(-24 * time.Hour)) {
		t.Errorf("24h = %v", got)
	}
	if got, _ := ParseSince("7d", tabNow); !got.Equal(tabNow.Add(-7 * 24 * time.Hour)) {
		t.Errorf("7d = %v", got)
	}
	if got, _ := ParseSince("2026-09-01", tabNow); !got.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", got)
	}
	for _, bad := range []string{"7", "7w", "yesterday", "2026-9-1"} {
		if _, err := ParseSince(bad, tabNow); !errors.Is(err, ErrBadSince) {
			t.Errorf("%q: err = %v, want ErrBadSince", bad, err)
		}
	}
}

func TestTabRowsByBinding(t *testing.T) {
	rows, total, err := TabRows(tabFixture(), "binding", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Group != "api" || rows[1].Group != "web" {
		t.Fatalf("rows = %+v", rows)
	}
	api := rows[0].Spend
	if api.Rounds != 2 || api.Consults != 1 || api.Measured < 0.699 || api.Measured > 0.701 || api.Estimated != 0.10 {
		t.Errorf("api = %+v", api)
	}
	if rows[1].Spend.Unknown != 1 || rows[1].Spend.Rounds != 1 {
		t.Errorf("web = %+v", rows[1].Spend)
	}
	if total.Rounds != 3 || total.Consults != 1 || total.Unknown != 1 {
		t.Errorf("total = %+v", total)
	}
}

func TestTabRowsSince(t *testing.T) {
	rows, total, _ := TabRows(tabFixture(), "binding", tabNow.Add(-7*24*time.Hour))
	if rows[0].Spend.Rounds != 1 || rows[0].Spend.Measured != 0.50 {
		t.Errorf("the 10-day-old round must be cut: %+v", rows[0].Spend)
	}
	if total.Rounds != 2 {
		t.Errorf("total = %+v", total)
	}
}

func TestTabRowsByModelAndProvider(t *testing.T) {
	rows, _, _ := TabRows(tabFixture(), "model", time.Time{})
	want := []string{"anthropic/claude-opus-5", "anthropic/claude-sonnet-5", "google/gemini-3.8-flash-high"}
	for i, w := range want {
		if i >= len(rows) || rows[i].Group != w {
			t.Fatalf("model rows = %+v, want %v", rows, want)
		}
	}
	rows, _, _ = TabRows(tabFixture(), "provider", time.Time{})
	if len(rows) != 2 || rows[0].Group != "anthropic" || rows[1].Group != "google" {
		t.Errorf("provider rows = %+v", rows)
	}
	if _, _, err := TabRows(nil, "day", time.Time{}); !errors.Is(err, ErrBadBy) {
		t.Errorf("bad --by: %v", err)
	}
}

func TestTabRowsUnknownModelGroup(t *testing.T) {
	e := TabEntry{Binding: "x", Entry: store.LogEntry{TS: tabNow, Kind: store.KindReport, Usage: &usage.Usage{Cost: usage.Cost{Basis: usage.Unknown}}}}
	rows, _, _ := TabRows([]TabEntry{e}, "model", time.Time{})
	if len(rows) != 1 || rows[0].Group != "unknown" {
		t.Errorf("empty provider/model must group as unknown: %+v", rows)
	}
}

func TestRenderTab(t *testing.T) {
	rows, total, _ := TabRows(tabFixture(), "binding", time.Time{})
	out := RenderTab(TabReport{By: "binding", Rows: rows, Total: total})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 2 rows + total, got:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "group") || !strings.Contains(lines[0], "measured") || !strings.Contains(lines[0], "unknown") {
		t.Errorf("header = %q", lines[0])
	}
	api, web, tot := lines[1], lines[2], lines[3]
	if !strings.Contains(api, "$0.70") || !strings.Contains(api, "~$0.10") {
		t.Errorf("api row = %q", api)
	}
	if strings.Contains(web, "$") {
		t.Errorf("an all-unknown group must show no dollars: %q", web)
	}
	if !strings.HasPrefix(tot, "total") || !strings.Contains(tot, "$0.70") {
		t.Errorf("total row = %q", tot)
	}
	if !strings.Contains(api, "12k") { // 3 entries × 4000 prompt tokens
		t.Errorf("api in-tokens = %q", api)
	}
	empty := RenderTab(TabReport{By: "binding"})
	if !strings.Contains(empty, "no rounds with usage") {
		t.Errorf("empty = %q", empty)
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/relay/ -run 'TestParseSince|TestTabRows|TestRenderTab'`
Expected: build failure.

- [ ] **Step 3: Write `internal/relay/tab.go`.**

```go
package relay

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// TabEntry is one log entry with the binding it came from; cmd/relay
// collects them from live logs and archives, TabRows sums them.
type TabEntry struct {
	Binding string
	Entry   store.LogEntry
}

// TabRow is one group's total.
type TabRow struct {
	Group string      `json:"group"`
	Spend usage.Spend `json:"spend"`
}

// TabReport is what `relay tab --json` prints.
type TabReport struct {
	Since *time.Time  `json:"since"`
	By    string      `json:"by"`
	Rows  []TabRow    `json:"rows"`
	Total usage.Spend `json:"total"`
}

var (
	ErrBadSince = errors.New("--since wants 24h, 7d or YYYY-MM-DD")
	ErrBadBy    = errors.New("--by wants binding, model or provider")
)

// ParseSince turns "" (zero: no cut), "24h", "7d" or "2026-09-01" into
// the instant before which entries are ignored.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if len(s) >= 2 {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err == nil && n > 0 {
			switch s[len(s)-1] {
			case 'h':
				return now.Add(-time.Duration(n) * time.Hour), nil
			case 'd':
				return now.Add(-time.Duration(n) * 24 * time.Hour), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("%q: %w", s, ErrBadSince)
}

func tabKey(e TabEntry, by string) string {
	u := e.Entry.Usage
	switch by {
	case "binding":
		return e.Binding
	case "provider":
		if u.Provider == "" {
			return "unknown"
		}
		return u.Provider
	default: // model
		if u.Provider == "" && u.Model == "" {
			return "unknown"
		}
		return strings.Trim(u.Provider+"/"+u.Model, "/")
	}
}

// TabRows groups report and findings entries that carry usage, from since
// on (zero: all), by "binding", "model" or "provider", and returns the
// rows sorted by group and the grand total.
func TabRows(entries []TabEntry, by string, since time.Time) ([]TabRow, usage.Spend, error) {
	switch by {
	case "binding", "model", "provider":
	default:
		return nil, usage.Spend{}, fmt.Errorf("%q: %w", by, ErrBadBy)
	}
	groups := map[string]usage.Spend{}
	var total usage.Spend
	for _, e := range entries {
		u := e.Entry.Usage
		if u == nil || (e.Entry.Kind != store.KindReport && e.Entry.Kind != store.KindFindings) {
			continue
		}
		if !since.IsZero() && e.Entry.TS.Before(since) {
			continue
		}
		one := usage.Sum([]usage.Usage{*u}, []bool{e.Entry.Kind == store.KindFindings})
		k := tabKey(e, by)
		groups[k] = groups[k].Add(one)
		total = total.Add(one)
	}
	rows := make([]TabRow, 0, len(groups))
	for k, s := range groups {
		rows = append(rows, TabRow{Group: k, Spend: s})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Group < rows[j].Group })
	return rows, total, nil
}

// RenderTab prints the table. Empty cells stay empty: no "$0.00" for a
// group with nothing measured, no "0" for zero plan or unknown rounds.
func RenderTab(r TabReport) string {
	if len(r.Rows) == 0 {
		return "no rounds with usage\n"
	}
	width := 5
	for _, row := range r.Rows {
		if len(row.Group) > width {
			width = len(row.Group)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*s  %6s  %7s  %5s  %7s  %9s  %9s  %4s  %7s\n", width, "group", "rounds", "in", "cache", "out", "measured", "estimated", "plan", "unknown")
	line := func(group string, s usage.Spend) {
		rounds := strconv.Itoa(s.Rounds)
		if s.Consults > 0 {
			rounds += fmt.Sprintf("+%dc", s.Consults)
		}
		prompt := s.Tokens.In + s.Tokens.CacheRead + s.Tokens.CacheWrite
		cache := ""
		if prompt > 0 {
			cache = fmt.Sprintf("%.0f%%", s.Tokens.CacheRatio()*100)
		}
		cell := func(n int) string {
			if n == 0 {
				return ""
			}
			return strconv.Itoa(n)
		}
		money := func(usd float64, basis usage.Basis) string {
			if usd == 0 {
				return ""
			}
			return usage.Money(usage.Cost{USD: usd, Basis: basis})
		}
		fmt.Fprintf(&sb, "%-*s  %6s  %7s  %5s  %7s  %9s  %9s  %4s  %7s\n", width, group, rounds,
			usage.ShortTokens(prompt), cache, usage.ShortTokens(s.Tokens.Out),
			money(s.Measured, usage.Measured), money(s.Estimated, usage.Estimated), cell(s.Plan), cell(s.Unknown))
	}
	for _, row := range r.Rows {
		line(row.Group, row.Spend)
	}
	line("total", r.Total)
	return sb.String()
}
```

- [ ] **Step 4: Run.**

Run: `go test -race -count=1 ./internal/relay/ -run 'TestParseSince|TestTabRows|TestRenderTab'`
Expected: PASS. If `TestRenderTab`'s `12k` check fails because the
fixture's prompt sum differs, recount from the fixture (3 api entries ×
(1000 + 3000)) rather than changing the assertion.

- [ ] **Step 5: Write `cmd/relay/tab.go`.**

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// cmdTab sums recorded usage across bindings, archived ones included
// (#142). It is freeze exception #2 under #114; see the surfaces spec.
func cmdTab(args []string) error {
	fs := flag.NewFlagSet("tab", flag.ContinueOnError)
	since := fs.String("since", "", "only rounds closed after this: 24h, 7d, or YYYY-MM-DD (default: all)")
	by := fs.String("by", "binding", "group rows by binding, model or provider")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relay tab [--since 7d] [--by binding|model|provider] [--json]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("usage: relay tab [--since 7d] [--by binding|model|provider] [--json]")
	}
	now := time.Now().UTC()
	cut, err := relay.ParseSince(*since, now)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	var entries []relay.TabEntry
	live, err := rt.Store.List()
	if err != nil {
		return err
	}
	for _, b := range live {
		log, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", b.Name, err)
		}
		for _, e := range log {
			entries = append(entries, relay.TabEntry{Binding: b.Name, Entry: e})
		}
	}
	archives, err := rt.Store.ListArchives()
	if err != nil {
		return err
	}
	for _, a := range archives {
		if !cut.IsZero() && a.At.Before(cut) {
			continue // every entry in it predates the archive itself
		}
		log, err := rt.Store.ReadArchivedLog(a.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relay tab: skip %s: %v\n", a.Path, err)
			continue
		}
		for _, e := range log {
			entries = append(entries, relay.TabEntry{Binding: a.Name, Entry: e})
		}
	}

	rows, total, err := relay.TabRows(entries, *by, cut)
	if err != nil {
		return err
	}
	rep := relay.TabReport{By: *by, Rows: rows, Total: total}
	if !cut.IsZero() {
		rep.Since = &cut
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(relay.RenderTab(rep))
	return nil
}
```

In `cmd/relay/main.go`: add `case "tab": return cmdTab(args[1:])` next to
`case "log":`, and after the `log` line in the help text:

```
  tab       tokens and cost across bindings, archived ones included [--since 7d] [--by binding|model|provider] [--json]
```

- [ ] **Step 6: README.** In the command surface section, add `tab`
after `log` with the same one-liner. In `### Round usage`, replace the
final paragraph ("Nothing prints these yet…") with:

````markdown
Where you see it: `relay log` prints the round line under each report
(`⎿ claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41`);
`relay status` adds a `usage` row (newest round) and a `spend` row
(the binding's total: `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown`), both
on `--json` as `last_usage` and `spend`; `relay ui` shows the total on
the card and in the header. Across bindings:

```
relay tab [--since 7d|24h|2026-09-01] [--by binding|model|provider] [--json]
```

sums every round relay has recorded, including bindings `gc` has
archived, one row per group and a total. Measured and estimated dollars
never share a column; `plan` and `unknown` are counts of rounds. `tab`
is the second exception to the #114 verb freeze, taken because its
sums exist regardless (they are on `status --json`) and a cross-binding
view has no other home.
````

- [ ] **Step 7: Full check.**

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
go build -o /tmp/relay-tab ./cmd/relay && /tmp/relay-tab tab --help 2>&1 | head -3
```

Expected: clean; the help text prints. Do not run `tab` itself against
the real state directory.

- [ ] **Step 8: Commit.**

```bash
git add internal/relay/tab.go internal/relay/tab_test.go cmd/relay/tab.go cmd/relay/main.go README.md
git commit -m "feat(relay): relay tab sums usage across bindings and archives (#142)"
```

---

## Report

End your report with:

- the output of the four check commands (verbatim last lines);
- `git log --oneline main..HEAD` (seven commits expected);
- `git diff --stat main..HEAD`;
- the golden diff summary from Task 5 Step 4;
- anything you stopped on.

## After merge (not for the builder)

By the human: `make install`, restart the daemon, `relay status`
(`usage`/`spend` rows on any binding that closed a round since #185),
`relay log <name>`, `relay tab` and `relay tab --by model --since 7d`
against the real state directory, `relay ui`. Then close #142.
