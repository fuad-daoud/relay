package db

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

// pair identifies a round by (binding name, round number), the shape every
// per-Filter-field test asserts an exact set of.
type pair struct {
	binding string
	number  int
}

func pairsOf(rows []RoundRow) []pair {
	out := make([]pair, len(rows))
	for i, r := range rows {
		out[i] = pair{binding: r.BindingName, number: r.Number}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].binding != out[j].binding {
			return out[i].binding < out[j].binding
		}
		return out[i].number < out[j].number
	})
	return out
}

func assertPairs(t *testing.T, got []RoundRow, want []pair) {
	t.Helper()
	gotPairs := pairsOf(got)
	sort.Slice(want, func(i, j int) bool {
		if want[i].binding != want[j].binding {
			return want[i].binding < want[j].binding
		}
		return want[i].number < want[j].number
	})
	if !reflect.DeepEqual(gotPairs, want) {
		t.Errorf("got %v, want %v", gotPairs, want)
	}
}

// seed builds: two repos (A has origin+common_dir; B has origin only),
// three bindings (webshop, api on repo A -- api archived; docs on repo B),
// one planner per binding, and six rounds spanning harnesses agy/opencode,
// outcomes reported/halted/open, one gated round, two cost bases, dates one
// day apart.
type seeded struct {
	d          *DB
	repoAID    string
	repoBID    string
	webshopID  string
	apiID      string
	docsID     string
	day1, day2 time.Time
}

func seedDB(t *testing.T) seeded {
	t.Helper()
	d := openTestDB(t)

	day1 := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)

	repoAID, err := d.UpsertRepo(Repo{
		OriginURL: ptr("https://example.test/a.git"),
		CommonDir: ptr("/home/x/a/.git"),
		FirstSeen: day1,
	})
	if err != nil {
		t.Fatalf("UpsertRepo A: %v", err)
	}
	repoBID, err := d.UpsertRepo(Repo{OriginURL: ptr("https://example.test/b.git"), FirstSeen: day1})
	if err != nil {
		t.Fatalf("UpsertRepo B: %v", err)
	}

	plannerID, err := d.UpsertPlanner(Planner{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: day1, LastSeen: day2})
	if err != nil {
		t.Fatalf("UpsertPlanner: %v", err)
	}

	webshopID, err := d.UpsertBinding(Binding{
		Name: "webshop", RepoID: &repoAID, PlannerID: &plannerID, Feature: ptr("checkout"),
		CWD: "/home/x/webshop", BuilderMode: "headless", CreatedAt: day1, IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding webshop: %v", err)
	}

	apiID, err := d.UpsertBinding(Binding{
		Name: "api", RepoID: &repoAID, PlannerID: &plannerID,
		CWD: "/home/x/api", BuilderMode: "headless", CreatedAt: day1,
		ArchivedAt: ptr(day2), ArchivePath: ptr("/archive/api.tar.gz"), IngestSource: IngestArchive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding api: %v", err)
	}

	docsID, err := d.UpsertBinding(Binding{
		Name: "docs", RepoID: &repoBID, PlannerID: &plannerID, FinalState: ptr("needs_you"),
		CWD: "/home/x/docs", BuilderMode: "pane", CreatedAt: day1, IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding docs: %v", err)
	}

	rounds := []Round{
		{
			BindingID: webshopID, Number: 1, StartedAt: day1, Outcome: OutcomeReported,
			BuilderCandidate: ptr("claude/anthropic/sonnet"), BuilderHarness: ptr("agy"),
			BuilderProvider: ptr("anthropic"), BuilderModel: ptr("sonnet"),
			CostBasis: ptr("exact"), CostUSD: ptr(1.5),
			Switches: 2,
		},
		{
			BindingID: webshopID, Number: 2, StartedAt: day2, Outcome: OutcomeOpen,
			BuilderCandidate: ptr("claude/anthropic/sonnet"), BuilderHarness: ptr("agy"),
			BuilderProvider: ptr("anthropic"), BuilderModel: ptr("sonnet"),
		},
		{
			BindingID: apiID, Number: 1, StartedAt: day1, Outcome: OutcomeHalted,
			BuilderCandidate: ptr("opencode/openrouter/glm"), BuilderHarness: ptr("opencode"),
			BuilderProvider: ptr("openrouter"), BuilderModel: ptr("glm"),
			GateResult: ptr("fail"), CostBasis: ptr("estimated"), CostUSD: ptr(0.2),
		},
		{
			BindingID: apiID, Number: 2, StartedAt: day2, Outcome: OutcomeReported,
			BuilderCandidate: ptr("opencode/openrouter/glm"), BuilderHarness: ptr("opencode"),
			BuilderProvider: ptr("openrouter"), BuilderModel: ptr("glm"),
			GateResult: ptr("pass"),
		},
		{
			BindingID: docsID, Number: 1, StartedAt: day1, Outcome: OutcomeReported,
			BuilderCandidate: ptr("claude/anthropic/sonnet"), BuilderHarness: ptr("agy"),
			BuilderProvider: ptr("anthropic"), BuilderModel: ptr("sonnet"), ReportOutcome: ptr("done"),
		},
		{
			BindingID: docsID, Number: 2, StartedAt: day2, Outcome: OutcomeHalted,
			BuilderCandidate: ptr("claude/anthropic/sonnet"), BuilderHarness: ptr("agy"),
			BuilderProvider: ptr("anthropic"), BuilderModel: ptr("sonnet"), ReportOutcome: ptr("halted"),
		},
	}
	for _, r := range rounds {
		if _, err := d.UpsertRound(r); err != nil {
			t.Fatalf("UpsertRound %+v: %v", r, err)
		}
	}

	return seeded{d: d, repoAID: repoAID, repoBID: repoBID, webshopID: webshopID, apiID: apiID, docsID: docsID, day1: day1, day2: day2}
}

func TestQueryNoFilterReturnsAllNewestFirst(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Newest: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d rows, want 6", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].StartedAt.Before(got[i].StartedAt) {
			t.Errorf("row %d (started %v) is before row %d (started %v); want newest first", i-1, got[i-1].StartedAt, i, got[i].StartedAt)
		}
	}
}

