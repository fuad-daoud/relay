package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

func newRuntime(t *testing.T, f *fakeHerdr) Runtime {
	t.Helper()
	return Runtime{
		Herdr:            f,
		Store:            store.New(t.TempDir()),
		Candidates:       candidateSet(t, testCandidatesJSON),
		LedgerPath:       filepath.Join(t.TempDir(), "ledger.json"),
		AvailabilityPath: filepath.Join(t.TempDir(), "availability.json"),
		Now:              func() time.Time { return baseTime },
		// Every local builder is headless since #303, so every Send needs a
		// Runner. A test that wants "no runner" sets rt.Runner = nil.
		Runner: newFakeRunner(),
	}
}

func plannerAgent() herdr.Agent {
	return herdr.Agent{
		Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
		PaneID: "w2:p3", Session: herdr.Session{Value: "planner-sess"},
	}
}

// TestBindRecordsRepoFeatureAndLocator pins #172: a fresh bind captures the
// git repo identity (normalised), the human-given --feature label, and the
// planner's own transcript file path (via rt.Sessions), and stamps CreatedAt.
func TestBindRecordsRepoFeatureAndLocator(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Git = &fakeGit{repoFactsOrigin: "git@github.com:o/r.git", repoFactsCommonDir: "/repo/.git"}
	rt.Sessions = func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "planner-sess" {
			return "/home/x/.claude/projects/slug/S.jsonl", true
		}
		return "", false
	}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Feature: "auth",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	wantRepoRef := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(b.RepoRef, wantRepoRef) {
		t.Errorf("RepoRef = %+v, want %+v", b.RepoRef, wantRepoRef)
	}
	if b.Feature != "auth" {
		t.Errorf("Feature = %q, want auth", b.Feature)
	}
	if b.Planner.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("Planner.TranscriptLocator = %q, want the resolved session path", b.Planner.TranscriptLocator)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want set")
	}
}

// TestBindRepoFactsFailureIsNil pins that a git failure never fails a bind:
// captureRepo swallows it and RepoRef stays nil.
func TestBindRepoFactsFailureIsNil(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Git = &fakeGit{repoFactsErr: errors.New("boom")}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.RepoRef != nil {
		t.Errorf("RepoRef = %+v, want nil when RepoFacts errors", b.RepoRef)
	}
}

// testPlannerRegistry seeds a registry holding rec and returns it with the
// record as the registry stamped it (created_at and seen_at filled in).
func testPlannerRegistry(t *testing.T, rec planner.Record) (*planner.FileRegistry, planner.Record) {
	t.Helper()
	reg := &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}
	created, err := reg.Create(rec)
	if err != nil {
		t.Fatalf("create planner record: %v", err)
	}
	return reg, created
}

// TestBindRecordsPlannerFromRegistry is the plan's required case (§3.2,
// §5.3): with a registry configured, Binding.PlannerID, Planner.Kind and
// Planner.SessionID come from the record -- not from the herdr agent list --
// while Planner.PaneID comes from the pane env the caller passes.
func TestBindRecordsPlannerFromRegistry(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w2:p9")

	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	reg, rec := testPlannerRegistry(t, planner.Record{
		ID:                "pl_aaaaaaaabbbb",
		Name:              "architect-1",
		HarnessKind:       "claude",
		SessionID:         "sess-from-record",
		CWD:               "/repo",
		TranscriptLocator: "/home/x/.claude/projects/slug/S.jsonl",
	})
	rt.Planners = reg

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testOpencodeRef,
		PlannerID:   rec.ID,
		PlannerPane: os.Getenv("HERDR_PANE_ID"),
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if b.PlannerID != rec.ID {
		t.Errorf("PlannerID = %q, want the record's %q", b.PlannerID, rec.ID)
	}
	if b.Planner.Kind != "claude" {
		t.Errorf("Planner.Kind = %q, want the record's claude", b.Planner.Kind)
	}
	if b.Planner.SessionID != "sess-from-record" {
		t.Errorf("Planner.SessionID = %q, want the record's sess-from-record", b.Planner.SessionID)
	}
	if b.Planner.PaneID != "w2:p9" {
		t.Errorf("Planner.PaneID = %q, want the pane env's w2:p9", b.Planner.PaneID)
	}
	if b.Planner.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("Planner.TranscriptLocator = %q, want the record's", b.Planner.TranscriptLocator)
	}
}

