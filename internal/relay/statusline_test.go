package relay

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relay/internal/store"
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

func statuslineFixture(now time.Time) Report {
	return Report{
		Bindings: []BindingStatus{
			{
				Name:             "api",
				Round:            3,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				Last: &LastEvent{
					TS:        now.Add(-12 * time.Minute),
					Kind:      store.KindPlan,
					Direction: store.DirToBuilder,
				},
			},
			{
				Name:             "client",
				Round:            1,
				Display:          "NEEDS YOU",
				BuilderCandidate: "opencode",
				Detail:           "builder pane gone",
				Last: &LastEvent{
					TS:   now.Add(-4 * time.Minute),
					Kind: store.KindPlan,
				},
			},
			{
				Name:             "docs",
				Round:            2,
				Display:          "HELD",
				BuilderCandidate: "agy",
				Pending: &PendingInfo{
					Round: 2,
					Kind:  store.KindReport,
					Hold: &HoldInfo{
						QuietMS: 23000,
						GraceMS: 60000,
					},
				},
				Last: &LastEvent{
					TS:        now.Add(-23 * time.Second),
					Kind:      store.KindReport,
					Direction: store.DirToPlanner,
				},
			},
		},
	}
}

func TestRenderStatusLineEmpty(t *testing.T) {
	if got := RenderStatusLine(Report{}, baseTime, 80); got != "" {
		t.Errorf("RenderStatusLine(Report{}, baseTime, 80) = %q, want empty", got)
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
	if !strings.HasPrefix(plain1, "● client  r1 · opencode · builder pane gone") {
		t.Errorf("line 1 prefix mismatch: %q", plain1)
	}
	if !strings.HasSuffix(plain1, " 4m · NEEDS YOU") {
		t.Errorf("line 1 suffix mismatch: %q", plain1)
	}

	plain2 := stripSGR(lines[2])
	if !strings.HasPrefix(plain2, "○ docs    r2 · agy · report → planner · quiet 23s of 1m0s") {
		t.Errorf("line 2 prefix mismatch: %q", plain2)
	}
	if !strings.HasSuffix(plain2, " 23s · HELD") {
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
	if strings.Contains(plain2, "1m0s") {
		t.Errorf("line 2 should not contain '1m0s': %q", plain2)
	}

	suffixes := []string{" 12m · ACTIVE", " 4m · NEEDS YOU", " 23s · HELD"}
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
			name: "nudge",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				Nudge:            &NudgeInfo{QuietMS: 23000, GraceMS: 60000},
				Last:             &LastEvent{Kind: store.KindPlan},
			},
			expectMid: "nudged · quiet 23s of 1m0s",
		},
		{
			name: "report note",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				Last:             &LastEvent{Kind: store.KindReport, Note: "unmarked"},
			},
			expectMid: "report in (unmarked)",
		},
		{
			name: "switch",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				Last:             &LastEvent{Kind: store.KindSwitch},
			},
			expectMid: "r1 · agy · switch",
		},
		{
			name: "last nil",
			binding: BindingStatus{
				Name:             "api",
				Round:            1,
				Display:          "ACTIVE",
				BuilderCandidate: "agy",
				Last:             nil,
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
				Last:             &LastEvent{Kind: store.KindPlan},
			},
			expectMid:   "r1 · plan sent",
			noSeparator: true,
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

func setupPlannerStatusStore(t *testing.T, f *fakeHerdr) Runtime {
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

func TestPlannerStatusFiltersToOnePane(t *testing.T) {
	f := &fakeHerdr{}
	rt := setupPlannerStatusStore(t, f)
	ctx := context.Background()

	rep, err := PlannerStatus(ctx, rt, "w2:p3")
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
		if b.PlannerPane != "w2:p3" {
			t.Errorf("row %d PlannerPane = %q, want %q", i, b.PlannerPane, "w2:p3")
		}
		if b.PlannerStatus != "gone" {
			t.Errorf("row %d PlannerStatus = %q, want gone", i, b.PlannerStatus)
		}
		if len(b.Foreign) != 0 {
			t.Errorf("row %d Foreign not empty: %+v", i, b.Foreign)
		}
	}

	repOther, err := PlannerStatus(ctx, rt, "w9:p1")
	if err != nil {
		t.Fatalf("PlannerStatus(w9:p1): %v", err)
	}
	if len(repOther.Bindings) != 1 || repOther.Bindings[0].Name != "other" {
		t.Errorf("got %d bindings for w9:p1, want only 'other'", len(repOther.Bindings))
	}
}

func TestPlannerStatusEmptyPaneIsEmpty(t *testing.T) {
	f := &fakeHerdr{}
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

func TestPlannerStatusNeverProbesHerdr(t *testing.T) {
	f := &fakeHerdr{}
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