func TestQueryByRepoOrigin(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Repo: "https://example.test/a.git"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}, {"webshop", 2}, {"api", 1}, {"api", 2}})
}

func TestQueryByRepoCommonDir(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Repo: "/home/x/a/.git"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}, {"webshop", 2}, {"api", 1}, {"api", 2}})
}

func TestQueryByFeature(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Feature: "checkout"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}, {"webshop", 2}})
}

func TestQueryByBinding(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Binding: "docs"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"docs", 1}, {"docs", 2}})
}

func TestQueryByPlanner(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Planner: "sess-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d rows, want 6", len(got))
	}
}

func TestQueryByHarness(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Harness: "opencode"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"api", 2}})
}

func TestQueryByProvider(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Provider: "openrouter"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"api", 2}})
}

func TestQueryByModel(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Model: "glm"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"api", 2}})
}

func TestQueryByCandidate(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Candidate: "opencode/openrouter/glm"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"api", 2}})
}

func TestQueryByOutcome(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Outcome: OutcomeHalted})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"docs", 2}})
}

func TestQueryByReportOutcome(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{ReportOutcome: "halted"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"docs", 2}})
}

func TestQueryByState(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{State: "needs_you"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"docs", 1}, {"docs", 2}})
}

func TestQueryByGateResult(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{GateResult: "fail"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}})
}

func TestQueryByCostBasis(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{CostBasis: "exact"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}})
}

func TestQueryByRound(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Round: 2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 2}, {"api", 2}, {"docs", 2}})
}

// TestQueryReturnsSwitches pins §4.1: RoundRow carries the round's stored
// switch count.
func TestQueryReturnsSwitches(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Binding: "webshop", Round: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Switches != 2 {
		t.Errorf("Switches = %d, want 2", got[0].Switches)
	}
}

func TestQuerySince(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Since: s.day2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 2}, {"api", 2}, {"docs", 2}})
}

func TestQueryUntil(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Until: s.day2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}, {"api", 1}, {"docs", 1}})
}

func TestQueryArchivedTrue(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Archived: ptr(true)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"api", 1}, {"api", 2}})
}

func TestQueryArchivedFalse(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Archived: ptr(false)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	assertPairs(t, got, []pair{{"webshop", 1}, {"webshop", 2}, {"docs", 1}, {"docs", 2}})
}

