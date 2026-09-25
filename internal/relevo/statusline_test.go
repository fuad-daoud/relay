package relevo

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestAgeText(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h 0m"},
		{27*time.Hour + 4*time.Minute + 30*time.Second, "27h 4m"},
		{-5 * time.Second, "0s"},
	}

	for _, tt := range tests {
		got := AgeText(tt.input)
		if got != tt.want {
			t.Errorf("AgeText(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripSGR(s string) string {
	return sgrRe.ReplaceAllString(s, "")
}

func width(s string) int {
	return utf8.RuneCountInString(stripSGR(s))
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func TestRenderStatusLineEmpty(t *testing.T) {
	if got := RenderStatusLine(Report{}, baseTime, 80); got != "" {
		t.Errorf("RenderStatusLine(Report{}, baseTime, 80) = %q, want empty", got)
	}
}

// TestRenderPlannerLine is #386's statusline surface: the first line names the
// planner, an empty name renders nothing, and a narrow terminal cuts the
// visible text to the column budget.
func TestRenderPlannerLine(t *testing.T) {
	got := RenderPlannerLine("architect-14", 80)
	if !strings.Contains(got, "planner architect-14") {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want it to contain %q", "architect-14", got, "planner architect-14")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want it to end in a newline", "architect-14", got)
	}

	if got := RenderPlannerLine("", 80); got != "" {
		t.Errorf("RenderPlannerLine(%q, 80) = %q, want empty", "", got)
	}

	narrow := RenderPlannerLine("architect-14", 10)
	visible := strings.TrimSuffix(stripSGR(narrow), "\n")
	if w := utf8.RuneCountInString(visible); w > 10 {
		t.Errorf("RenderPlannerLine(%q, 10) visible text is %d runes, want at most 10: %q", "architect-14", w, visible)
	}
}

func TestRenderStatusLineWaitingFallthrough(t *testing.T) {
	now := baseTime
	tests := []struct {
		name        string
		binding     BindingStatus
		expectMid   string
		expectRight string
		noSeparator bool
	}{
		{
			name: "report note",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				LastPayload:      &LastEvent{Kind: store.KindReport, Note: "unmarked"},
			},
			expectMid: "report in (unmarked)",
		},
		{
			name: "question",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				LastPayload:      &LastEvent{Kind: store.KindQuestion},
			},
			expectMid: "question in",
		},
		{
			name: "answer",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				LastPayload:      &LastEvent{Kind: store.KindAnswer},
			},
			expectMid: "answered",
		},
		{
			name: "last nil",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				LastPayload:      nil,
			},
			expectMid:   "no plan yet",
			expectRight: "--",
		},
		{
			name: "empty candidate",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "",
				LastPayload:      &LastEvent{Kind: store.KindPlan},
			},
			expectMid:   "r1 · plan sent",
			noSeparator: true,
		},
		{
			name: "candidate with no slash",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				LastPayload:      &LastEvent{Kind: store.KindPlan},
			},
			expectMid: "r1 · agy · plan sent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := Report{Bindings: []BindingStatus{tt.binding}}
			out := RenderStatusLine(rep, now, 80)
			lines := splitLines(out)
			if len(lines) != 1 {
				t.Fatalf("got %d lines, want 1", len(lines))
			}
			plain := stripSGR(lines[0])
			if !strings.Contains(plain, tt.expectMid) {
				t.Errorf("line %q does not contain mid %q", plain, tt.expectMid)
			}
			if tt.expectRight != "" && !strings.Contains(plain, tt.expectRight) {
				t.Errorf("line %q does not contain right %q", plain, tt.expectRight)
			}
			if tt.noSeparator && strings.Contains(plain, "·  ·") {
				t.Errorf("line %q contains empty separator", plain)
			}
		})
	}
}

