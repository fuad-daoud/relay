package relevo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestDoneKeepsRemoteWorktreeWhileRoundOpen(t *testing.T) {
	st := store.New(t.TempDir())
	fg := &fakeGit{}
	b := remoteBinding("contabo")
	b.Worktree = t.TempDir()
	b.Branch = "relevo/api"
	b.Round = 1
	b.RoundStartedAt = baseTime
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Git: fg, Remote: &fakeRemote{}, Now: func() time.Time { return baseTime }}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeKept != b.Worktree {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, b.Worktree)
	}
	wantReason := "round 1 open; the builder may still write"
	if res.KeptReason != wantReason {
		t.Errorf("KeptReason = %q, want %q", res.KeptReason, wantReason)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

func TestRenderStatusShowsDetailLine(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "doctor", CWD: "/repo", Round: 3,
		Display:          "NEEDS YOU",
		BuilderCandidate: testAgyRef,
		PlannerKind:      "claude", BuilderKind: "agy", BuilderStatus: "gone",
		Detail: "round 2 report delivered; nothing outstanding -- unless you want another round",
	}}})

	if !strings.Contains(out, "  detail   round 2 report delivered") {
		t.Errorf("detail line missing from:\n%s", out)
	}
	// It must sit between the builder line and pending, where a human about to
	// rebind is already looking.
	builderAt := strings.Index(out, "  builder ")
	detailAt := strings.Index(out, "  detail ")
	pendingAt := strings.Index(out, "  pending ")
	if !(builderAt < detailAt && detailAt < pendingAt) {
		t.Errorf("detail must follow builder and precede pending, got:\n%s", out)
	}
}

// TestRenderStatusPlannerChat is #386's status surface: a row whose planner
// carries a chat label and a link shows them after its route, and a row that
// carries neither is byte-identical to the line before these fields existed.
func TestRenderStatusPlannerChat(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{
		{
			Name: "one", CWD: "/a", Round: 1, Display: "ACTIVE",
			PlannerName: "architect-1", PlannerKind: "claude", PlannerRoute: "pull",
			PlannerChatLabel: `"fix the flake"`, PlannerChatLink: "https://claude.ai/code/session_01TEST",
		},
		{
			Name: "two", CWD: "/b", Round: 1, Display: "ACTIVE",
			PlannerName: "architect-2", PlannerKind: "claude", PlannerRoute: "pull",
		},
	}})

	var plannerLines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  planner  ") {
			plannerLines = append(plannerLines, line)
		}
	}
	if len(plannerLines) != 2 {
		t.Fatalf("got %d planner lines, want 2:\n%s", len(plannerLines), out)
	}

	wantFirst := `route pull · "fix the flake" · https://claude.ai/code/session_01TEST`
	if !strings.HasSuffix(plannerLines[0], wantFirst) {
		t.Errorf("planner line %q does not end with %q", plannerLines[0], wantFirst)
	}
	// Row two has neither field set, so nothing follows its route.
	if !strings.HasSuffix(plannerLines[1], "route pull") {
		t.Errorf("planner line %q does not end with %q", plannerLines[1], "route pull")
	}
}

func TestRenderStatusOmitsEmptyDetail(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "ok", CWD: "/repo", Round: 1, Display: "ACTIVE",
		PlannerKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
		BuilderCandidate: testAgyRef,
	}}})

	if strings.Contains(out, "detail") {
		t.Errorf("no detail line may appear for a healthy binding:\n%s", out)
	}
}

func TestRenderStatusOmitsTheConsultCountWhenZero(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Consults: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "+0c") {
		t.Errorf("rendered a zero consult count:\n%s", out)
	}
}

func TestRenderStatusShowsSwitches(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 1,
	}}}

	if out := RenderStatus(r); !strings.Contains(out, "switched 1x") {
		t.Errorf("RenderStatus output missing switched 1x:\n%s", out)
	}
}

func TestRenderStatusOmitsSwitchedWhenZero(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "switched") {
		t.Errorf("rendered a zero switch count:\n%s", out)
	}
}

func TestHideDoneRemovesOnlyDoneRows(t *testing.T) {
	in := Report{
		Bindings: []BindingStatus{
			{Name: "first", State: string(store.StateActive)},
			{Name: "second", State: string(store.StateDone)},
			{Name: "third", State: string(store.StateBroken)},
		},
	}

	got := HideDone(in)

	if len(in.Bindings) != 3 {
		t.Fatalf("HideDone modified input report: len = %d, want 3", len(in.Bindings))
	}
	if got.DoneHidden != 1 {
		t.Errorf("DoneHidden = %d, want 1", got.DoneHidden)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(got.Bindings))
	}
	if got.Bindings[0].Name != "first" || got.Bindings[1].Name != "third" {
		t.Errorf("bindings = %+v, want first and third in original order", got.Bindings)
	}
}

func TestRenderStatusFooterCountsHidden(t *testing.T) {
	cases := []struct {
		hidden    int
		wantSub   string
		wantNoSub string
	}{
		{hidden: 3, wantSub: "3 done · relevo unbind --done to clear"},
		{hidden: 1, wantSub: "1 done · relevo unbind --done to clear"},
		{hidden: 0, wantNoSub: "done ·"},
	}

	for _, tc := range cases {
		r := Report{
			Bindings: []BindingStatus{
				{Name: "live", CWD: "/repo", Round: 1, Display: "ACTIVE"},
			},
			DoneHidden: tc.hidden,
		}
		out := RenderStatus(r)
		if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
			t.Errorf("hidden=%d: RenderStatus output missing %q:\n%s", tc.hidden, tc.wantSub, out)
		}
		if tc.wantNoSub != "" && strings.Contains(out, tc.wantNoSub) {
			t.Errorf("hidden=%d: RenderStatus output should not contain %q:\n%s", tc.hidden, tc.wantNoSub, out)
		}
	}
}

func TestRenderStatusFooterOnlyWhenEverythingIsDone(t *testing.T) {
	r := Report{
		Bindings:   nil,
		DoneHidden: 2,
	}
	out := RenderStatus(r)
	if !strings.Contains(out, "2 done · relevo unbind --done to clear") {
		t.Errorf("RenderStatus output missing footer:\n%s", out)
	}
	if strings.Contains(out, "no bindings") {
		t.Errorf("RenderStatus output should not contain 'no bindings':\n%s", out)
	}
}

func TestRenderStatusGatedBlock(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	t.Cleanup(SetGateClock(func() time.Time { return now }))
	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: []ledger.Gate{
			{Token: testAgyRef, Name: "agy-m", Kind: ledger.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
			{Token: testClaudeRef, Name: "claude-m", Kind: ledger.RateLimited, Since: now, Until: time.Time{}},
		},
	}

	out := RenderStatus(r)
	if !strings.Contains(out, "candidates\n") {
		t.Fatalf("missing candidates block:\n%s", out)
	}
	rest := out[strings.Index(out, "candidates\n")+len("candidates\n"):]
	lines := strings.SplitN(rest, "\n", 3)
	if len(lines) < 2 {
		t.Fatalf("expected two gate rows, got:\n%s", rest)
	}
	wantUntil := "until " + now.Add(10*time.Minute).Local().Format("15:04")
	if !strings.Contains(lines[0], "agy-m") || !strings.Contains(lines[0], "spawn failed") || !strings.Contains(lines[0], wantUntil) {
		t.Errorf("row 0 = %q", lines[0])
	}
	if strings.Contains(lines[0], testAgyRef) {
		t.Errorf("row 0 must print the gate's name, not its token: %q", lines[0])
	}
	if !strings.Contains(lines[1], "claude-m") || !strings.Contains(lines[1], "rate-limited") || !strings.Contains(lines[1], "until cleared") {
		t.Errorf("row 1 = %q", lines[1])
	}
}