func TestQueryLimit(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Query(Filter{Limit: 2, Newest: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
}

func TestQueryHereUnresolvedIsInvalid(t *testing.T) {
	s := seedDB(t)
	_, err := s.d.Query(Filter{Here: "/some/cwd"})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// TestQueryRowCarriesTokensDurationAndMode pins the RoundRow columns the
// dashboard's sums need: the four token counters, the duration computed in
// Go from started_at and closed_at, report_outcome, the round's
// builder_mode, and the binding's server. A round with no closed_at has a
// nil DurationMS.
func TestQueryRowCarriesTokensDurationAndMode(t *testing.T) {
	d := openTestDB(t)

	server := "contabo"
	bindingID, err := d.UpsertBinding(Binding{
		Name: "remote-run", CWD: "/home/x/remote", BuilderMode: "headless",
		Server: &server, CreatedAt: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC),
		IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	started := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	closed := started.Add(27 * time.Minute)
	in, cache, write, out := int64(1000), int64(2000), int64(3000), int64(4000)
	mode, report := "remote", "done"
	if _, err := d.UpsertRound(Round{
		BindingID: bindingID, Number: 1, StartedAt: started, ClosedAt: &closed,
		Outcome: OutcomeReported, InTokens: &in, CacheTokens: &cache,
		WriteTokens: &write, OutTokens: &out, BuilderMode: &mode, ReportOutcome: &report,
	}); err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}
	if _, err := d.UpsertRound(Round{
		BindingID: bindingID, Number: 2, StartedAt: started.Add(24 * time.Hour),
		Outcome: OutcomeOpen,
	}); err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}

	rows, err := d.Query(Filter{Newest: false})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	got := rows[0]
	if got.Number != 1 {
		t.Fatalf("rows[0].Number = %d, want 1 (oldest first)", got.Number)
	}
	for _, c := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"InTokens", got.InTokens, 1000},
		{"CacheTokens", got.CacheTokens, 2000},
		{"WriteTokens", got.WriteTokens, 3000},
		{"OutTokens", got.OutTokens, 4000},
		{"DurationMS", got.DurationMS, 27 * 60 * 1000},
	} {
		switch {
		case c.got == nil:
			t.Errorf("%s = nil, want %d", c.name, c.want)
		case *c.got != c.want:
			t.Errorf("%s = %d, want %d", c.name, *c.got, c.want)
		}
	}
	if got.ReportOutcome == nil || *got.ReportOutcome != "done" {
		t.Errorf("ReportOutcome = %v, want done", got.ReportOutcome)
	}
	if got.BuilderMode == nil || *got.BuilderMode != "remote" {
		t.Errorf("BuilderMode = %v, want remote", got.BuilderMode)
	}
	if got.Server == nil || *got.Server != "contabo" {
		t.Errorf("Server = %v, want contabo", got.Server)
	}

	open := rows[1]
	if open.Number != 2 {
		t.Fatalf("rows[1].Number = %d, want 2", open.Number)
	}
	if open.ClosedAt != nil || open.DurationMS != nil {
		t.Errorf("open round: ClosedAt = %v, DurationMS = %v; want both nil", open.ClosedAt, open.DurationMS)
	}
	if open.InTokens != nil || open.ReportOutcome != nil || open.BuilderMode != nil {
		t.Errorf("open round: InTokens = %v, ReportOutcome = %v, BuilderMode = %v; want all nil",
			open.InTokens, open.ReportOutcome, open.BuilderMode)
	}
}

func TestBindingsNewestActivityFirst(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Bindings(Filter{})
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].LastActivity.Before(got[i].LastActivity) {
			t.Errorf("row %d (activity %v) is before row %d (activity %v); want newest first",
				i-1, got[i-1].LastActivity, i, got[i].LastActivity)
		}
	}
	for _, br := range got {
		if br.Rounds != 2 {
			t.Errorf("binding %s has %d rounds, want 2", br.Name, br.Rounds)
		}
	}
}