// TestRenderStatusLineIgnoresBookkeepingLast pins that the statusline reads
// LastPayload, not relevo's own bookkeeping entries (Last): a binding whose
// most recent log entry is a drift note must still show the plan, and the
// age of that plan, not the age of the drift note.
func TestRenderStatusLineIgnoresBookkeepingLast(t *testing.T) {
	now := baseTime
	rep := Report{Bindings: []BindingStatus{
		{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-12 * time.Minute),
			Last:             &LastEvent{Kind: store.KindDrift, TS: now.Add(-1 * time.Second)},
			LastPayload:      &LastEvent{Kind: store.KindPlan, TS: now.Add(-12 * time.Minute)},
		},
	}}

	out := RenderStatusLine(rep, now, 80)
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	plain := stripSGR(lines[0])
	if !strings.Contains(plain, "plan sent") {
		t.Errorf("line %q does not contain %q", plain, "plan sent")
	}
	if strings.Contains(plain, "drift") {
		t.Errorf("line %q must not mention drift: %q", plain, plain)
	}
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("line %q does not have right cell ending in %q", plain, " 12m")
	}
	if strings.Contains(plain, "ACTIVE") {
		t.Errorf("line %q must not contain ACTIVE", plain)
	}
}

// TestRenderStatusLineLiveSegment pins the trailing live segment (#234):
// a row whose round is running shows the live figure last in the middle
// cell, before the right cell's clock and state.
func TestRenderStatusLineLiveSegment(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "plan sent · 41k tok") {
		t.Errorf("no live tokens segment: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
	if strings.Contains(plain, "live") {
		t.Errorf("must not contain 'live': %q", plain)
	}
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("the right cell must survive: %q", plain)
	}
}

// TestRenderStatusLineClosedRoundTokens pins the closed-round segment: a row
// with a closed round shows that round's tokens.
func TestRenderStatusLineClosedRoundTokens(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            3,
		Display:          "NEEDS YOU",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		RoundEnd:         baseTime.Add(-2 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-2 * time.Minute), Kind: store.KindReport, Direction: store.DirToPlanner},
		RoundUsage:       &usage.Usage{Tokens: usage.Tokens{In: 2_100_000}},
		Spend:            &usage.Spend{Rounds: 2, Measured: 1.51, Tokens: usage.Tokens{In: 9_000_000}},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "· 2.1M tok") {
		t.Errorf("no round tokens segment: %q", plain)
	}
	if strings.Contains(plain, "9.0M") || strings.Contains(plain, "9M") {
		t.Errorf("must not contain spend tokens: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
}

func TestRenderStatusLineLiveWinsOverSpend(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            4,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		Spend:            &usage.Spend{Rounds: 3, Measured: 1.51, Tokens: usage.Tokens{In: 2_100_000}},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "41k tok") {
		t.Errorf("the live figure must show: %q", plain)
	}
	if strings.Contains(plain, "2.1M") {
		t.Errorf("spend must yield to the live figure, never share a line: %q", plain)
	}
	if strings.Contains(plain, "$") {
		t.Errorf("must not contain dollar figure: %q", plain)
	}
}

// TestRenderStatusLineNarrowDropsUsageFirst pins the placement: the usage
// segment is the last thing in mid, so truncation drops it before the
// waiting verb, and the right cell always survives.
func TestRenderStatusLineNarrowDropsUsageFirst(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		RoundStart:       baseTime.Add(-12 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 40))[0])
	if !strings.HasSuffix(plain, " 12m") {
		t.Errorf("12m must survive the narrow row: %q", plain)
	}
	if strings.Contains(plain, "tok") {
		t.Errorf("the usage segment must be the part truncated: %q", plain)
	}
}

func TestStatusLineWidth(t *testing.T) {
	tests := []struct {
		columns  int
		override string
		want     int
	}{
		{146, "", 142},
		{146, "0", 146},
		{146, "10", 136},
		{146, "x", 142},
		{146, "-1", 142},
		{0, "", 0},
		{-5, "0", 0},
		{3, "", 1},
	}

	for _, tt := range tests {
		got := StatusLineWidth(tt.columns, tt.override)
		if got != tt.want {
			t.Errorf("StatusLineWidth(%d, %q) = %d, want %d", tt.columns, tt.override, got, tt.want)
		}
	}
}

func TestShouldDrainStdin(t *testing.T) {
	tests := []struct {
		mode os.FileMode
		want bool
	}{
		{os.ModeCharDevice, false},
		{os.FileMode(0), true},
		{os.ModeNamedPipe, true},
		{os.ModeCharDevice | os.ModeDevice, false},
	}

	for _, tt := range tests {
		got := ShouldDrainStdin(tt.mode)
		if got != tt.want {
			t.Errorf("ShouldDrainStdin(%v) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestWaitingReportOutcome(t *testing.T) {
	t.Run("outcome halted", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Outcome: "halted",
			},
		}
		if got := waiting(b); got != "report in · halted" {
			t.Errorf("waiting = %q, want 'report in · halted'", got)
		}
	})

	t.Run("outcome halted with note", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Note:    "unmarked",
				Outcome: "halted",
			},
		}
		if got := waiting(b); got != "report in (unmarked) · halted" {
			t.Errorf("waiting = %q, want 'report in (unmarked) · halted'", got)
		}
	})

	t.Run("outcome done quiet", func(t *testing.T) {
		b := BindingStatus{
			LastPayload: &LastEvent{
				Kind:    store.KindReport,
				Outcome: "done",
			},
		}
		if got := waiting(b); got != "report in" {
			t.Errorf("waiting = %q, want 'report in'", got)
		}
	})
}