// TestRenderStatusGatedBlockFallsBackToToken pins the other half of A1 §4.4:
// a gate with no name -- one built without a candidate set -- prints its
// token.
func TestRenderStatusGatedBlockFallsBackToToken(t *testing.T) {
	r := Report{
		Gated: []ledger.Gate{{Token: testAgyRef, Kind: ledger.RateLimited, Until: time.Time{}}},
	}

	out := RenderStatus(r)
	if !strings.Contains(out, testAgyRef) {
		t.Errorf("RenderStatus =\n%s\nwant it naming %q", out, testAgyRef)
	}
}

// TestStatusJSONHasBuilderName pins A1 §4.4: a binding's status document
// carries the candidate's short name beside the token. A token no longer
// configured reads as itself, exactly as the text listing prints it, and a
// binding with no candidate carries no key at all.
func TestStatusJSONHasBuilderName(t *testing.T) {
	rt := newRuntime(t)

	row := statusRowForTest(t, rt, store.Binding{
		Name: "webshop", CWD: "/repo",
		Builder:          store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		BuilderCandidate: testAgyRef,
		Round:            1, State: store.StateActive,
	})
	if row.BuilderName != "agy-m" {
		t.Errorf("BuilderName = %q, want agy-m", row.BuilderName)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"builder_name":"agy-m"`) {
		t.Errorf("JSON = %s, want it to contain %q", raw, `"builder_name":"agy-m"`)
	}

	gone := statusRowForTest(t, rt, store.Binding{
		Name: "gone", CWD: "/gone-repo",
		Builder:          store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/gone",
		Round:            1, State: store.StateActive,
	})
	// Round 3 F3: a token no longer configured leaves BuilderName empty, so
	// the name field never carries a token. The renderers print the token.
	if gone.BuilderName != "" {
		t.Errorf("BuilderName for an unconfigured token = %q, want empty", gone.BuilderName)
	}
	raw, err = json.Marshal(gone)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "builder_name") {
		t.Errorf("JSON = %s, want no builder_name key for a retired token", raw)
	}
	if out := RenderStatus(Report{Bindings: []BindingStatus{gone}}); !strings.Contains(out, "`agy/test/gone`") {
		t.Errorf("RenderStatus =\n%s\nwant it printing the retired token", out)
	}

	none := statusRowForTest(t, rt, store.Binding{
		Name: "adopted", CWD: "/adopted-repo",
		Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		Round:   1, State: store.StateActive,
	})
	raw, err = json.Marshal(none)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "builder_name") {
		t.Errorf("JSON = %s, want no builder_name key with no candidate", raw)
	}
}

func TestRenderStatusNoGatesIsUnchanged(t *testing.T) {
	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			PlannerKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: nil,
	}

	out := RenderStatus(r)
	if strings.Contains(out, "candidates") {
		t.Errorf("no gates must render no candidates block:\n%s", out)
	}
}

func TestRenderStatusGatesWithNoBindings(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	r := Report{
		Gated: []ledger.Gate{
			{Token: testAgyRef, Kind: ledger.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
		},
	}

	out := RenderStatus(r)
	if !strings.HasPrefix(out, "no bindings\ncandidates\n") {
		t.Errorf("out = %q, want it to start with %q", out, "no bindings\ncandidates\n")
	}
}

func TestBindingStatusKey(t *testing.T) {
	if got := (BindingStatus{Name: "api"}).Key(); got != "api" {
		t.Errorf("planner key = %q, want api", got)
	}
	if got := (BindingStatus{Name: "api", Owner: "SHA256:abc"}).Key(); got != "SHA256:abc/api" {
		t.Errorf("server key = %q, want SHA256:abc/api", got)
	}
}

// TestShortOwner pins the truncation: a fingerprint id keeps "SHA256:" and
// the first twelve characters, then an ellipsis; a short id, or one without
// the prefix, is unchanged.

func TestShortOwner(t *testing.T) {
	if got := ShortOwner("SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"); got != "SHA256:VLERFMZnvN5H…" {
		t.Errorf("ShortOwner(fingerprint) = %q", got)
	}
	if got := ShortOwner("SHA256:short"); got != "SHA256:short" {
		t.Errorf("ShortOwner(short) = %q", got)
	}
	if got := ShortOwner("notanid"); got != "notanid" {
		t.Errorf("ShortOwner(prefix-less) = %q", got)
	}
}

// TestHideDoneKeepsGated pins that hiding DONE bindings does not drop the
// machine-wide gated block: cmdStatus applies HideDone between Status and
// RenderStatus, so a gate lost here never reaches the terminal.

func TestHideDoneKeepsGated(t *testing.T) {
	in := Report{
		Bindings: []BindingStatus{{Name: "old", State: string(store.StateDone)}},
		Gated:    []ledger.Gate{{Token: "agy/test/m", Kind: ledger.RateLimited}},
	}
	out := HideDone(in)
	if out.DoneHidden != 1 || len(out.Bindings) != 0 {
		t.Fatalf("HideDone = %+v, want one hidden and no rows", out)
	}
	if len(out.Gated) != 1 || out.Gated[0].Token != "agy/test/m" {
		t.Fatalf("Gated = %+v, want the gate carried through", out.Gated)
	}
}

func TestRenderStatusShowsDirty(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2, Dirty: true, Consults: 1,
	}}}
	out := RenderStatus(r)
	if !strings.Contains(out, "round 2   ACTIVE dirty +1c") {
		t.Errorf("RenderStatus output missing 'dirty' after the display word:\n%s", out)
	}
}

func TestRenderStatusOmitsDirtyWhenClean(t *testing.T) {
	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastClose: &CloseInfo{Round: 1, Tree: "dirty"}, // the raw fact, without the rule applied
	}}}
	if out := RenderStatus(r); strings.Contains(out, "dirty") {
		t.Errorf("rendered dirty from LastClose instead of Dirty:\n%s", out)
	}
}

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
	if !strings.Contains(text, "  spend    2 rounds +1c · $0.40 · ~$0.02 · 330 tok") {
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

// statusLiveFixture is a headless binding with an open round (round 1
// sent) and a live reader wired, with now moved past the round's start so
// the live figure has a duration.

func TestStatusLiveUsageOnOpenRound(t *testing.T) {
	rt, b := statusLiveFixture(t, &fakeUsage{peekSamples: []usage.Sample{
		{Provider: "cline-pass", Model: "glm-5.3-flash", Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200}, USD: 0.04, HasCost: true},
	}})
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.LiveUsage == nil {
		t.Fatal("LiveUsage = nil on an open round")
	}
	if got.LiveUsage.Cost.Basis != usage.Measured || got.LiveUsage.Cost.USD != 0.04 {
		t.Errorf("cost = %+v, want measured $0.04", got.LiveUsage.Cost)
	}
	if got.LiveUsage.DurationMS <= 0 {
		t.Errorf("DurationMS = %d, want the round's running wall time", got.LiveUsage.DurationMS)
	}
	if got.LiveUsage.Harness != "agy" || got.LiveUsage.Provider != "cline-pass" {
		t.Errorf("harness/provider = %q/%q, want agy/cline-pass", got.LiveUsage.Harness, got.LiveUsage.Provider)
	}
	// The live figure never touches Spend: with no recorded entries the
	// spend is what the fixture entries give -- nil.
	if got.Spend != nil {
		t.Errorf("Spend = %+v, want nil (the live figure is never summed)", got.Spend)
	}
	if len(fuFrom(rt).peeks) != 1 {
		t.Fatalf("%d peeks, want 1", len(fuFrom(rt).peeks))
	}
	if !fuFrom(rt).peeks[0].Start.Equal(b.RoundStartedAt) {
		t.Errorf("peek window start = %v, want the round's start %v", fuFrom(rt).peeks[0].Start, b.RoundStartedAt)
	}
	if fuFrom(rt).peeks[0].Mode != usage.ModeHeadless {
		t.Errorf("peek mode = %q, want headless for a headless builder", fuFrom(rt).peeks[0].Mode)
	}
}

// fuFrom digs the fakeUsage back out of the runtime the tests wired.

func fuFrom(rt Runtime) *fakeUsage { return rt.Usage.(*fakeUsage) }

func TestStatusNoLiveUsageWhenRoundClosed(t *testing.T) {
	rt, _ := seedClosedRound(t, "clean", 1)
	fu := &fakeUsage{peekSamples: []usage.Sample{{Provider: "p", Tokens: usage.Tokens{In: 1}, USD: 0.01, HasCost: true}}}
	rt.Usage = fu
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].LiveUsage != nil {
		t.Errorf("LiveUsage = %+v on a closed round, want nil", rep.Bindings[0].LiveUsage)
	}
	if len(fu.peeks) != 0 {
		t.Errorf("the reader was peeked %d times with no round open, want 0", len(fu.peeks))
	}
}

func TestStatusNoLiveUsageWhenPeekEmpty(t *testing.T) {
	rt, _ := statusLiveFixture(t, &fakeUsage{peekNote: "no usage events"})
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].LiveUsage != nil {
		t.Errorf("LiveUsage = %+v when Peek found nothing, want nil", rep.Bindings[0].LiveUsage)
	}
}

func TestStatusTextUsageRowPrefersLive(t *testing.T) {
	rep := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastUsage: &usage.Usage{Harness: "agy", Provider: "google", Model: "gemini-3-pro", DurationMS: 6 * 60_000,
			Cost: usage.Cost{Basis: usage.Unknown}, Note: "agy keeps no usage record"},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
			Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
			Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3},
	}}}
	text := RenderStatus(rep)
	lines := strings.Split(text, "\n")
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "  usage") {
			n++
			if !strings.HasPrefix(l, "  usage    live  ") {
				t.Errorf("usage row must be the live figure:\n%s", l)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d usage rows, want exactly one (the live one):\n%s", n, text)
	}
}

func TestStatusLiveUsageJSON(t *testing.T) {
	with := BindingStatus{LiveUsage: &usage.Usage{Harness: "agy", Cost: usage.Cost{USD: 0.04, Basis: usage.Measured}}}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"live_usage"`) {
		t.Errorf("json = %s, want the live_usage key", raw)
	}
	raw, _ = json.Marshal(BindingStatus{})
	if strings.Contains(string(raw), "live_usage") {
		t.Errorf("json = %s, live_usage must be absent when nil", raw)
	}
}