// TestBindNoPlannerIsHardError is the plan's required case for §4.3: with a
// registry configured and nothing resolving -- no --planner, no
// $RELAY_PLANNER, no host and no detectable session -- a verb fails with
// exactly the CLI's no-planner line. The old "no planner pane" error is gone.
func TestBindNoPlannerIsHardError(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w2:p3")
	t.Setenv("RELAY_PLANNER", "")
	t.Setenv("CLAUDECODE", "")

	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Planners = &planner.FileRegistry{Root: t.TempDir(), Now: func() time.Time { return baseTime }}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("Bind with no resolvable planner must fail")
	}
	const want = `no relay planner for this session. Run "relay planner init" once here, or enable the relay plugin (relay doctor).`
	if err.Error() != want {
		t.Errorf("err = %q, want exactly %q", err.Error(), want)
	}
}

// TestBindRejectsBadFeature pins that a bad --feature is refused before
// anything is spawned, with the same error store.ValidFeature reports.
func TestBindRejectsBadFeature(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Feature: "a/b",
	})
	if err == nil || !strings.Contains(err.Error(), "feature:") {
		t.Fatalf("Bind err = %v, want one containing %q", err, "feature:")
	}
	if len(f.starts) != 0 {
		t.Errorf("a rejected feature must spawn no builder, got %d starts", len(f.starts))
	}
}