func statuslineFixture(now time.Time) Report {
	return Report{
		Bindings: []BindingStatus{
			{
				Name:             "api",
				Round:            3,
				Display:          "ACTIVE",
				BuilderCandidate: "agy/google/gemini-3.8-flash-high",
				RoundStart:       now.Add(-12 * time.Minute),
				LastPayload: &LastEvent{
					TS:        now.Add(-12 * time.Minute),
					Kind:      store.KindPlan,
					Direction: store.DirToBuilder,
				},
			},
			{
				Name:             "client",
				Round:            1,
				Display:          "NEEDS YOU",
				BuilderCandidate: "opencode/openrouter/z-ai/glm-5.3-flash",
				RoundStart:       now.Add(-4 * time.Minute),
				LastPayload: &LastEvent{
					TS:   now.Add(-4 * time.Minute),
					Kind: store.KindPlan,
				},
			},
			{
				Name:             "docs",
				Round:            2,
				Display:          "PAUSED",
				BuilderCandidate: "agy/google/gemini-3.8-flash-high",
				RoundStart:       now.Add(-5 * time.Minute),
				RoundEnd:         now.Add(-5*time.Minute + 23*time.Second),
				LastPayload: &LastEvent{
					TS:        now.Add(-23 * time.Second),
					Kind:      store.KindReport,
					Direction: store.DirToPlanner,
				},
			},
		},
	}
}

func TestRenderStatusLineAt80(t *testing.T) {
	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := width(line); w != 80 {
			t.Errorf("line %d width = %d, want 80; line = %q", i, w, stripSGR(line))
		}
	}

	plain0 := stripSGR(lines[0])
	if !strings.HasPrefix(plain0, "○ api     r3 · agy · plan sent") {
		t.Errorf("line 0 prefix mismatch: %q", plain0)
	}
	if !strings.HasSuffix(plain0, " 12m") {
		t.Errorf("line 0 suffix mismatch: %q", plain0)
	}
	if strings.Contains(plain0, "ACTIVE") {
		t.Errorf("line 0 must not contain ACTIVE: %q", plain0)
	}

	plain1 := stripSGR(lines[1])
	if !strings.HasPrefix(plain1, "● client  r1 · opencode · plan sent") {
		t.Errorf("line 1 prefix mismatch: %q", plain1)
	}
	if !strings.HasSuffix(plain1, " 4m · NEEDS YOU") {
		t.Errorf("line 1 suffix mismatch: %q", plain1)
	}

	plain2 := stripSGR(lines[2])
	if !strings.HasPrefix(plain2, "○ docs    r2 · agy · report in") {
		t.Errorf("line 2 prefix mismatch: %q", plain2)
	}
	if !strings.HasSuffix(plain2, " 23s · PAUSED") {
		t.Errorf("line 2 suffix mismatch: %q", plain2)
	}
}

func TestRenderStatusLineTruncatesAt40(t *testing.T) {
	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 40)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := width(line); w != 40 {
			t.Errorf("line %d width = %d, want 40; line = %q", i, w, stripSGR(line))
		}
	}

	plain2 := stripSGR(lines[2])
	if !strings.Contains(plain2, "…") {
		t.Errorf("line 2 expected to contain '…': %q", plain2)
	}

	suffixes := []string{" 12m", " 4m · NEEDS YOU", " 23s · PAUSED"}
	for i, line := range lines {
		plain := stripSGR(line)
		if !strings.HasSuffix(plain, suffixes[i]) {
			t.Errorf("line %d suffix mismatch: %q, want suffix %q", i, plain, suffixes[i])
		}
	}
}