func TestBindingNewestByName(t *testing.T) {
	s := seedDB(t)
	d := s.d

	dup, err := d.UpsertBinding(Binding{
		Name: "webshop", CWD: "/home/x/webshop2", BuilderMode: "headless",
		CreatedAt: s.day2.Add(24 * time.Hour), IngestSource: IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding dup: %v", err)
	}

	br, ok, err := d.Binding("webshop")
	if err != nil {
		t.Fatalf("Binding: %v", err)
	}
	if !ok {
		t.Fatal("Binding not found")
	}
	if br.ID != dup {
		t.Errorf("Binding returned id %q, want the newest %q", br.ID, dup)
	}
}

func TestRoundsAscending(t *testing.T) {
	s := seedDB(t)
	got, err := s.d.Rounds(s.webshopID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rounds, want 2", len(got))
	}
	if got[0].Number != 1 || got[1].Number != 2 {
		t.Errorf("round numbers = [%d %d], want [1 2]", got[0].Number, got[1].Number)
	}
}

func TestArtifactMissingIsFalse(t *testing.T) {
	s := seedDB(t)
	_, ok, err := s.d.Artifact("nonexistent-round", ArtifactPlan)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if ok {
		t.Error("Artifact found for a nonexistent round, want not found")
	}
}

func TestTranscriptPaging(t *testing.T) {
	d := openTestDB(t)
	var recs []TranscriptRecord
	for i := 0; i < 5; i++ {
		recs = append(recs, TranscriptRecord{Seq: i, RecordJSON: "{}", Rendered: "line"})
	}
	if _, err := d.AppendTranscript(OwnerPlanner, "sess-1", recs); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}

	got, err := d.Transcript(OwnerPlanner, "sess-1", 2, 2)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 3 {
		t.Errorf("seqs = [%d %d], want [2 3]", got[0].Seq, got[1].Seq)
	}
}

func TestEventsRoundZeroIsAll(t *testing.T) {
	d := openTestDB(t)
	bindingID, err := d.UpsertBinding(newTestBinding("webshop", time.Now()))
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	round1ID, err := d.UpsertRound(newTestRound(bindingID, 1, OutcomeReported))
	if err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}
	round2ID, err := d.UpsertRound(newTestRound(bindingID, 2, OutcomeOpen))
	if err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}

	evs := []Event{
		{BindingID: bindingID, RoundID: &round1ID, Seq: 1, TS: time.Now(), Kind: "send", Direction: "planner_to_builder", EntryJSON: "{}"},
		{BindingID: bindingID, RoundID: &round2ID, Seq: 2, TS: time.Now(), Kind: "send", Direction: "planner_to_builder", EntryJSON: "{}"},
	}
	if _, err := d.AppendEvents(bindingID, evs); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	all, err := d.Events(bindingID, 0)
	if err != nil {
		t.Fatalf("Events(0): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Events(0) got %d, want 2", len(all))
	}

	round1Only, err := d.Events(bindingID, 1)
	if err != nil {
		t.Fatalf("Events(1): %v", err)
	}
	if len(round1Only) != 1 || round1Only[0].Seq != 1 {
		t.Fatalf("Events(1) = %+v, want one event with seq 1", round1Only)
	}
}

func TestStatsCounts(t *testing.T) {
	s := seedDB(t)
	stats, err := s.d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if want := embeddedVersion(t); stats.Version != want {
		t.Errorf("Version = %d, want %d", stats.Version, want)
	}
	if stats.Rows["binding"] != 3 {
		t.Errorf("Rows[binding] = %d, want 3", stats.Rows["binding"])
	}
	if stats.Rows["round"] != 6 {
		t.Errorf("Rows[round] = %d, want 6", stats.Rows["round"])
	}
	if stats.Rows["repo"] != 2 {
		t.Errorf("Rows[repo] = %d, want 2", stats.Rows["repo"])
	}
	if stats.NewestRound == nil || !stats.NewestRound.Equal(s.day2) {
		t.Errorf("NewestRound = %v, want %v", stats.NewestRound, s.day2)
	}
	if stats.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", stats.SizeBytes)
	}
}