func TestRenderStatusOutcome(t *testing.T) {
	t.Run("prints outcome when halted", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
						Note: "noreport", Outcome: "halted",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1 (noreport) halted\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
	})

	t.Run("prints nothing for done outcome", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
						Outcome: "done",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
		if strings.Contains(text, "done") {
			t.Errorf("status text should not print 'done' outcome:\n%s", text)
		}
	})
}

// seedClosedRound is a binding past round 1 whose diff entry records how the
// round closed, so the status row's last_close and dirty columns can be read.
func seedClosedRound(t *testing.T, tree string, commits int) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t)
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindDiff, Confirmed: true,
		Commits: commits, Tree: tree,
	}); err != nil {
		t.Fatalf("AppendLog diff: %v", err)
	}
	b.Round = 2
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, b
}

// statusLiveFixture is a sent headless binding with a scripted usage reader
// and a clock 90s into the round, so the live-usage columns have something to
// show.
func statusLiveFixture(t *testing.T, fu *fakeUsage) (Runtime, store.Binding) {
	t.Helper()
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Usage = fu
	rt.Now = func() time.Time { return baseTime.Add(90 * time.Second) }
	return rt, b
}
func TestDoneStopsRelaying(t *testing.T) {
	rt, b := sentBinding(t)

	if _, err := Done(context.Background(), rt, b.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}
}

// TestDoneRefusesQueued pins #285: a served binding still queued (Owner set,
// QueuedAt non-zero) has no process to stop and nothing to hand back, so
// Done refuses it the same way the wire does, and state does not change.
//
// Mutation check: drop the `b.Owner != "" && !b.QueuedAt.IsZero()` guard
// from Done and this fails.

func TestDoneRefusesQueued(t *testing.T) {
	rt, b := sentBinding(t)
	b.Owner = "owner1"
	b.QueuedAt = rt.Now()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Done(context.Background(), rt, b.Name); err == nil {
		t.Fatal("Done: want an error refusing a queued round")
	} else if !strings.Contains(err.Error(), "is queued") || !strings.Contains(err.Error(), "relevo stop to drop it from the queue") {
		t.Fatalf("Done err = %q, want it to mention the round is queued and to relevo stop", err.Error())
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State == store.StateDone {
		t.Error("state must not become done")
	}
}

func TestDoneReleasesCleanWorktree(t *testing.T) {
	rt, b := sentBinding(t)
	fg := &fakeGit{dirtyResult: false}
	rt.Git = fg

	b.Worktree = t.TempDir()
	b.Branch = "relevo/webshop"
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeRemoved != b.Worktree {
		t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, b.Worktree)
	}
	if res.Branch != "relevo/webshop" {
		t.Errorf("Branch = %q, want relevo/webshop", res.Branch)
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
	}
	call := fg.removeWorktreeCalls[0]
	if call.Dir != b.CWD || call.Path != b.Worktree || call.Force != false {
		t.Errorf("RemoveWorktreeCall = %+v, want Dir=%q, Path=%q, Force=false", call, b.CWD, b.Worktree)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}
}