func TestRenderStatusLineZeroColumnsIs80(t *testing.T) {
	out0 := RenderStatusLine(statuslineFixture(baseTime), baseTime, 0)
	out80 := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	if out0 != out80 {
		t.Errorf("output for columns 0 does not match columns 80:\nout0:\n%s\nout80:\n%s", out0, out80)
	}
}

func TestRenderStatusLineUnpaddedWhenTooNarrow(t *testing.T) {
	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 20)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	plain0 := stripSGR(lines[0])
	want := "○ api  r3 · agy · plan sent · 12m"
	if plain0 != want {
		t.Errorf("line 0 = %q, want %q", plain0, want)
	}
}

func TestRenderStatusLineColours(t *testing.T) {
	out := RenderStatusLine(statuslineFixture(baseTime), baseTime, 80)
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	if !strings.Contains(lines[0], "\x1b[38;5;245m○\x1b[0m") {
		t.Errorf("line 0 missing dim dot: %q", lines[0])
	}
	if strings.Contains(lines[0], "\x1b[38;5;42m") {
		t.Errorf("line 0 must not contain active display colour: %q", lines[0])
	}
	if strings.Contains(lines[0], "ACTIVE") {
		t.Errorf("line 0 must not contain ACTIVE: %q", lines[0])
	}

	if !strings.Contains(lines[1], "\x1b[1;38;5;214m●\x1b[0m") {
		t.Errorf("line 1 missing needs you dot: %q", lines[1])
	}
	if !strings.Contains(lines[1], "\x1b[1;38;5;214mNEEDS YOU\x1b[0m") {
		t.Errorf("line 1 missing needs you display colour: %q", lines[1])
	}

	if strings.Count(lines[2], "\x1b[") != 2 {
		t.Errorf("line 2 should contain exactly 2 escape sequences (the dot only), got %d: %q", strings.Count(lines[2], "\x1b["), lines[2])
	}
}

func setupPlannerStatusStore(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	bindings := []store.Binding{
		{
			Name:             "zeta",
			CWD:              "/a",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "alpha",
			CWD:              "/b",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "other",
			CWD:              "/c",
			PlannerID:        otherClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "finished",
			CWD:              "/d",
			PlannerID:        testClaimPlanner,
			Builder:          store.Endpoint{Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateDone,
		},
	}
	for _, b := range bindings {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", b.Name, err)
		}
	}
	return rt
}

func TestPlannerStatusFiltersToOnePlanner(t *testing.T) {
	rt := setupPlannerStatusStore(t)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, testClaimPlanner)
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(rep.Bindings))
	}
	if rep.Bindings[0].Name != "alpha" || rep.Bindings[1].Name != "zeta" {
		t.Errorf("got bindings [%s, %s], want [alpha, zeta]", rep.Bindings[0].Name, rep.Bindings[1].Name)
	}
	for i, b := range rep.Bindings {
		if b.PlannerID != testClaimPlanner {
			t.Errorf("row %d PlannerID = %q, want %q", i, b.PlannerID, testClaimPlanner)
		}
	}

	repOther, err := PlannerStatus(ctx, rt, otherClaimPlanner)
	if err != nil {
		t.Fatalf("PlannerStatus(other): %v", err)
	}
	if len(repOther.Bindings) != 1 || repOther.Bindings[0].Name != "other" {
		t.Errorf("got %d bindings for the other planner, want only 'other'", len(repOther.Bindings))
	}
}

func TestPlannerStatusEmptyPlannerIsEmpty(t *testing.T) {
	rt := setupPlannerStatusStore(t)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, "")
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 0 {
		t.Errorf("got %d bindings, want 0", len(rep.Bindings))
	}
}

func TestRoundClock(t *testing.T) {
	start := baseTime.Add(-10 * time.Minute)
	end := baseTime.Add(-3 * time.Minute)

	// Zero start -> "--"
	zero := BindingStatus{}
	if got := roundClock(zero, baseTime); got != "--" {
		t.Errorf("roundClock(zero) = %q, want %q", got, "--")
	}

	// Open -> now - start, and advancing now advances it
	open := BindingStatus{RoundStart: start}
	now1 := baseTime
	now2 := baseTime.Add(5 * time.Minute)
	got1 := roundClock(open, now1)
	got2 := roundClock(open, now2)
	if got1 != "10m" {
		t.Errorf("roundClock(open, now1) = %q, want '10m'", got1)
	}
	if got2 != "15m" {
		t.Errorf("roundClock(open, now2) = %q, want '15m'", got2)
	}
	if got1 == got2 {
		t.Errorf("advancing now must advance open round clock: %q vs %q", got1, got2)
	}

	// Closed -> end - start, and two different now values give the same string
	closed := BindingStatus{RoundStart: start, RoundEnd: end}
	c1 := roundClock(closed, now1)
	c2 := roundClock(closed, now2)
	if c1 != "7m" {
		t.Errorf("roundClock(closed, now1) = %q, want '7m'", c1)
	}
	if c1 != c2 {
		t.Errorf("different now values must give same string for closed round: %q vs %q", c1, c2)
	}
}

