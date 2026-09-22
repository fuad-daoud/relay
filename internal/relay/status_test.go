package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// TestStatusRemoteRow checks that a remote binding's status row names the
// server as its pane and shows the last RemoteStatus the daemon recorded
// (#100 step 3), and that RenderStatus prints both.
// TestStatusRemoteRowUnknownStatus checks the "" -> "unknown" fallback for a
// remote binding the daemon has never ticked.
// TestStatusRowQueuedText pins #285's queued text: RemoteQueue facts render
// "queued (3/3 busy on contabo, 2 ahead, 4m)", and a queued row with no
// facts yet (part 1's tolerance) falls back to the bare "queued".
//
// Mutation check: drop queueText's format string (or the RemoteQueue
// branch in statusRow) and this test fails.
// TestStatusDegradesWhenHerdrUnreachable pins the degrade contract (#, spec
// §7.4): a failed ListAgents must not fail the report. The report carries
// every row, carries the herdr error as data, and marks every pane endpoint
// it could not look up as unknown -- not gone, because relay did not ask
// and must not claim absence.
// TestStatusHerdrErrorEmptyOnSuccess pins that an *answered* lookup -- even
// an answered empty agent list -- leaves HerdrError empty and the absent
// word gone, not unknown.
// TestRenderStatusHerdrHeader pins the `relay status` header: a report that
// carried a herdr error opens with the unreachable line, then the ordinary
// body; a report that did not renders byte-identical to today.
// heldStatusBinding saves a HELD binding whose planner clock started 23s
// before the runtime's fixed clock, with the grace the test supplies.
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
// TestStatusRowBranch: the worktree branch reaches the row; a --cwd
// binding (no branch) leaves it empty.
// TestStatusRowWaiting: a needs_you binding whose round has a captured
// question carries Waiting{Cause: "blocked", Hint: "relay answer ..."};
// an active binding carries nil.
// TestStatusRowGatingShowsAge pins #132: a binding with GateRun set shows
// "gating <age>" as its BuilderStatus, overriding whatever the builder
// itself reports.
// TestDoneRefusesQueued pins #285: a served binding still queued (Owner set,
// QueuedAt non-zero) has no process to stop and nothing to hand back, so
// Done refuses it the same way the wire does, and state does not change.
//
// Mutation check: drop the `b.Owner != "" && !b.QueuedAt.IsZero()` guard
// from Done and this fails.
// TestDoneKeepsRemoteWorktreeWhileRoundOpen pins the remote half of Done's
// worktree rule (#303 §3 trap): a remote binding has no local builder pane,
// so `!Headless() && round open` keeps the worktree exactly as a pane
// binding's open round did -- remote behaviour is unchanged by the deletion
// of pane builders.
func TestDoneKeepsRemoteWorktreeWhileRoundOpen(t *testing.T) {
	st := store.New(t.TempDir())
	fg := &fakeGit{}
	b := remoteBinding("contabo")
	b.Worktree = t.TempDir()
	b.Branch = "relay/api"
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

// orphaned also collapses into NEEDS YOU but is not overloaded, so it gets no
// detail. This pins the scope decision.
func TestRenderStatusOmitsForeignLineWhenNone(t *testing.T) {
	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
	}}})
	if strings.Contains(out, "  foreign ") {
		t.Errorf("unexpected foreign line:\n%s", out)
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

func TestRenderStatusFooterOnlyWhenEverythingIsDone(t *testing.T) {
	r := Report{
		Bindings:   nil,
		DoneHidden: 2,
	}
	out := RenderStatus(r)
	if !strings.Contains(out, "2 done · relay gc to clear") {
		t.Errorf("RenderStatus output missing footer:\n%s", out)
	}
	if strings.Contains(out, "no bindings") {
		t.Errorf("RenderStatus output should not contain 'no bindings':\n%s", out)
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

// TestBindingStatusKey: a planner row keys by Name; a server row keys by
// owner/name, so two clients' same-named bindings never collide.
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

// TestStatusLastPayloadSkipsBookkeepingKinds pins that LastPayload walks the
// log for the last plan/report/question/answer entry, skipping relay's own
// bookkeeping kinds (drift, diff, ...) that Last does not skip.
// seedClosedRound appends a diff entry for round 1 with the given facts and
// returns the binding as the daemon leaves it after queueReport: round 2,
// not yet sent (RoundStartedAt zero).
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

// statusLiveFixture is a headless binding with an open round (round 1
// sent) and a live reader wired, with now moved past the round's start so
// the live figure has a duration.
// fuFrom digs the fakeUsage back out of the runtime the tests wired.
func fuFrom(rt Runtime) *fakeUsage { return rt.Usage.(*fakeUsage) }

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

// TestDisplayStatePaused: a paused binding shows as PAUSED, after ACTIVE and
// before DONE.
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
// TestStatusUnlandedRowSaysNothing: a binding that was never landed has an
// empty "landed" word, so the round line is exactly what it always was.
// TestSendClearsLanded pins #136's clearing rule: a new round moves the
// branch again, so the last land stops describing it.
// TestStatusRowsAreAttentionOrdered pins #143: Status (and its JSON) now
// return rows in the same attention-first order ui has always used --
// NEEDS YOU, HELD, ACTIVE, PAUSED, DONE -- instead of plain name order.
//
// Mutation check: restore `sort.Slice(rows, func(i, j int) bool { return
// rows[i].Name < rows[j].Name })` in buildReport in place of `SortRows(rows,
// true)` and this fails, because "a" (ACTIVE) would then sort before "b"
// (NEEDS YOU).
// TestStatusLiveDiffWhileRoundOpen pins #143's live diff figure: a --cwd
// binding (no worktree of its own) is marked Shared, a binding whose round
// is not open gets no Live at all, and RenderStatus prints the figure.
// TestStatusLiveDiffCached pins #143's 5s per-binding cache: two Status
// calls within the window make one DiffWorktreeStat call; once the fake
// clock has moved past the window, a third call makes a second.
// TestStatusQuietForOnActive pins #143's quiet age: only an ACTIVE row with
// an open, sampled round gets one; a NEEDS YOU row with the same progress
// data gets none.
// TestStatusUnreadUntilViewed pins #143's unread marker: a report entry
// with no .viewed stamp reads Unread; MarkViewed clears it; a newer report
// sets it again.
