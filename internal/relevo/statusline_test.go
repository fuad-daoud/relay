package relevo

import (
	"context"
	"os"
	"regexp"
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
			expectRight: "-- · ",
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
	if !strings.HasSuffix(plain, " 12m · ACTIVE") {
		t.Errorf("line %q does not have right cell beginning %q", plain, "12m · ")
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
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "plan sent · live $0.02 · 41k tok") {
		t.Errorf("no live segment: %q", plain)
	}
	if !strings.HasSuffix(plain, "ACTIVE") {
		t.Errorf("the right cell must survive: %q", plain)
	}
}

// TestRenderStatusLineSpendSegment pins the closed-round segment: a row
// with a spend and no live figure shows the spend last.
func TestRenderStatusLineSpendSegment(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            3,
		Display:          "NEEDS YOU",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		Spend:            &usage.Spend{Rounds: 2, Measured: 1.51, Tokens: usage.Tokens{In: 2_100_000}},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "· $1.51 · 2.1M tok") {
		t.Errorf("no spend segment: %q", plain)
	}
}

func TestRenderStatusLineLiveWinsOverSpend(t *testing.T) {
	b := BindingStatus{
		Name:             "api",
		Round:            4,
		Display:          "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm-5.3-flash",
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		Spend:            &usage.Spend{Rounds: 3, Measured: 1.51, Tokens: usage.Tokens{In: 2_100_000}},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 120))[0])
	if !strings.Contains(plain, "live ") {
		t.Errorf("the live figure must show: %q", plain)
	}
	if strings.Contains(plain, "$1.51") {
		t.Errorf("spend must yield to the live figure, never share a line: %q", plain)
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
		LastPayload:      &LastEvent{TS: baseTime.Add(-12 * time.Minute), Kind: store.KindPlan, Direction: store.DirToBuilder},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash",
			Tokens: usage.Tokens{In: 4_000, CacheRead: 30_000, Out: 7_000}, Cost: usage.Cost{USD: 0.02, Basis: usage.Measured}, Samples: 1},
	}
	plain := stripSGR(splitLines(RenderStatusLine(Report{Bindings: []BindingStatus{b}}, baseTime, 60))[0])
	if !strings.HasSuffix(plain, "ACTIVE") {
		t.Errorf("ACTIVE must survive the narrow row: %q", plain)
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
	if !strings.HasSuffix(plain0, " 12m · ACTIVE") {
		t.Errorf("line 0 suffix mismatch: %q", plain0)
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

	suffixes := []string{" 12m · ACTIVE", " 4m · NEEDS YOU", " 23s · PAUSED"}
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
	want := "○ api  r3 · agy · plan sent · 12m · ACTIVE"
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
	if !strings.Contains(lines[0], "\x1b[38;5;42mACTIVE\x1b[0m") {
		t.Errorf("line 0 missing active display colour: %q", lines[0])
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