func TestRenderStatusLineRemoteServer(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            1,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		Server:           "contabo",
		RoundStart:       baseTime.Add(-10 * time.Minute),
		LastPayload:      &LastEvent{TS: baseTime.Add(-10 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
	}
	out := RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120)
	plain := stripSGR(splitLines(out)[0])
	if !strings.Contains(plain, "r1 · opencode@contabo · plan sent") {
		t.Errorf("expected opencode@contabo, got: %q", plain)
	}

	b.Server = ""
	outLocal := RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120)
	plainLocal := stripSGR(splitLines(outLocal)[0])
	if !strings.Contains(plainLocal, "r1 · opencode · plan sent") {
		t.Errorf("expected opencode, got: %q", plainLocal)
	}
}

func TestRoundTokensAddsPrior(t *testing.T) {
	// 1. open round with live 41k plus prior 100k gives "141k tok"
	b1 := BindingStatus{
		RoundEnd: time.Time{}, // open
		LiveUsage: &usage.Usage{
			Samples: 1,
			Tokens:  usage.Tokens{In: 41_000},
		},
		RoundPriorTokens: usage.Tokens{In: 100_000},
	}
	if got := roundTokens(b1); got != "141k tok" {
		t.Errorf("open roundTokens = %q, want %q", got, "141k tok")
	}

	// 2. closed with 2.1M plus 0.4M gives "2.5M tok"
	b2 := BindingStatus{
		RoundEnd: baseTime, // closed
		RoundUsage: &usage.Usage{
			Tokens: usage.Tokens{In: 2_100_000},
		},
		RoundPriorTokens: usage.Tokens{In: 400_000},
	}
	if got := roundTokens(b2); got != "2.5M tok" {
		t.Errorf("closed roundTokens = %q, want %q", got, "2.5M tok")
	}

	// 3. no live samples but prior 100k gives "100k tok"
	b3 := BindingStatus{
		RoundEnd:         time.Time{}, // open
		LiveUsage:        &usage.Usage{Samples: 0},
		RoundPriorTokens: usage.Tokens{In: 100_000},
	}
	if got := roundTokens(b3); got != "100k tok" {
		t.Errorf("no live samples roundTokens = %q, want %q", got, "100k tok")
	}
}