// TestBindRefusesABuilderNameHerdrWouldRefuse pins #64: a 25-character binding
// name passes the store's own limit but builds a 33-character agent name, and
// Bind must refuse it before any pane is split, any agent started or any
// binding saved.
func TestBindRefusesABuilderNameHerdrWouldRefuse(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if len(f.starts) != 0 || len(f.tabs) != 0 {
		t.Errorf("a refused name must touch no pane: tabs = %d, starts = %d", len(f.tabs), len(f.starts))
	}
	if !errors.Is(err, herdr.ErrInvalidAgentName) {
		t.Fatalf("Bind err = %v, want one wrapping herdr.ErrInvalidAgentName", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

// slicesContains reports whether want is one of args.
func slicesContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// launchArgs renders the headless launch for b's builder candidate and tier,
// the way Send would, so a test can assert the argv a bind resolved to.
func launchArgs(t *testing.T, rt Runtime, b store.Binding, tier harness.Tier) []string {
	t.Helper()
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		t.Fatalf("builder candidate %q: %v", b.BuilderCandidate, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		t.Fatalf("lookup %q: %v", ref, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := headlessLaunch(c, role, tier, 0, "", b.CWD, rt.Store.Dir(b.Name))
	if err != nil {
		t.Fatalf("headlessLaunch: %v", err)
	}
	return argv
}

func TestBindRefusesACandidateThatDoesNotServeBuilder(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"test","model":"m","roles":["reviewer"]}]`)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	// Also assert no pane was SPLIT. Checking only f.starts cannot tell a
	// refusal that happened before anything was created from one that ran after
	// builderPane and left a pane behind with nothing pointing at it -- the
	// ~800 MB leak CLAUDE.md warns about. This is what pins the refusal's
	// placement ahead of builderPane.
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindSpawnUnknownAliasFails(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/test/nope", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Fatalf("got %v, want ErrUnknownCandidate", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("an unknown alias must not start an agent, got %+v", f.starts)
	}
}

func TestBindResolvesTheOnlyBuilderCandidate(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--x"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !b.Builder.Headless() || b.Builder.Kind != "agy" {
		t.Errorf("Builder = %+v, want a headless agy endpoint", b.Builder)
	}
	argv := launchArgs(t, rt, b, harness.TierHarness)
	for _, want := range []string{"--model", "m", "--agent", "plan-executor", "--x"} {
		if !containsArg(argv, want, "") && !slicesContains(argv, want) {
			t.Errorf("launch args = %v, want them to carry %q", argv, want)
		}
	}
	if b.BuilderCandidate != "agy/test/m" {
		t.Errorf("BuilderCandidate = %q, want agy/test/m", b.BuilderCandidate)
	}
}

func TestBindRefusesAnAmbiguousCandidate(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("started %d agents; a refused bind must spawn nothing", len(f.starts))
	}
	if len(f.tabs) != 0 {
		t.Errorf("created %d tabs; a refused bind must not create a pane it then abandons", len(f.tabs))
	}
}

func TestBindWithNoCandidatesSaysSo(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, "[]")

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("want ErrNoCandidates, got %v", err)
	}
}

func TestResumeWithoutABuilderDoesNotSpawn(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/test/m", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("seed Bind: %v", err)
	}
	f.starts = nil

	_, err = Bind(context.Background(), rt, BindOptions{
		Name: b.Name, Resume: true, Candidate: "", PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume without candidate must not spawn, got starts = %+v", f.starts)
	}
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	opts := BindOptions{Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo"}
	if _, err := Bind(context.Background(), rt, opts); err != nil {
		t.Fatalf("first Bind: %v", err)
	}

	opts.Name = "webshop2"
	_, err := Bind(context.Background(), rt, opts)
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestBindResumeRepointsPlannerAndKeepsRound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	b.Round = 5
	b.State = store.StateOrphaned
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	f.agents = []herdr.Agent{{
		Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
		PaneID: "w7:pB", Session: herdr.Session{Value: "planner-sess-2"},
	}}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.Round != 5 {
		t.Errorf("resume must keep the round, got %d", got.Round)
	}
	if got.Planner.SessionID != "planner-sess-2" || got.Planner.PaneID != "w7:pB" {
		t.Errorf("resume must repoint the planner, got %+v", got.Planner)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

// TestResumeKeepsFieldsAndRefreshesLocator pins the resume rule for #172's
// new fields: a planner-only resume leaves RepoRef and Feature exactly as
// the binding already had them, and refreshes Planner.TranscriptLocator
// (which endpointOf wipes along with the rest of the old Planner endpoint)
// since it was previously empty.
func TestResumeKeepsFieldsAndRefreshesLocator(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{AgentName: "webshop-builder", PaneID: "w1:p2", Kind: "opencode"},
		RepoRef: &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"},
		Feature: "auth",
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	rt.Sessions = func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "planner-sess" {
			return "/home/x/.claude/projects/slug/S.jsonl", true
		}
		return "", false
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}

	wantRepoRef := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(got.RepoRef, wantRepoRef) {
		t.Errorf("RepoRef = %+v, want kept as %+v", got.RepoRef, wantRepoRef)
	}
	if got.Feature != "auth" {
		t.Errorf("Feature = %q, want kept as auth", got.Feature)
	}
	if got.Planner.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("Planner.TranscriptLocator = %q, want refreshed to the resolved session path", got.Planner.TranscriptLocator)
	}
}

// TestResumeKeepsExistingLocatorWhenAlreadySet is the other half of the
// refresh rule: when the binding already has a TranscriptLocator, resume
// must not overwrite it with whatever rt.Sessions resolves for the new
// planner pane's session.
func TestResumeKeepsExistingLocatorWhenAlreadySet(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   3,
		State:   store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1", TranscriptLocator: "/already/set.jsonl"},
		Builder: store.Endpoint{AgentName: "webshop-builder", PaneID: "w1:p2", Kind: "opencode"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	rt.Sessions = func(kind, sessionID string) (string, bool) {
		return "/would/overwrite.jsonl", true
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.Planner.TranscriptLocator != "/already/set.jsonl" {
		t.Errorf("Planner.TranscriptLocator = %q, want the existing value kept, not re-resolved", got.Planner.TranscriptLocator)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"webshop":    "webshop",
		"money/ai":   "money-ai",
		"My.Repo":    "my-repo",
		"2024-thing": "b2024-thing",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBindRefusesExistingName is the regression test for a silently broken
// second session: Save only rewrites bind.json, so the previous session's
// log.jsonl and NNN-*.md files survive and a fresh round 1 collides with the
// old round 1. Reconcile then reads the old report entry as "already handled"
// and the binding stalls with no error and no notification.
func TestBindRefusesExistingName(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil {
		t.Fatal("binding an existing name must be refused")
	}

	// The refusal has to happen before anything is spawned, or it strands a
	// live builder pane with nothing pointing at it.
	if len(f.starts) != 0 {
		t.Errorf("no agent may be started, got %+v", f.starts)
	}
	if len(f.tabs) != 0 {
		t.Errorf("no tab may be created, got %d", len(f.tabs))
	}

	if !strings.Contains(err.Error(), "relay unbind webshop") {
		t.Errorf("error must name the unbind exit, got %q", err)
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Errorf("error must name the resume exit, got %q", err)
	}

	// The existing binding must be untouched by the refusal.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 4 || got.Builder.PaneID != "w1:p2" {
		t.Errorf("refused bind must not rewrite the binding, got %+v", got)
	}
}

// TestBindResumeStillAdoptsAnExistingName guards the exit the refusal offers.
func TestBindResumeStillAdoptsAnExistingName(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateOrphaned,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume must still adopt an existing binding: %v", err)
	}
	if got.Round != 4 || got.Planner.PaneID != "w2:p3" || got.State != store.StateActive {
		t.Errorf("resume = %+v", got)
	}
	if len(f.starts) != 0 {
		t.Errorf("resume must not start an agent, got %+v", f.starts)
	}
}

func TestBindRebindWithGoneBuilder(t *testing.T) {
	existing := store.Binding{
		Name:              "webshop",
		CWD:               "/repo",
		Round:             5,
		RoundBaselineTree: "tree-abc",
		State:             store.StateBroken,
		HaltNotifiedRound: 5,
		Halt:              "round 5 has run past 24h0m0s",
		HaltAt:            baseTime,
		BuilderScreen:     "some terminal output",
		BuilderScreenAt:   baseTime,
		Planner:           store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:           store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderCandidate:  testOpencodeRef,
	}

	t.Run("spawn replacement builder", func(t *testing.T) {
		f := &fakeHerdr{
			agents: []herdr.Agent{
				plannerAgent(),
				{Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
			},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind: %v", err)
		}

		if !got.Builder.Headless() || got.Builder.Kind != "opencode" {
			t.Errorf("Builder = %+v, want a headless opencode endpoint", got.Builder)
		}
		if got.BuilderCandidate != testOpencodeRef {
			t.Errorf("BuilderCandidate = %q, want %s", got.BuilderCandidate, testOpencodeRef)
		}
		if got.BuilderScreen != "" {
			t.Errorf("BuilderScreen = %q, want empty", got.BuilderScreen)
		}
		if !got.BuilderScreenAt.IsZero() {
			t.Errorf("BuilderScreenAt = %v, want zero", got.BuilderScreenAt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
		}
		if got.Halt != "" {
			t.Errorf("Halt = %q, want empty", got.Halt)
		}
		if !got.HaltAt.IsZero() {
			t.Errorf("HaltAt = %v, want zero", got.HaltAt)
		}
		if got.State != store.StateActive {
			t.Errorf("State = %s, want active", got.State)
		}
		if got.Round != 5 {
			t.Errorf("Round = %d, want 5 (untouched)", got.Round)
		}
		if got.CWD != "/repo" {
			t.Errorf("CWD = %q, want /repo (untouched)", got.CWD)
		}
		if got.RoundBaselineTree != "tree-abc" {
			t.Errorf("RoundBaselineTree = %q, want tree-abc (untouched)", got.RoundBaselineTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !saved.Builder.Headless() ||
			saved.BuilderScreen != "" || !saved.BuilderScreenAt.IsZero() ||
			saved.HaltNotifiedRound != 0 || saved.Halt != "" || !saved.HaltAt.IsZero() ||
			saved.Round != 5 || saved.RoundBaselineTree != "tree-abc" {
			t.Errorf("saved binding does not reflect rebind updates: %+v", saved)
		}
	})

}

func TestBindResumeDoneBindingScope(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateDone,
		Planner: store.Endpoint{PaneID: "w1:p1"},
		Builder: store.Endpoint{PaneID: "w1:p2", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	t.Run("planner-only resume of DONE binding succeeds", func(t *testing.T) {
		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("planner-only resume on done binding must succeed: %v", err)
		}
		if got.State != store.StateActive {
			t.Errorf("state = %s, want active", got.State)
		}
		if got.Planner.PaneID != "w2:p3" {
			t.Errorf("Planner.PaneID = %q, want w2:p3", got.Planner.PaneID)
		}
		if got.Builder != existing.Builder {
			t.Errorf("Builder = %+v, want %+v (untouched)", got.Builder, existing.Builder)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})

	t.Run("rebind of DONE binding is refused", func(t *testing.T) {
		// Reset state to Done for this subtest
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("reset existing binding: %v", err)
		}
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err == nil {
			t.Fatal("rebind on done binding must be refused")
		}
		wantMsg := `binding "webshop" is done: ` + "`relay bind` to start fresh"
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
		}
		if len(f.tabs) != 0 || len(f.starts) != 0 {
			t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
		}
	})
}

func TestResumeRestoresMissingWorktree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	wantCall := checkoutWorktreeCall{Dir: "/repo", Path: wt, Branch: "relay/webshop"}
	if fg.checkoutWorktreeCalls[0] != wantCall {
		t.Errorf("checkoutWorktreeCall = %+v, want %+v", fg.checkoutWorktreeCalls[0], wantCall)
	}
	if res.RestoredWorktree != wt {
		t.Errorf("RestoredWorktree = %q, want %q", res.RestoredWorktree, wt)
	}
	if res.RestoredBranch != "relay/webshop" {
		t.Errorf("RestoredBranch = %q, want relay/webshop", res.RestoredBranch)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateActive {
		t.Errorf("loaded.State = %s, want active", loaded.State)
	}
}

// TestResumePausedRestoresAndRebinds pins #137: resuming a PAUSED binding
// restores the released worktree and rebinds a fresh builder even though the
// caller passed neither --rebind nor --builder, because a paused binding has
// no builder identity left to keep.
func TestResumePausedRestoresAndRebinds(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo
	rt.Policy = orderOf("builder", testAgyRef)

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		Round:    4,
		State:    store.StatePaused,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{Kind: "agy"}, // pause cleared PaneID, kept Kind
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if !res.WasPaused {
		t.Error("WasPaused = false, want true")
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	if !got.Builder.Headless() {
		t.Fatalf("Builder = %+v, want a headless rebuild on resume of a PAUSED binding", got.Builder)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
	if got.Round != 4 {
		t.Errorf("Round = %d, want 4 (unchanged)", got.Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindResume {
		t.Errorf("last log kind = %s, want resume", last.Kind)
	}
	if last.Note != "resumed" {
		t.Errorf("last log note = %q, want resumed", last.Note)
	}
}

func TestResumeRestoreHeadlessHasNoOrphan(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo
	fr := newFakeRunner()
	rt.Runner = fr

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if res.OrphanedPane != "" {
		t.Errorf("OrphanedPane = %q, want empty", res.OrphanedPane)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Errorf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
}

func TestResumeRefusesRestoreWithoutBranch(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "no branch is recorded") {
		t.Fatalf("err = %v, want containing 'no branch is recorded'", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("loaded.State = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeRefusesRestoreFromWrongRepo(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{} // branchExists stays false: the caller's cwd has no relay/webshop
	rt.Git = fg

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt,
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/elsewhere",
	})
	if err == nil || !strings.Contains(err.Error(), "run resume from the repository") {
		t.Fatalf("err = %v, want 'run resume from the repository'", err)
	}
	if fg.lastBranchDir != "/elsewhere" || fg.lastBranchName != "relay/webshop" {
		t.Errorf("BranchExists asked (%q, %q), want (/elsewhere, relay/webshop)", fg.lastBranchDir, fg.lastBranchName)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("state = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeSurfacesBranchCheckedOut(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{checkoutWorktreeErr: git.ErrBranchCheckedOut}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "git worktree list") {
		t.Fatalf("err = %v, want containing 'git worktree list'", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs=%d starts=%+v, want none", len(f.tabs), f.starts)
	}
}

func TestResumePresentWorktreeIsNotRestored(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := t.TempDir()
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		State:    store.StateActive,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	if res.RestoredWorktree != "" {
		t.Errorf("RestoredWorktree = %q, want empty", res.RestoredWorktree)
	}
}

func TestRebindOnDoneWithRestoredWorktree(t *testing.T) {
	oldBuilder := herdr.Agent{
		Name:   "webshop-builder",
		Kind:   "opencode",
		Status: herdr.StatusIdle,
		PaneID: "w2:p4",
		CWD:    "/repo",
	}
	f := &fakeHerdr{
		agents:  []herdr.Agent{plannerAgent(), oldBuilder},
		newPane: "w2:p5",
	}
	rt := newRuntime(t, f)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relay/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:     "webshop",
		CWD:      wt, // an add binding's CWD is its worktree
		Worktree: wt,
		Branch:   "relay/webshop",
		Round:    4,
		State:    store.StateDone,
		Planner:  store.Endpoint{PaneID: "w2:p3"},
		Builder:  store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
	if !got.Builder.Headless() {
		t.Errorf("Builder = %+v, want a headless endpoint", got.Builder)
	}
}

func TestBindRebindNotFound(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got err = %v, want store.ErrNotFound", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("no tab may be created or agent started, tabs=%d starts=%+v", len(f.tabs), f.starts)
	}
}

func TestBindTimeoutOverrideAndDefault(t *testing.T) {
	t.Run("override is stored", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
			RoundTimeout: 90 * time.Minute,
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got, want := b.RoundTimeoutMS, int((90 * time.Minute).Milliseconds()); got != want {
			t.Errorf("RoundTimeoutMS = %d, want %d", got, want)
		}
	})

	t.Run("default is a day, not half an hour", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "kobe", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo2",
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		// A builder working a real stage runs for hours; 30m flagged healthy
		// work as needing a human on the first live run.
		if got, want := b.RoundTimeoutMS, int((24 * time.Hour).Milliseconds()); got != want {
			t.Errorf("default RoundTimeoutMS = %d, want %d (24h)", got, want)
		}
	})
}

func TestUnbindTeardown(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary binding unbinds with all-zero result", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		b := store.Binding{
			Name: "webshop", CWD: "/repo", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "webshop", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.ArchivedTo != "" || res.WorktreeRemoved != "" || res.WorktreeKept != "" || res.KeptReason != "" {
			t.Errorf("expected all-zero result for ordinary binding unbind, got %+v", res)
		}
		if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding state still exists after Unbind: %v", err)
		}
	})

	t.Run("clean worktree is removed", func(t *testing.T) {
		fg := &fakeGit{}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-clean", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-clean", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if res.WorktreeKept != "" {
			t.Errorf("WorktreeKept = %q, want empty", res.WorktreeKept)
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
		}
		if fg.removeWorktreeCalls[0].Force {
			t.Error("teardown must pass force: false")
		}
		if _, err := rt.Store.Load("fork-clean"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should be removed")
		}
	})

	t.Run("dirty worktree is kept and says why", func(t *testing.T) {
		fg := &fakeGit{dirtyResult: true}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != "" {
			t.Errorf("WorktreeRemoved = %q, want empty", res.WorktreeRemoved)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "uncommitted changes" {
			t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Error("RemoveWorktree must NOT be called for dirty tree")
		}
		if _, err := rt.Store.Load("fork-dirty"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("dirty check error keeps worktree with honest reason", func(t *testing.T) {
		fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty-err", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty-err", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "dirty check failed: git lock busy" {
			t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
		}
	})

	t.Run("git unavailable keeps worktree", func(t *testing.T) {
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = nil

		b := store.Binding{
			Name: "fork-nogit", CWD: "/state/.worktrees/fork-nogit",
			Worktree: "/state/.worktrees/fork-nogit", State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-nogit", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != "/state/.worktrees/fork-nogit" || res.KeptReason != "git unavailable" {
			t.Errorf("kept mismatch: %+v", res)
		}
		if _, err := rt.Store.Load("fork-nogit"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("git remove failure keeps worktree and completes unbind", func(t *testing.T) {
		fg := &fakeGit{removeWorktreeErr: errors.New("git lock locked\ndetails")}
		f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
		rt := newRuntime(t, f)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-fail", CWD: wt,
			Worktree: wt, State: store.StateActive,
			Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-fail", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "git lock locked" {
			t.Errorf("KeptReason = %q, want 'git lock locked' (brief)", res.KeptReason)
		}
		if _, err := rt.Store.Load("fork-fail"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})
}

func TestUnbindReportsAnAlreadyGoneWorktree(t *testing.T) {
	ctx := context.Background()
	fg := &fakeGit{}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Git = fg

	missingWT := filepath.Join(t.TempDir(), "already-gone-worktree")
	b := store.Binding{
		Name: "fork-gone", CWD: "/repo",
		Worktree: missingWT, State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Unbind(ctx, rt, "fork-gone", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if res.WorktreeGone != missingWT {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, missingWT)
	}
	if res.WorktreeKept != "" || res.WorktreeRemoved != "" {
		t.Errorf("kept=%q removed=%q, want both empty", res.WorktreeKept, res.WorktreeRemoved)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("removeWorktreeCalls = %d, want 0", len(fg.removeWorktreeCalls))
	}
	if _, err := rt.Store.Load("fork-gone"); !errors.Is(err, store.ErrNotFound) {
		t.Error("binding state should still be deleted")
	}
}

// A session-less builder that cannot be located may be dead or may be alive in
// a pane that moved workspaces. relay cannot tell, so it must not spawn a
// replacement on the guess -- that is how a live builder gets orphaned.
// A builder with a recorded session is unambiguous: if no live agent carries
// that session it really is gone, so the gate must not fire.
// A builder with an agent name is unambiguous: it can be identified by name, so
// the gate must not fire even when SessionID is empty and a round was open.
// --assume-dead releases only the unverifiable case. A builder relay can
// positively see is alive is still refused: that is #20's guarantee.
// #20's recovery (PR #22): a session-less builder with no round in flight is
// unambiguous enough to rebind without ceremony. The gate must not broaden to
// catch this case -- if it ever does, this test fails loudly.
func TestResumeRebindClearsRoundClosedTree(t *testing.T) {
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		RoundClosedTree:  "tree-closed-123",
		State:            store.StateActive,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess"},
		BuilderCandidate: testOpencodeRef,
	}

	t.Run("with alias", func(t *testing.T) {
		f := &fakeHerdr{
			agents: []herdr.Agent{
				plannerAgent(),
				{Kind: "opencode", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
			},
			newPane: "w2:p9",
		}
		rt := newRuntime(t, f)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("Bind resume with alias: %v", err)
		}
		if got.RoundClosedTree != "" {
			t.Errorf("returned RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.RoundClosedTree != "" {
			t.Errorf("loaded RoundClosedTree = %q, want empty", saved.RoundClosedTree)
		}
	})

}

func TestResumePlannerOnlyPreservesRoundClosedTree(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusWorking, CWD: "/repo",
			PaneID: "w7:pB", Session: herdr.Session{Value: "planner-sess-2"}},
	}}
	rt := newRuntime(t, f)

	const closedTree = "tree-closed-123"
	existing := store.Binding{
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: closedTree,
		State:           store.StateOrphaned,
		Planner:         store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:         store.Endpoint{PaneID: "w2:p4", SessionID: "builder-sess"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, PlannerPane: "w7:pB", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind resume planner only: %v", err)
	}
	if got.RoundClosedTree != closedTree {
		t.Errorf("returned RoundClosedTree = %q, want %q", got.RoundClosedTree, closedTree)
	}

	saved, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.RoundClosedTree != closedTree {
		t.Errorf("loaded RoundClosedTree = %q, want %q", saved.RoundClosedTree, closedTree)
	}
}

func TestResumeRebindResolvesThroughTheOrder(t *testing.T) {
	// #92: a builder that halted between rounds is gone, no round is open,
	// and the planner wants a replacement without naming a token. Rebind
	// must walk policy.json order and the ledger exactly as create does,
	// and record the pick.
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		State:            store.StateBroken,
		Planner:          store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder:          store.Endpoint{PaneID: "w2:p4", SessionID: "dead-builder-sess", Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{
		agents: []herdr.Agent{
			plannerAgent(),
			{Kind: "agy", Status: herdr.StatusWorking, PaneID: "w2:p9", Session: herdr.Session{Value: "new-builder-sess"}},
		},
		newPane: "w2:p9",
	}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}

	if res.How != HowOrder || res.Position != 1 {
		t.Errorf("resolution = %+v, want order #1", res)
	}
	if got.BuilderCandidate != testAgyRef {
		t.Errorf("BuilderCandidate = %q, want the order's first, %s", got.BuilderCandidate, testAgyRef)
	}
	if !got.Builder.Headless() || got.Builder.Kind != "agy" || got.State != store.StateActive || got.Round != 3 {
		t.Errorf("binding = %+v, want a headless agy builder, active, still round 3", got)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var picks int
	for _, e := range entries {
		if e.Kind == store.KindPick && e.Round == 3 && e.Note == ExplainResolution("builder", res) {
			picks++
		}
	}
	if picks != 1 {
		t.Errorf("want exactly one pick entry for round 3 reading %q, got entries %+v", ExplainResolution("builder", res), entries)
	}
}

func TestBindHeadlessRecordsAnEndpointAndSpawnsNothing(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("headless must open no tab and start no agent: tabs=%d starts=%d", len(f.tabs), len(f.starts))
	}
	ep := b.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.AgentName != "webshop-builder" || ep.Kind != "opencode" {
		t.Errorf("AgentName/Kind = %q/%q, want webshop-builder/opencode", ep.AgentName, ep.Kind)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("candidate = %q / %q, want %q", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("round/state = %d/%s, want 1/active", b.Round, b.State)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh headless binding logs its pick and nothing else: %+v (%v)", entries, err)
	}
	// The stored binding reads back headless too.
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
func TestBindResumeRebindKeepsAHeadlessBindingHeadless(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateBroken,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, false) // the old process is gone
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Fatalf("a headless rebind must open no tab and start no agent: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	ep := got.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("rebound endpoint must be a fresh headless endpoint with nothing running: %+v", ep)
	}
	if got.BuilderCandidate != testAgyRef || res.How != HowOrder {
		t.Errorf("candidate = %q (%+v), want the order's first, %s", got.BuilderCandidate, res, testAgyRef)
	}
	if got.State != store.StateActive || got.Round != 4 {
		t.Errorf("state/round = %s/%d, want active/4", got.State, got.Round)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// herdr's agent list cannot see a process, so a headless binding's liveness
// is the Runner's answer. A live process refuses the rebind (§4.3).
func TestBindResumeRebindRefusesALiveHeadlessProcess(t *testing.T) {
	existing := store.Binding{
		Name:    "webshop",
		CWD:     "/repo",
		Round:   4,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3", SessionID: "planner-sess"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p9"}
	rt := newRuntime(t, f)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, true) // still running
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("a refused rebind must spawn nothing: tabs=%+v starts=%+v", f.tabs, f.starts)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || stored.Builder.PID != 4321 || !stored.Builder.Headless() {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

// A pane cannot replace a process builder; the mode is fixed at creation.
func TestBindHeadlessStillRefusesANameHerdrWouldRefuse(t *testing.T) {
	// The agent name is validated even though no herdr agent is started:
	// the name is what status, log and a later pane-mode rebind identify
	// the builder by, and the limit must not depend on the mode.
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}}
	rt := newRuntime(t, f)
	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33
	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	})
	if err == nil || !strings.Contains(err.Error(), "builder agent name") {
		t.Fatalf("err = %v, want the agent-name refusal", err)
	}
}

func TestBindWithTierEditOnClaude(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "edit",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "edit" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "edit")
	}
	gotArgs := launchArgs(t, rt, b, harness.TierEdit)
	for _, want := range []string{"--model", "m", "--agent", "plan-executor", "--permission-mode", "acceptEdits"} {
		if !slicesContains(gotArgs, want) {
			t.Errorf("expected %q in the launch args, got %v", want, gotArgs)
		}
	}
}

func TestBindWithTierYoloWithoutAllowYoloRefused(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "yolo",
	})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("err = %v, want ErrTierAboveMax", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindOpencodeCandidateTierReadRefused(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testOpencodeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
		Tier:        "read",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if len(f.tabs) != 0 || len(f.starts) != 0 {
		t.Errorf("tabs = %d, starts = %d; want 0", len(f.tabs), len(f.starts))
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindPolicyTierBuilderRead(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Tier = map[string]string{"builder": "read"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:        "webshop",
		Candidate:   testClaudeRef,
		PlannerPane: "w2:p3",
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "read" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "read")
	}
	args := launchArgs(t, rt, b, harness.TierRead)
	hasFlag := false
	for i, arg := range args {
		if arg == "--permission-mode" && i+1 < len(args) && args[i+1] == "plan" {
			hasFlag = true
			break
		}
	}
	if !hasFlag {
		t.Errorf("expected --permission-mode plan in starts[0].Args, got %v", f.starts[0].Args)
	}
}

// TestBindGateFlagStored pins #132: an explicit --gate is stored on the
// binding as given.
func TestBindGateFlagStored(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Gate: "make check",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want %q", b.Gate, "make check")
	}
}

// TestBindGatePolicyDefaultApplied pins #132: with no --gate, policy.json's
// gate.default is used.
func TestBindGatePolicyDefaultApplied(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want the policy default %q", b.Gate, "make check")
	}
}

// TestBindNoGateOverridesPolicyDefault pins #132: --no-gate opts a binding
// out of policy.json's gate.default.
func TestBindNoGateOverridesPolicyDefault(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		NoGate: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "" {
		t.Errorf("b.Gate = %q, want empty despite the policy default", b.Gate)
	}
}

// TestForkInheritsSourceGate pins #132: a fork with no --gate/--no-gate
// inherits the source binding's Gate.
func TestForkInheritsSourceGate(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Gate = "make check"
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Gate != "make check" {
		t.Errorf("Binding.Gate = %q, want inherited from source", res.Binding.Gate)
	}

	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Gate != "make check" {
		t.Errorf("stored Gate = %q, want inherited from source", stored.Gate)
	}
}

// TestBindRegateFlagStored pins #132 part 2: an explicit --regate is stored on
// the binding as given.
func TestBindRegateFlagStored(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Regate: ptr(3),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 3 {
		t.Errorf("b.Regate = %d, want 3", b.Regate)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Regate != 3 {
		t.Errorf("stored Regate = %d, want 3", stored.Regate)
	}
}

// TestBindRegatePolicyDefaultApplied pins #132 part 2: with no --regate,
// policy.json's gate.regate becomes the binding's budget.
func TestBindRegatePolicyDefaultApplied(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 2 {
		t.Errorf("b.Regate = %d, want the policy default 2", b.Regate)
	}
}

// TestBindRegateFlagOverridesPolicy pins #132 part 2: an explicit --regate 0
// turns the policy default off for this binding.
func TestBindRegateFlagOverridesPolicy(t *testing.T) {
	f := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p4"}
	rt := newRuntime(t, f)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerPane: "w2:p3", CWD: "/repo",
		Regate: ptr(0),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 0 {
		t.Errorf("b.Regate = %d, want 0 despite the policy default", b.Regate)
	}
}

// TestForkInheritsSourceRegate pins #132 part 2: a fork with no --regate
// inherits the source binding's budget.
func TestForkInheritsSourceRegate(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Regate = 2
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Regate != 2 {
		t.Errorf("Binding.Regate = %d, want inherited from source", res.Binding.Regate)
	}

	stored, err := rt.Store.Load("alt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Regate != 2 {
		t.Errorf("stored Regate = %d, want inherited from source", stored.Regate)
	}
}

// TestForkRegateFlagOverridesSource pins #132 part 2: an explicit --regate on
// a fork wins over the source's budget.
func TestForkRegateFlagOverridesSource(t *testing.T) {
	ctx := context.Background()
	fh := &fakeHerdr{agents: []herdr.Agent{plannerAgent()}, newPane: "w2:p5"}
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt := newForkRuntime(t, fh, fg, nil)

	srcCWD := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(srcCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	seedFourRoundBinding(t, rt, "source", srcCWD)
	src, err := rt.Store.Load("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Regate = 2
	if err := rt.Store.Save(src); err != nil {
		t.Fatal(err)
	}

	res, err := Fork(ctx, rt, ForkOptions{
		Source:      "source",
		Round:       2,
		NewName:     "alt",
		PlannerPane: "w2:p3",
		Regate:      ptr(0),
	})
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if res.Binding.Regate != 0 {
		t.Errorf("Binding.Regate = %d, want the explicit 0", res.Binding.Regate)
	}
}