func TestDoneKeepsDirtyWorktree(t *testing.T) {
	rt, b := sentBinding(t)
	fg := &fakeGit{dirtyResult: true}
	rt.Git = fg

	b.Worktree = t.TempDir()
	b.Branch = "relevo/webshop"
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeKept != b.Worktree {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, b.Worktree)
	}
	if res.KeptReason != "uncommitted changes" {
		t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

// TestDoneKeepsRemoteWorktreeWhileRoundOpen pins the remote half of Done's
// worktree rule (#303 §3 trap): a remote binding has no local builder pane,
// so `!Headless() && round open` keeps the worktree exactly as a pane
// binding's open round did -- remote behaviour is unchanged by the deletion
// of pane builders.

func TestDoneNoWorktreeIsZeroResult(t *testing.T) {
	rt, b := sentBinding(t)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	want := DoneResult{Branch: b.Branch}
	if res != want {
		t.Errorf("res = %+v, want %+v", res, want)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
}

func TestDoneReportsGoneWorktree(t *testing.T) {
	rt, b := sentBinding(t)
	fg := &fakeGit{}
	rt.Git = fg

	b.Worktree = filepath.Join(t.TempDir(), "missing")
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeGone != b.Worktree {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, b.Worktree)
	}
}

func TestStatusRowBranch(t *testing.T) {
	rt, b := sentBinding(t)
	b.Branch = "relevo/webshop"
	row, err := statusRow(context.Background(), rt, b)
	if err != nil {
		t.Fatal(err)
	}
	if row.Branch != "relevo/webshop" {
		t.Errorf("Branch = %q", row.Branch)
	}
	b.Branch = ""
	row, _ = statusRow(context.Background(), rt, b)
	if row.Branch != "" {
		t.Errorf("--cwd binding Branch = %q, want empty", row.Branch)
	}
}

// TestStatusRowWaiting: a needs_you binding whose round has a captured
// question carries Waiting{Cause: "blocked", Hint: "relevo status ..."};
// an active binding carries nil.

func TestStatusRowWaiting(t *testing.T) {
	rt, b := sentBinding(t)
	b.State = store.StateNeedsYou
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	qPath := rt.Store.QuestionPath(b.Name, 2)
	if err := os.WriteFile(qPath, []byte("Do you want to proceed?"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: rt.Now().UTC(), Round: 2, Direction: store.DirToPlanner, Kind: store.KindQuestion,
		Path: qPath,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	row, err := statusRow(context.Background(), rt, b)
	if err != nil {
		t.Fatal(err)
	}
	if row.Waiting == nil || row.Waiting.Cause != "blocked" {
		t.Fatalf("Waiting = %+v, want cause blocked", row.Waiting)
	}
	if row.Waiting.Hint != "relevo status --name "+b.Name {
		t.Errorf("Hint = %q", row.Waiting.Hint)
	}
	b.State = store.StateActive
	row, _ = statusRow(context.Background(), rt, b)
	if row.Waiting != nil {
		t.Errorf("active row Waiting = %+v, want nil", row.Waiting)
	}
}

// TestStatusRowGatingShowsAge pins #132: a binding with GateRun set shows
// "gating <age>" as its BuilderStatus, overriding whatever the builder
// itself reports.

func TestStatusRowGatingShowsAge(t *testing.T) {
	rt, b := sentBinding(t)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)
	b.GateRun = &store.GateRun{
		PID: 4242, StartedAt: clock.Now().Add(-72 * time.Second).Unix(),
		Round: b.Round, Command: "make check",
	}

	row, err := statusRow(context.Background(), rt, b)
	if err != nil {
		t.Fatal(err)
	}
	if row.BuilderStatus != "gating 1m12s" {
		t.Errorf("BuilderStatus = %q, want %q", row.BuilderStatus, "gating 1m12s")
	}
}

func TestDisplayStatePaused(t *testing.T) {
	if got := displayState(store.StatePaused); got != "PAUSED" {
		t.Errorf("displayState(paused) = %q, want PAUSED", got)
	}
	rt := newRuntime(t)
	if err := rt.Store.Save(store.Binding{
		Name: "parked", CWD: "/repo-parked",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Kind: "agy"},
		Round:   3, State: store.StatePaused,
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	found := false
	for _, row := range rep.Bindings {
		if row.Name == "parked" {
			found = true
			if row.Display != "PAUSED" {
				t.Errorf("Display = %q, want PAUSED", row.Display)
			}
		}
	}
	if !found {
		t.Fatal("paused binding missing from status")
	}
}

// TestStatusLabelsStalledExploringStale pins #135's three labels on the status
// row: a stalled headless builder, an exploring headless one, and a stale
// NEEDS YOU one, plus the four structured JSON fields.

func TestStatusLabelsStalledExploringStale(t *testing.T) {
	rt := newRuntime(t)
	rt.Runner = newFakeRunner()
	rt.Runner.(*fakeRunner).script(48211, true)
	now := baseTime

	paneAt := now.Add(-17 * time.Minute)
	pane := store.Binding{
		Name: "pane", CWD: "/repo-pane", State: store.StateActive, Round: 4,
		RoundStartedAt: now.Add(-time.Hour),
		Planner:        store.Endpoint{Kind: "claude", PaneID: "w2:p3"},
		Builder: store.Endpoint{Kind: "agy", Mode: store.ModeHeadless, PID: 48211,
			StartedAt: now.Add(-time.Hour).Unix()},
		StalledSince: paneAt,
		Progress: &store.Progress{
			SampledAt: now, Tree: "tree-1", TreeAt: now.Add(-time.Hour),
			Output: "screen-1", OutputAt: paneAt,
		},
	}
	exploreAt := now.Add(-22 * time.Minute)
	headless := store.Binding{
		Name: "headless", CWD: "/repo-headless", State: store.StateActive, Round: 2,
		RoundStartedAt: now.Add(-time.Hour),
		Planner:        store.Endpoint{Kind: "claude", PaneID: "w2:p3"},
		Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless, PID: 48211,
			StartedAt: now.Add(-time.Hour).Unix()},
		ExploringSince: exploreAt,
	}
	staleAt := now.Add(-4*time.Hour - 10*time.Minute)
	stale := store.Binding{
		Name: "stale", CWD: "/repo-stale", State: store.StateNeedsYou, Round: 1,
		RoundStartedAt: now.Add(-time.Hour),
		Planner:        store.Endpoint{Kind: "claude", PaneID: "w2:p3"},
		Builder:        store.Endpoint{Kind: "agy", PaneID: "w2:p9"},
		StaleSince:     staleAt,
	}
	for _, b := range []store.Binding{pane, headless, stale} {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rep.Bindings), rep.Bindings)
	}
	rows := map[string]BindingStatus{}
	for _, r := range rep.Bindings {
		rows[r.Name] = r
	}

	wantStall := "stalled " + AgeText(now.Sub(paneAt))
	if got := rows["pane"].BuilderStatus; got != wantStall {
		t.Errorf("pane BuilderStatus = %q, want %q", got, wantStall)
	}
	if got := rows["pane"].Stall; got != wantStall {
		t.Errorf("pane Stall = %q, want %q", got, wantStall)
	}
	if !rows["pane"].LastProgressAt.Equal(paneAt) {
		t.Errorf("pane LastProgressAt = %s, want %s", rows["pane"].LastProgressAt, paneAt)
	}

	wantExplore := "exploring " + AgeText(now.Sub(exploreAt))
	if got := rows["headless"].BuilderStatus; got != wantExplore {
		t.Errorf("headless BuilderStatus = %q, want %q", got, wantExplore)
	}
	if got := rows["headless"].Exploring; got != wantExplore {
		t.Errorf("headless Exploring = %q, want %q", got, wantExplore)
	}
	if got := rows["headless"].Stall; got != "" {
		t.Errorf("headless Stall = %q, want empty", got)
	}

	wantStale := "stale " + AgeText(now.Sub(staleAt))
	if got := rows["stale"].Stale; got != wantStale {
		t.Errorf("stale Stale = %q, want %q", got, wantStale)
	}
	if got := rows["stale"].BuilderStatus; got == "" {
		t.Error("stale BuilderStatus is empty, want the absent word for a missing pane")
	}

	// The structured fields, per row: each label's own key is present, and a
	// sampled row carries when its signals last moved.
	assertHasKey := func(name, key string) {
		t.Helper()
		raw, err := json.Marshal(rows[name])
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s row JSON is missing %q: %s", name, key, raw)
		}
	}
	assertHasKey("pane", "stall")
	assertHasKey("pane", "last_progress_at")
	assertHasKey("headless", "exploring")
	assertHasKey("stale", "stale")
	assertHasKey("stale", "last_progress_at")
}