func TestStatusLineRows(t *testing.T) {
	now := baseTime

	t.Run("NEEDS YOU row with a report LastPayload", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            2,
			Display:          "NEEDS YOU",
			BuilderCandidate: "claude/model",
			RoundStart:       now.Add(-5 * time.Minute),
			RoundEnd:         now.Add(-2 * time.Minute),
			LastPayload: &LastEvent{
				Kind: store.KindReport,
				Note: "halted",
				TS:   now.Add(-2 * time.Minute),
			},
			PlannerRoute: "deliverer",
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		row := rows[0]
		if row.Name != "worker" || row.Round != 2 || row.Display != "NEEDS YOU" {
			t.Errorf("row = %+v", row)
		}
		if row.Waiting != "report in (halted)" {
			t.Errorf("Waiting = %q, want 'report in (halted)'", row.Waiting)
		}
		if row.Clock != "3m" {
			t.Errorf("Clock = %q, want '3m'", row.Clock)
		}
		if row.LastKind != "report" {
			t.Errorf("LastKind = %q, want 'report'", row.LastKind)
		}
		expectedTS := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
		if row.LastTS != expectedTS {
			t.Errorf("LastTS = %q, want %q", row.LastTS, expectedTS)
		}
		if row.Route != "deliverer" {
			t.Errorf("Route = %q, want 'deliverer'", row.Route)
		}
	})

	t.Run("open round with LiveUsage samples -> tokens = <n> tok", func(t *testing.T) {
		b := BindingStatus{
			Name:             "api",
			Round:            1,
			Display:          "ACTIVE",
			BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
			RoundStart:       now.Add(-12 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-12 * time.Minute), Kind: store.KindPlan},
			LiveUsage: &usage.Usage{
				Tokens:  usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000},
				Samples: 1,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Tokens != "41k tok" {
			t.Errorf("Tokens = %q, want '41k tok'", rows[0].Tokens)
		}
	})

	t.Run("closed round with RoundUsage -> its tokens", func(t *testing.T) {
		b := BindingStatus{
			Name:       "api",
			Round:      1,
			RoundStart: now.Add(-10 * time.Minute),
			RoundEnd:   now.Add(-5 * time.Minute),
			RoundUsage: &usage.Usage{
				Tokens: usage.Tokens{In: 20_000, Out: 5_000},
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Tokens != "25k tok" {
			t.Errorf("Tokens = %q, want '25k tok'", rows[0].Tokens)
		}
	})

	t.Run("remote row (Server set) -> harness <h>@<server>", func(t *testing.T) {
		b := BindingStatus{
			Name:             "api",
			Round:            1,
			BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
			Server:           "contabo",
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Harness != "opencode@contabo" {
			t.Errorf("Harness = %q, want 'opencode@contabo'", rows[0].Harness)
		}
	})

	t.Run("no RoundStart -> clock --", func(t *testing.T) {
		b := BindingStatus{
			Name:  "api",
			Round: 1,
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].Clock != "--" {
			t.Errorf("Clock = %q, want '--'", rows[0].Clock)
		}
	})

	t.Run("no payload -> last_kind \"\", last_ts \"\"", func(t *testing.T) {
		b := BindingStatus{
			Name:        "api",
			Round:       1,
			LastPayload: nil,
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].LastKind != "" {
			t.Errorf("LastKind = %q, want empty", rows[0].LastKind)
		}
		if rows[0].LastTS != "" {
			t.Errorf("LastTS = %q, want empty", rows[0].LastTS)
		}
	})

	t.Run("empty report -> [] (not nil) once wrapped in StatusLineDoc and marshalled", func(t *testing.T) {
		rows := StatusLineRows(Report{}, now)
		if rows == nil {
			t.Fatal("StatusLineRows returned nil slice, want non-nil empty slice")
		}
		doc := StatusLineDoc{
			Planner: nil,
			Now:     now.UTC(),
			Rows:    rows,
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		s := string(data)
		if !strings.Contains(s, `"planner":null`) {
			t.Errorf("json %q does not contain '\"planner\":null'", s)
		}
		if !strings.Contains(s, `"rows":[]`) {
			t.Errorf("json %q does not contain '\"rows\":[]'", s)
		}
	})

	t.Run("delivered report (Pending nil) -> needs_you false, report_in true, report_round = its round", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   4,
			Display: "ACTIVE",
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false (the report reached the chat)", rows[0].NeedsYou)
		}
		if !rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want true", rows[0].ReportIn)
		}
		if rows[0].ReportRound != 3 {
			t.Errorf("ReportRound = %d, want 3", rows[0].ReportRound)
		}
	})

	t.Run("pending report, deliverer live, 5s old -> needs_you false, report_in false", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            4,
			Display:          "ACTIVE",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now.Add(-5 * time.Second),
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false (the push has 60s to land)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

	t.Run("pending report, deliverer live, 61s old -> needs_you true", func(t *testing.T) {
		b := BindingStatus{
			Name:             "worker",
			Round:            4,
			Display:          "ACTIVE",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now.Add(-61 * time.Second),
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true (waited longer than PendingNeedsYouAfter)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

	t.Run("pending report, route pull -> needs_you true at once", func(t *testing.T) {
		b := BindingStatus{
			Name:         "worker",
			Round:        4,
			Display:      "ACTIVE",
			PlannerRoute: "pull",
			Pending:      &PendingInfo{Round: 3, Kind: store.KindReport},
			LastPayload: &LastEvent{
				Round:     3,
				Kind:      store.KindReport,
				Direction: store.DirToPlanner,
				TS:        now,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true (a pull route cannot be pushed to)", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (still pending)", rows[0].ReportIn)
		}
	})

	t.Run("NEEDS YOU display with a plan payload -> needs_you true, report_in false, report_round 0", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   2,
			Display: "NEEDS YOU",
			LastPayload: &LastEvent{
				Round:     2,
				Kind:      store.KindPlan,
				Direction: store.DirToBuilder,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if !rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want true", rows[0].NeedsYou)
		}
		if rows[0].ReportIn {
			t.Errorf("ReportIn = %v, want false (no to-planner payload)", rows[0].ReportIn)
		}
		if rows[0].ReportRound != 0 {
			t.Errorf("ReportRound = %d, want 0", rows[0].ReportRound)
		}
	})

	t.Run("ACTIVE + plan to builder -> false, 0", func(t *testing.T) {
		b := BindingStatus{
			Name:    "worker",
			Round:   1,
			Display: "ACTIVE",
			LastPayload: &LastEvent{
				Round:     1,
				Kind:      store.KindPlan,
				Direction: store.DirToBuilder,
			},
		}
		rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, now)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		if rows[0].NeedsYou {
			t.Errorf("NeedsYou = %v, want false", rows[0].NeedsYou)
		}
		if rows[0].ReportRound != 0 {
			t.Errorf("ReportRound = %d, want 0", rows[0].ReportRound)
		}
	})
}

// TestRenderStatusLineSharesTheRowRule is #393: the Claude Code line is
// rendered from StatusLineRows, so its text carries exactly the row's shown
// round (report_round when > 0, else round) and the row's word -- no word for
// ACTIVE, REPORT IN for a delivered report, NEEDS YOU for a stalled pending or
// a NEEDS YOU display.
func TestRenderStatusLineSharesTheRowRule(t *testing.T) {
	now := baseTime
	rep := Report{Bindings: []BindingStatus{
		{
			Name:             "active",
			Round:            2,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-3 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-3 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		},
		{
			Name:             "delivered",
			Round:            5,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-4 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-4 * time.Minute), Round: 4, Kind: store.KindReport, Direction: store.DirToPlanner},
		},
		{
			Name:             "stalled",
			Round:            7,
			Display:          "ACTIVE",
			BuilderCandidate: "agy",
			PlannerRoute:     "deliverer",
			PlannerRouteLive: true,
			Pending:          &PendingInfo{Round: 6, Kind: store.KindReport},
			RoundStart:       now.Add(-7 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-2 * time.Minute), Round: 6, Kind: store.KindReport, Direction: store.DirToPlanner},
		},
		{
			Name:             "stuck",
			Round:            3,
			Display:          "NEEDS YOU",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-2 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-2 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		},
		{
			Name:             "paused",
			Round:            6,
			Display:          "PAUSED",
			BuilderCandidate: "agy",
			RoundStart:       now.Add(-6 * time.Minute),
			LastPayload:      &LastEvent{TS: now.Add(-1 * time.Minute), Round: 5, Kind: store.KindReport, Direction: store.DirToPlanner},
		},
	}}

	rows := StatusLineRows(rep, now)
	lines := splitLines(RenderStatusLine(rep, now, 120))
	if len(rows) != len(lines) {
		t.Fatalf("got %d rows and %d lines, want one line per row", len(rows), len(lines))
	}

	cases := []struct {
		i     int
		round int
		word  string
		dot   string
	}{
		{0, 2, "", "○"},
		{1, 4, "REPORT IN", "○"},
		{2, 6, "NEEDS YOU", "●"},
		{3, 3, "NEEDS YOU", "●"},
		{4, 5, "PAUSED", "○"},
	}

	for _, tc := range cases {
		row := rows[tc.i]
		shown := row.Round
		if row.ReportRound > 0 {
			shown = row.ReportRound
		}
		if shown != tc.round {
			t.Errorf("row %d shown round = %d, want %d", tc.i, shown, tc.round)
		}
		plain := stripSGR(lines[tc.i])
		if !strings.Contains(plain, "r"+strconv.Itoa(tc.round)) {
			t.Errorf("line %d %q does not carry the row's shown round r%d", tc.i, plain, tc.round)
		}
		if tc.word == "" {
			for _, absent := range []string{"ACTIVE", "REPORT IN", "NEEDS YOU"} {
				if strings.Contains(plain, absent) {
					t.Errorf("line %d %q must carry no word, found %q", tc.i, plain, absent)
				}
			}
		} else if !strings.Contains(plain, tc.word) {
			t.Errorf("line %d %q does not carry the row's word %q", tc.i, plain, tc.word)
		}
		if !strings.HasPrefix(plain, tc.dot+" ") {
			t.Errorf("line %d %q does not start with the %s dot", tc.i, plain, tc.dot)
		}
	}
}