func TestRecentEvents(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().Truncate(time.Millisecond)

	binding1ID, err := d.UpsertBinding(newTestBinding("atlas", now.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("UpsertBinding 1: %v", err)
	}
	binding2ID, err := d.UpsertBinding(newTestBinding("webshop", now.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("UpsertBinding 2: %v", err)
	}

	// Reported round with tokens
	r1 := newTestRound(binding1ID, 1, OutcomeReported)
	r1.StartedAt = now.Add(-30 * time.Minute)
	ct := now.Add(-20 * time.Minute)
	r1.ClosedAt = &ct
	in := int64(100)
	cache := int64(200)
	write := int64(300)
	out := int64(400)
	r1.InTokens = &in
	r1.CacheTokens = &cache
	r1.WriteTokens = &write
	r1.OutTokens = &out
	round1ID, err := d.UpsertRound(r1)
	if err != nil {
		t.Fatalf("UpsertRound 1: %v", err)
	}

	// Open round without tokens
	r2 := newTestRound(binding2ID, 2, OutcomeOpen)
	r2.StartedAt = now.Add(-10 * time.Minute)
	round2ID, err := d.UpsertRound(r2)
	if err != nil {
		t.Fatalf("UpsertRound 2: %v", err)
	}

	since := now.Add(-15 * time.Minute)

	evs1 := []Event{
		// Event 1: before since (atlas, round 1)
		{BindingID: binding1ID, RoundID: &round1ID, Seq: 1, TS: now.Add(-20 * time.Minute), Kind: "plan", Direction: "planner_to_builder", EntryJSON: "{}"},
		// Event 2: after since (atlas, round 1)
		{BindingID: binding1ID, RoundID: &round1ID, Seq: 2, TS: now.Add(-10 * time.Minute), Kind: "report", Direction: "builder_to_planner", EntryJSON: `{"outcome":"done"}`},
	}
	if _, err := d.AppendEvents(binding1ID, evs1); err != nil {
		t.Fatalf("AppendEvents 1: %v", err)
	}

	evs2 := []Event{
		// Event 3: after since (webshop, round 2)
		{BindingID: binding2ID, RoundID: &round2ID, Seq: 1, TS: now.Add(-5 * time.Minute), Kind: "plan", Direction: "planner_to_builder", EntryJSON: "{}"},
		// Event 4: after since (webshop, round 2)
		{BindingID: binding2ID, RoundID: &round2ID, Seq: 2, TS: now.Add(-2 * time.Minute), Kind: "switch", Direction: "system", EntryJSON: "{}"},
	}
	if _, err := d.AppendEvents(binding2ID, evs2); err != nil {
		t.Fatalf("AppendEvents 2: %v", err)
	}

	// Read since
	got, err := d.RecentEvents(since, 0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}

	// Assert only the three since `since` come back, newest first
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}

	// Newest first: Event 4 (-2m, webshop), Event 3 (-5m, webshop), Event 2 (-10m, atlas)
	if got[0].Kind != "switch" || got[0].BindingName != "webshop" {
		t.Errorf("got[0] = %+v, want switch on webshop", got[0])
	}
	if got[1].Kind != "plan" || got[1].BindingName != "webshop" {
		t.Errorf("got[1] = %+v, want plan on webshop", got[1])
	}
	if got[2].Kind != "report" || got[2].BindingName != "atlas" {
		t.Errorf("got[2] = %+v, want report on atlas", got[2])
	}

	// Binding names are correct
	// Round is set
	if got[0].Round == nil || *got[0].Round != 2 {
		t.Errorf("got[0].Round = %v, want 2", got[0].Round)
	}
	if got[2].Round == nil || *got[2].Round != 1 {
		t.Errorf("got[2].Round = %v, want 1", got[2].Round)
	}

	// Tokens is the sum for round 1 (100+200+300+400 = 1000)
	if got[2].Tokens == nil || *got[2].Tokens != 1000 {
		t.Errorf("got[2].Tokens = %v, want 1000", got[2].Tokens)
	}

	// DurationMS is non-nil for round 1 (10 minutes = 600,000 ms)
	if got[2].DurationMS == nil || *got[2].DurationMS != 600_000 {
		t.Errorf("got[2].DurationMS = %v, want 600000", got[2].DurationMS)
	}

	// DurationMS is nil for the open round
	if got[0].DurationMS != nil {
		t.Errorf("got[0].DurationMS = %v, want nil for open round", got[0].DurationMS)
	}
	if got[1].DurationMS != nil {
		t.Errorf("got[1].DurationMS = %v, want nil for open round", got[1].DurationMS)
	}

	// Limit caps the result
	limited, err := d.RecentEvents(since, 2)
	if err != nil {
		t.Fatalf("RecentEvents(limit 2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("len(limited) = %d, want 2", len(limited))
	}
}
