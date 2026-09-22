package relay

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
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

// TestRenderStatusLineIgnoresBookkeepingLast pins that the statusline reads
// LastPayload, not relay's own bookkeeping entries (Last): a binding whose
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

func setupPlannerStatusStore(t *testing.T, f *fakePanes) Runtime {
	t.Helper()
	rt := newRuntime(t, f)
	bindings := []store.Binding{
		{
			Name:             "zeta",
			CWD:              "/a",
			Planner:          store.Endpoint{PaneID: "w2:p3"},
			Builder:          store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "alpha",
			CWD:              "/b",
			Planner:          store.Endpoint{PaneID: "w2:p3"},
			Builder:          store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "other",
			CWD:              "/c",
			Planner:          store.Endpoint{PaneID: "w9:p1"},
			Builder:          store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			BuilderCandidate: testAgyRef,
			Round:            1,
			State:            store.StateActive,
		},
		{
			Name:             "finished",
			CWD:              "/d",
			Planner:          store.Endpoint{PaneID: "w2:p3"},
			Builder:          store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
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

func TestPlannerStatusEmptyPaneIsEmpty(t *testing.T) {
	f := &fakePanes{}
	rt := setupPlannerStatusStore(t, f)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, "")
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 0 {
		t.Errorf("got %d bindings, want 0", len(rep.Bindings))
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

func TestPlannerStatusNeverProbesHerdr(t *testing.T) {
	f := &fakePanes{}
	rt := setupPlannerStatusStore(t, f)
	ctx := context.Background()

	f.onList = func() { t.Fatal("PlannerStatus called ListAgents") }
	rep, err := PlannerStatus(ctx, rt, "w2:p3")
	if err != nil {
		t.Fatalf("PlannerStatus: %v", err)
	}
	if len(rep.Bindings) != 2 {
		t.Errorf("got %d bindings, want 2", len(rep.Bindings))
	}
	if f.listCalls != 0 {
		t.Errorf("f.listCalls = %d, want 0", f.listCalls)
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