// TestStatusShowsLandedPR pins #136's status rule: a binding landed since
// its last send reads "landed" -- "landed pr <url>" when a PR was created --
// on the round line, and carries the same fact as landed_at/landed_pr for a
// statusline consumer.

func TestStatusShowsLandedPR(t *testing.T) {
	rt, _ := seedBound(t)
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.LandedAt = baseTime
	b.LandedPR = "https://github.com/o/r/pull/7"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d rows, want 1", len(rep.Bindings))
	}
	row := rep.Bindings[0]
	if want := "landed pr https://github.com/o/r/pull/7"; row.Landed != want {
		t.Errorf("Landed = %q, want %q", row.Landed, want)
	}
	if !row.LandedAt.Equal(baseTime) || row.LandedPR != "https://github.com/o/r/pull/7" {
		t.Errorf("LandedAt/LandedPR = %v/%q, want %v/%q",
			row.LandedAt, row.LandedPR, baseTime, "https://github.com/o/r/pull/7")
	}

	// The rendered round line carries it, so the terminal never disagrees
	// with the JSON.
	if text := RenderStatus(rep); !strings.Contains(text, "landed pr https://github.com/o/r/pull/7") {
		t.Errorf("RenderStatus did not print the land state:\n%s", text)
	}

	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{`"landed"`, `"landed_at"`, `"landed_pr"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("status JSON is missing %s: %s", key, raw)
		}
	}
}

// TestStatusUnlandedRowSaysNothing: a binding that was never landed has an
// empty "landed" word, so the round line is exactly what it always was.

func TestStatusUnlandedRowSaysNothing(t *testing.T) {
	rt, _ := seedBound(t)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Landed; got != "" {
		t.Errorf("Landed = %q, want \"\" on a binding that was never landed", got)
	}
	if text := RenderStatus(rep); strings.Contains(text, "landed") {
		t.Errorf("RenderStatus must not mention landing:\n%s", text)
	}
}

// TestSendClearsLanded pins #136's clearing rule: a new round moves the
// branch again, so the last land stops describing it.

func TestSendClearsLanded(t *testing.T) {
	rt, _ := seedBound(t)
	rt.Git = &fakeGit{snapshotTreeID: "tree-1"}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.LandedAt = baseTime
	b.LandedPR = "https://github.com/o/r/pull/7"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "# again"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !after.LandedAt.IsZero() {
		t.Errorf("LandedAt = %v, want zero after a new round", after.LandedAt)
	}
	if after.LandedPR != "" {
		t.Errorf("LandedPR = %q, want \"\" after a new round", after.LandedPR)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].Landed != "" {
		t.Errorf("status still says %q after a new round", rep.Bindings[0].Landed)
	}
}

// TestStatusRowsAreAttentionOrdered pins #143: Status (and its JSON) now
// return rows in the same attention-first order ui has always used --
// NEEDS YOU, HELD, ACTIVE, PAUSED, DONE -- instead of plain name order.
//
// Mutation check: restore `sort.Slice(rows, func(i, j int) bool { return
// rows[i].Name < rows[j].Name })` in buildReport in place of `SortRows(rows,
// true)` and this fails, because "a" (ACTIVE) would then sort before "b"
// (NEEDS YOU).

func TestStatusRowsAreAttentionOrdered(t *testing.T) {
	rt := newRuntime(t)
	a := store.Binding{Name: "a", CWD: "/repo-a", State: store.StateActive, Round: 1}
	b := store.Binding{Name: "b", CWD: "/repo-b", State: store.StateNeedsYou, Round: 1}
	c := store.Binding{Name: "c", CWD: "/repo-c", State: store.StateDone, Round: 1}
	for _, binding := range []store.Binding{a, b, c} {
		if err := rt.Store.Save(binding); err != nil {
			t.Fatalf("Save %s: %v", binding.Name, err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var gotNames []string
	for _, row := range rep.Bindings {
		gotNames = append(gotNames, row.Name)
	}
	want := []string{"b", "a", "c"}
	if len(gotNames) != len(want) {
		t.Fatalf("got %v, want %v", gotNames, want)
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("Status order = %v, want %v", gotNames, want)
		}
	}

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	ia, ib, ic := strings.Index(string(data), `"name":"a"`), strings.Index(string(data), `"name":"b"`), strings.Index(string(data), `"name":"c"`)
	if !(ib < ia && ia < ic) {
		t.Errorf("status --json order wrong: b@%d a@%d c@%d", ib, ia, ic)
	}
}

// TestStatusLiveDiffWhileRoundOpen pins #143's live diff figure: a --cwd
// binding (no worktree of its own) is marked Shared, a binding whose round
// is not open gets no Live at all, and RenderStatus prints the figure.

func TestStatusLiveDiffWhileRoundOpen(t *testing.T) {
	rt := newRuntime(t)
	fg := &fakeGit{worktreeStat: git.Stat{FilesChanged: 6, Insertions: 120, Deletions: 30}}
	rt.Git = fg

	open := store.Binding{
		Name: "open", CWD: "/repo-open", State: store.StateActive, Round: 2,
		RoundStartedAt:    baseTime,
		RoundBaselineTree: "tree-open",
		Worktree:          "/wt-open",
	}
	shared := store.Binding{
		Name: "shared", CWD: "/repo-shared", State: store.StateActive, Round: 2,
		RoundStartedAt:    baseTime,
		RoundBaselineTree: "tree-shared",
		// Worktree left empty: a --cwd binding shares the planner's own tree.
	}
	closedRound := store.Binding{
		Name: "closed", CWD: "/repo-closed", State: store.StateActive, Round: 2,
		RoundBaselineTree: "tree-closed",
		Worktree:          "/wt-closed",
		// RoundStartedAt left zero: no round is open.
	}
	for _, binding := range []store.Binding{open, shared, closedRound} {
		if err := rt.Store.Save(binding); err != nil {
			t.Fatalf("Save %s: %v", binding.Name, err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rows := map[string]BindingStatus{}
	for _, r := range rep.Bindings {
		rows[r.Name] = r
	}

	want := LiveDiff{Files: 6, Added: 120, Removed: 30}
	if got := rows["open"].Live; got == nil || *got != want {
		t.Errorf("open Live = %+v, want %+v", got, want)
	}
	if got := rows["shared"].Live; got == nil || !got.Shared {
		t.Errorf("shared Live = %+v, want Shared true", got)
	}
	if got := rows["closed"].Live; got != nil {
		t.Errorf("closed Live = %+v, want nil (round not open)", got)
	}

	text := RenderStatus(rep)
	if !strings.Contains(text, "+120/-30 in 6") {
		t.Errorf("RenderStatus missing live diff line:\n%s", text)
	}
}

// TestStatusLiveDiffCached pins #143's 5s per-binding cache: two Status
// calls within the window make one DiffWorktreeStat call; once the fake
// clock has moved past the window, a third call makes a second.

func TestStatusLiveDiffCached(t *testing.T) {
	rt := newRuntime(t)
	now := baseTime
	rt.Now = func() time.Time { return now }
	fg := &fakeGit{worktreeStat: git.Stat{FilesChanged: 2, Insertions: 10, Deletions: 3}}
	rt.Git = fg

	b := store.Binding{
		Name: "cached", CWD: "/repo-cached", State: store.StateActive, Round: 2,
		RoundStartedAt:    now,
		RoundBaselineTree: "tree-cached",
		Worktree:          "/wt-cached",
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status 1: %v", err)
	}
	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status 2: %v", err)
	}
	if fg.worktreeStatCalls != 1 {
		t.Fatalf("worktreeStatCalls after two calls within 5s = %d, want 1", fg.worktreeStatCalls)
	}

	now = now.Add(6 * time.Second)
	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status 3: %v", err)
	}
	if fg.worktreeStatCalls != 2 {
		t.Fatalf("worktreeStatCalls after 6s = %d, want 2", fg.worktreeStatCalls)
	}
}

// TestStatusQuietForOnActive pins #143's quiet age: only an ACTIVE row with
// an open, sampled round gets one; a NEEDS YOU row with the same progress
// data gets none.

func TestStatusQuietForOnActive(t *testing.T) {
	rt := newRuntime(t)
	now := baseTime
	progressAt := now.Add(-12 * time.Second)

	active := store.Binding{
		Name: "quiet-active", CWD: "/repo-active", State: store.StateActive, Round: 2,
		RoundStartedAt: now.Add(-time.Hour),
		Progress: &store.Progress{
			SampledAt: now, Tree: "t1", TreeAt: progressAt,
			Output: "o1", OutputAt: progressAt,
		},
	}
	needsYou := store.Binding{
		Name: "quiet-needsyou", CWD: "/repo-ny", State: store.StateNeedsYou, Round: 1,
		RoundStartedAt: now.Add(-time.Hour),
		Progress: &store.Progress{
			SampledAt: now, Tree: "t2", TreeAt: progressAt,
			Output: "o2", OutputAt: progressAt,
		},
	}
	for _, binding := range []store.Binding{active, needsYou} {
		if err := rt.Store.Save(binding); err != nil {
			t.Fatalf("Save %s: %v", binding.Name, err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rows := map[string]BindingStatus{}
	for _, r := range rep.Bindings {
		rows[r.Name] = r
	}

	want := AgeText(now.Sub(progressAt))
	if got := rows["quiet-active"].QuietFor; got != want {
		t.Errorf("QuietFor = %q, want %q", got, want)
	}
	if got := rows["quiet-needsyou"].QuietFor; got != "" {
		t.Errorf("NEEDS YOU QuietFor = %q, want empty", got)
	}
}

// TestStatusUnreadUntilViewed pins #143's unread marker: a report entry
// with no .viewed stamp reads Unread; MarkViewed clears it; a newer report
// sets it again.

func TestStatusUnreadUntilViewed(t *testing.T) {
	rt := newRuntime(t)
	b := store.Binding{Name: "unread", CWD: "/repo-unread", State: store.StateNeedsYou, Round: 2}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog 1: %v", err)
	}

	unreadOf := func(rep Report) bool {
		for _, r := range rep.Bindings {
			if r.Name == "unread" {
				return r.Unread
			}
		}
		t.Fatal("unread binding missing from status")
		return false
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status 1: %v", err)
	}
	if !unreadOf(rep) {
		t.Fatal("Unread = false before any view, want true")
	}

	if err := rt.Store.MarkViewed(b.Name, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}
	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status 2: %v", err)
	}
	if unreadOf(rep) {
		t.Fatal("Unread = true after MarkViewed, want false")
	}

	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: baseTime.Add(2 * time.Minute), Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}
	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status 3: %v", err)
	}
	if !unreadOf(rep) {
		t.Fatal("Unread = false after a newer report, want true")
	}
}

func TestStatusDetailsBrokenBinding(t *testing.T) {
	rt, b := sentBinding(t)
	b.State = store.StateBroken
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The builder is gone; only the planner is live.

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]

	if got.Display != "NEEDS YOU" {
		t.Errorf("display = %q, want NEEDS YOU (the collapse must not change)", got.Display)
	}
	want := DiagnoseBuilder(b).Detail(b.Round)
	if got.Detail != want {
		t.Errorf("detail =\n  %q\nwant\n  %q", got.Detail, want)
	}
	// sentBinding names the builder, so it is identified; no moved-pane warning.
	if strings.Contains(got.Detail, "moved pane") {
		t.Errorf("detail must not warn about a moved pane when named, got %q", got.Detail)
	}

	// Sibling case: an adopted pane (nameless and session-less) still gets the warning.
	b.Builder.AgentName = ""
	b.Builder.SessionID = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	repAdopted, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	gotAdopted := repAdopted.Bindings[0]
	wantAdopted := DiagnoseBuilder(b).Detail(b.Round)
	if gotAdopted.Detail != wantAdopted {
		t.Errorf("adopted detail =\n  %q\nwant\n  %q", gotAdopted.Detail, wantAdopted)
	}
	// #303 deleted the pane, so the adopted-builder detail no longer warns
	// about a moved pane; the row now says the work is unaccounted for, which
	// is the same warning in the words that survive.
	if !strings.Contains(gotAdopted.Detail, "unaccounted for") {
		t.Errorf("adopted detail must warn that the work is unaccounted for, got %q", gotAdopted.Detail)
	}
}

func TestStatusOmitsDetailForHealthyBinding(t *testing.T) {
	rt, _ := sentBinding(t)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Detail != "" {
		t.Errorf("detail = %q, want empty for a healthy binding", got.Detail)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "detail") {
		t.Errorf("detail must be omitempty, got %s", raw)
	}
}

// orphaned also collapses into NEEDS YOU but is not overloaded, so it gets no
// detail. This pins the scope decision.

func TestStatusCountsOnlyRunningConsults(t *testing.T) {
	rt, _ := seedBound(t)

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Consults = []store.Consult{
		{ID: "aaaaaaaa", Role: "reviewer", State: store.ConsultRunning},
		{ID: "bbbbbbbb", Role: "reviewer", State: store.ConsultRunning},
		{ID: "cccccccc", Role: "reviewer", State: store.ConsultDone},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings", len(rep.Bindings))
	}
	if rep.Bindings[0].Consults != 2 {
		t.Errorf("Consults = %d, want 2 running (the done one awaits reap)", rep.Bindings[0].Consults)
	}
}

func TestStatusPopulatesGated(t *testing.T) {
	rt, _ := seedBound(t)

	if _, err := Unavailable(rt, testAgyRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Gated) == 0 {
		t.Fatalf("rep.Gated = %+v, want non-empty", rep.Gated)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"gated"`) {
		t.Errorf("expected \"gated\" key present, got %s", raw)
	}

	rawEmpty, err := json.Marshal(Report{})
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if strings.Contains(string(rawEmpty), "gated") {
		t.Errorf("empty report must omit gated, got %s", rawEmpty)
	}
}

// TestBindingStatusKey: a planner row keys by Name; a server row keys by
// owner/name, so two clients' same-named bindings never collide.

func TestStatusLastPayloadSkipsBookkeepingKinds(t *testing.T) {
	rt, b := sentBinding(t)

	// Log now reads: plan (from sentBinding), then drift.
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindDrift,
	}); err != nil {
		t.Fatalf("AppendLog drift: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0]
	if got.Last == nil || got.Last.Kind != store.KindDrift {
		t.Fatalf("Last = %+v, want KindDrift", got.Last)
	}
	if got.LastPayload == nil || got.LastPayload.Kind != store.KindPlan {
		t.Fatalf("LastPayload = %+v, want KindPlan", got.LastPayload)
	}

	// Log now reads: plan, drift, diff, report (with a note).
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindDiff,
	}); err != nil {
		t.Fatalf("AppendLog diff: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToPlanner, Kind: store.KindReport, Note: "unmarked",
	}); err != nil {
		t.Fatalf("AppendLog report: %v", err)
	}

	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got = rep.Bindings[0]
	if got.Last == nil || got.Last.Kind != store.KindReport {
		t.Fatalf("Last = %+v, want KindReport", got.Last)
	}
	if got.LastPayload == nil || got.LastPayload.Kind != store.KindReport {
		t.Fatalf("LastPayload = %+v, want KindReport", got.LastPayload)
	}
	if got.LastPayload.Note != "unmarked" {
		t.Errorf("LastPayload.Note = %q, want %q", got.LastPayload.Note, "unmarked")
	}

	// A fresh binding with no Send has no log entries at all.
	rt2, _ := seedBound(t)

	rep2, err := Status(context.Background(), rt2)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// NOTE (deviation from the plan text): the plan says this binding's Last
	// should also be nil. It is not: seedBound's Bind call passes an explicit
	// Candidate, and create() (bind.go) unconditionally logs a KindPick entry
	// for HowExplicit resolutions, regardless of Send. That logging is
	// existing, out-of-scope behaviour this plan does not touch. LastPayload
	// is unaffected -- KindPick is not a payload kind -- so the assertion
	// that matters to this plan (LastPayload nil on an unsent binding) still
	// holds and is checked below.
	got2 := rep2.Bindings[0]
	if got2.Last == nil || got2.Last.Kind != store.KindPick {
		t.Errorf("Last = %+v, want the KindPick entry Bind logs for an explicit candidate", got2.Last)
	}
	if got2.LastPayload != nil {
		t.Errorf("LastPayload = %+v, want nil", got2.LastPayload)
	}
}

func TestStatusRemoteRow(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Builder.RemoteStatus = "running"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	got := rep.Bindings[0]
	// #303 deleted BuilderPane: the server a remote binding is served by is
	// what the row shows now, and RenderStatus prints it below.
	if got.BuilderStatus != "running" {
		t.Fatalf("BuilderStatus = %q, want the recorded RemoteStatus", got.BuilderStatus)
	}

	// The row shows the remote builder and its status; the server name is no
	// longer part of the row (#303 deleted the pane/server column data).
	text := RenderStatus(rep)
	if !strings.Contains(text, "remote") || !strings.Contains(text, "running") {
		t.Errorf("rendered status must show the remote builder and its status, got %q", text)
	}
}

// TestStatusRemoteRowUnknownStatus checks the "" -> "unknown" fallback for a
// remote binding the daemon has never ticked.

func TestStatusRemoteRowUnknownStatus(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].BuilderStatus; got != "unknown" {
		t.Fatalf("BuilderStatus with no recorded RemoteStatus = %q, want unknown", got)
	}
}

// TestStatusRowQueuedText pins #285's queued text: RemoteQueue facts render
// "queued (3/3 busy on contabo, 2 ahead, 4m)", and a queued row with no
// facts yet (part 1's tolerance) falls back to the bare "queued".
//
// Mutation check: drop queueText's format string (or the RemoteQueue
// branch in statusRow) and this test fails.
func TestStatusJSONForkProvenance(t *testing.T) {
	rt, b := sentBinding(t)

	// 1. Ordinary binding: forked_from and forked_at_round must be omitted from JSON
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	humanBefore := RenderStatus(rep)

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	row := decoded["bindings"].([]any)[0].(map[string]any)
	if _, ok := row["forked_from"]; ok {
		t.Errorf("ordinary binding must omit forked_from: %+v", row)
	}
	if _, ok := row["forked_at_round"]; ok {
		t.Errorf("ordinary binding must omit forked_at_round: %+v", row)
	}

	// 2. Forked binding: forked_from and forked_at_round must be present in JSON
	b.ForkedFrom = "source"
	b.ForkedAtRound = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	repFork, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status fork: %v", err)
	}
	humanAfter := RenderStatus(repFork)

	// RenderStatus human output must be byte-identical
	if humanBefore != humanAfter {
		t.Errorf("RenderStatus human output changed for fork:\nbefore:\n%s\nafter:\n%s", humanBefore, humanAfter)
	}

	rawFork, err := json.Marshal(repFork)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decodedFork map[string]any
	if err := json.Unmarshal(rawFork, &decodedFork); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	rowFork := decodedFork["bindings"].([]any)[0].(map[string]any)
	if got, ok := rowFork["forked_from"].(string); !ok || got != "source" {
		t.Errorf("forked_from = %v, want 'source'", rowFork["forked_from"])
	}
	if got, ok := rowFork["forked_at_round"].(float64); !ok || got != 2 {
		t.Errorf("forked_at_round = %v, want 2", rowFork["forked_at_round"])
	}
}

func TestStatusLastCloseAndDirty(t *testing.T) {
	t.Run("dirty close, next round not sent", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "dirty", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Round != 1 || got.LastClose.Tree != "dirty" || got.LastClose.Commits != 0 {
			t.Fatalf("LastClose = %+v, want round 1, dirty, 0 commits", got.LastClose)
		}
		if !got.Dirty {
			t.Error("Dirty = false, want true")
		}
	})

	t.Run("clean close", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "clean", 3)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "clean" || got.LastClose.Commits != 3 {
			t.Fatalf("LastClose = %+v, want clean, 3 commits", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true, want false")
		}
	})

	t.Run("dirty close but the next round is sent", func(t *testing.T) {
		rt, b := seedClosedRound(t, "dirty", 0)
		b.RoundStartedAt = time.Now().UTC()
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "dirty" {
			t.Fatalf("LastClose = %+v, want dirty", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true while a round is running, want false")
		}
	})

	t.Run("pre-field diff entry", func(t *testing.T) {
		rt, _ := seedClosedRound(t, "", 0)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := rep.Bindings[0]
		if got.LastClose == nil || got.LastClose.Tree != "" {
			t.Fatalf("LastClose = %+v, want present with unknown tree", got.LastClose)
		}
		if got.Dirty {
			t.Error("Dirty = true for an unknown tree, want false")
		}
	})

	t.Run("no diff entry", func(t *testing.T) {
		rt, _ := sentBinding(t)
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got := rep.Bindings[0]; got.LastClose != nil || got.Dirty {
			t.Errorf("LastClose = %+v, Dirty = %v; want nil, false", got.LastClose, got.Dirty)
		}
	})
}

func TestStatusReportsLastSeq(t *testing.T) {
	rt := newRuntime(t)
	withLog := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}
	without := store.Binding{Name: "empty", CWD: "/repo2", Round: 1, State: store.StateActive}
	for _, b := range []store.Binding{withLog, without} {
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := rt.Store.AppendLog(withLog.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	byName := map[string]BindingStatus{}
	for _, row := range rep.Bindings {
		byName[row.Name] = row
	}
	if got := byName[withLog.Name].LastSeq; got != 3 {
		t.Errorf("webshop LastSeq = %d, want 3", got)
	}
	if got := byName[without.Name].LastSeq; got != 0 {
		t.Errorf("empty LastSeq = %d, want 0", got)
	}

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"last_seq":3`) {
		t.Errorf("status JSON does not carry last_seq 3: %s", data)
	}
}

// TestDisplayStatePaused: a paused binding shows as PAUSED, after ACTIVE and
// before DONE.

func TestStatusRowQueuedText(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("contabo")
	b.Builder.RemoteStatus = "queued"
	b.Builder.RemoteQueue = &store.QueueFacts{
		Position: 3, Ahead: 2, Running: 3, Cap: 3, Since: baseTime.Add(-4 * time.Minute),
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := "queued (3/3 busy on contabo, 2 ahead, 4m)"
	if got := rep.Bindings[0].BuilderStatus; got != want {
		t.Fatalf("BuilderStatus = %q, want %q", got, want)
	}

	// Nil facts (accepted but not yet reported) fall back to the bare word.
	st2 := store.New(t.TempDir())
	b2 := remoteBinding("contabo")
	b2.Builder.RemoteStatus = "queued"
	if err := st2.Save(b2); err != nil {
		t.Fatal(err)
	}
	rt2 := Runtime{Store: st2, Now: func() time.Time { return baseTime }}
	rep2, err := Status(context.Background(), rt2)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep2.Bindings[0].BuilderStatus; got != "queued" {
		t.Fatalf("BuilderStatus with no facts = %q, want %q", got, "queued")
	}
}

// TestStatusShowsStopping pins #138: a stop in flight shows
// "stopping <elapsed> of <grace>" as the builder's status, overriding
// whatever the builder itself reports, and carries the stop bookkeeping as
// data for the statusline consumer.
// TestStatusHidesStoppingAfterGrace pins the fix for the "stopping" label
// surviving an abandoned stop: once the grace has elapsed and the binding
// went NEEDS YOU (stopDecision no longer says stopWait), the NEEDS YOU line
// already says why, so BuilderStatus must not still read
// "stopping <elapsed> of <grace>".
// TestStatusJSONCarriesStructuredFields guards the statusline interface: Last
// and Pending must serialise as JSON objects with typed fields, not as
// rendered prose the consumer would have to regex apart.
func TestStatusJSONCarriesStructuredFields(t *testing.T) {
	rt, b := queuedBinding(t)
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	bindings, ok := decoded["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("bindings = %#v", decoded["bindings"])
	}
	row, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("row is not a JSON object: %#v", bindings[0])
	}

	pending, ok := row["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending must be a JSON object, not prose, got %#v", row["pending"])
	}
	if _, ok := pending["round"].(float64); !ok {
		t.Errorf("pending.round missing or not numeric: %#v", pending)
	}
	if _, ok := pending["kind"].(string); !ok {
		t.Errorf("pending.kind missing or not a string: %#v", pending)
	}

	last, ok := row["last"].(map[string]any)
	if !ok {
		t.Fatalf("last must be a JSON object, not prose, got %#v", row["last"])
	}
	if _, ok := last["round"].(float64); !ok {
		t.Errorf("last.round missing or not numeric: %#v", last)
	}
	if _, ok := last["direction"].(string); !ok {
		t.Errorf("last.direction missing or not a string: %#v", last)
	}
}

// TestStatusRowBranch: the worktree branch reaches the row; a --cwd
// binding (no branch) leaves it empty.

func TestRoundFacts(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Minute)
	t2 := t0.Add(2 * time.Minute)
	t3 := t0.Add(3 * time.Minute)
	u1 := &usage.Usage{Tokens: usage.Tokens{In: 1000}}

	tests := []struct {
		name      string
		entries   []store.LogEntry
		wantStart time.Time
		wantEnd   time.Time
		wantUsage *usage.Usage
	}{
		{
			name:      "empty log",
			entries:   nil,
			wantStart: time.Time{},
			wantEnd:   time.Time{},
			wantUsage: nil,
		},
		{
			name: "plan r1",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			},
			wantStart: t0,
			wantEnd:   time.Time{},
			wantUsage: nil,
		},
		{
			name: "plan r1, report r1 with usage",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 1, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: u1},
			},
			wantStart: t0,
			wantEnd:   t1,
			wantUsage: u1,
		},
		{
			name: "plan r1, report r1 (usage U1), plan r2",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 1, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: u1},
				{TS: t2, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
			},
			wantStart: t2,
			wantEnd:   time.Time{},
			wantUsage: nil,
		},
		{
			name: "plan r2, later plan r2 (nudge or switch), report r2",
			entries: []store.LogEntry{
				{TS: t0, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t2, Round: 2, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: u1},
			},
			wantStart: t0,
			wantEnd:   t2,
			wantUsage: u1,
		},
		{
			name: "plan r1, report r1 (U1), plan r2, report r2 with nil usage",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 1, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: u1},
				{TS: t2, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t3, Round: 2, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: nil},
			},
			wantStart: t2,
			wantEnd:   t3,
			wantUsage: nil,
		},
		{
			name: "plan r1, findings entry with usage, question and answer entries",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 1, Kind: store.KindFindings, Direction: store.DirToPlanner, Usage: u1},
				{TS: t2, Round: 1, Kind: store.KindQuestion, Direction: store.DirToPlanner},
				{TS: t3, Round: 1, Kind: store.KindAnswer, Direction: store.DirToBuilder},
			},
			wantStart: t0,
			wantEnd:   time.Time{},
			wantUsage: nil,
		},
		{
			name: "plan r1, plan r2, late report r1 with usage",
			entries: []store.LogEntry{
				{TS: t0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t1, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
				{TS: t2, Round: 1, Kind: store.KindReport, Direction: store.DirToPlanner, Usage: u1},
			},
			wantStart: t1,
			wantEnd:   time.Time{},
			wantUsage: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd, gotUsage := roundFacts(tt.entries)
			if !gotStart.Equal(tt.wantStart) {
				t.Errorf("start = %v, want %v", gotStart, tt.wantStart)
			}
			if !gotEnd.Equal(tt.wantEnd) {
				t.Errorf("end = %v, want %v", gotEnd, tt.wantEnd)
			}
			if tt.wantUsage == nil {
				if gotUsage != nil {
					t.Errorf("usage = %+v, want nil", gotUsage)
				}
			} else {
				if gotUsage == nil {
					t.Fatalf("usage is nil, want %+v", tt.wantUsage)
				}
				if *gotUsage != *tt.wantUsage {
					t.Errorf("usage = %+v, want %+v", gotUsage, tt.wantUsage)
				}
				for _, e := range tt.entries {
					if e.Usage != nil && gotUsage == e.Usage {
						t.Errorf("gotUsage must be a fresh copy, but shares pointer with log entry")
					}
				}
			}
		})
	}
}
