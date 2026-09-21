package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// stubHerdr implements relay.Herdr with no-op stubs. relay.Status calls
// ListAgents unconditionally, so every RelayVerbs test needs a non-nil
// Herdr even when the binding under test never reaches a live pane; every
// other method here is unused by the paths these tests exercise.
type stubHerdr struct {
	agents []herdr.Agent
	err    error
}

func (s *stubHerdr) ListAgents(ctx context.Context) ([]herdr.Agent, error)   { return s.agents, s.err }
func (s *stubHerdr) Prompt(ctx context.Context, target, text string) error   { return nil }
func (s *stubHerdr) SendKeys(ctx context.Context, target, keys string) error { return nil }
func (s *stubHerdr) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	return "", nil
}
func (s *stubHerdr) ReadAgentSource(ctx context.Context, target, source string, lines int) (string, error) {
	return "", nil
}
func (s *stubHerdr) CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error) {
	return "", nil
}
func (s *stubHerdr) StartAgent(ctx context.Context, name, kind, paneID string, args []string) error {
	return nil
}
func (s *stubHerdr) Notify(ctx context.Context, title, body string, sound herdr.Sound) error {
	return nil
}
func (s *stubHerdr) ReportMetadata(ctx context.Context, paneID string, m herdr.PaneMetadata) error {
	return nil
}
func (s *stubHerdr) ClosePane(ctx context.Context, paneID string) error { return nil }
func (s *stubHerdr) Subscribe(ctx context.Context, paneIDs []string) (<-chan herdr.Event, error) {
	return nil, herdr.ErrNoSocket
}

// stubRunner implements relay.Runner with no-op stubs, for a headless
// binding whose PID is 0 (Alive is never actually called on that path, but
// Runtime.Runner must be non-nil or sendPreflight refuses before it gets
// that far).
type stubRunner struct{}

func (stubRunner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	return relay.ProcHandle{}, nil
}
func (stubRunner) Alive(ctx context.Context, h relay.ProcHandle) (bool, error) { return false, nil }
func (stubRunner) ExitCode(ctx context.Context, h relay.ProcHandle, logPath string) (int, bool) {
	return 0, false
}
func (stubRunner) Kill(ctx context.Context, h relay.ProcHandle) error { return nil }

func writeCandidates(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write candidates fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidates fixture: %v", err)
	}
	return set
}

func writeTempPlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func saveVerbBinding(t *testing.T, s *store.Store, b store.Binding) {
	t.Helper()
	if err := s.Save(b); err != nil {
		t.Fatalf("save binding %q: %v", b.Name, err)
	}
}

// TestRelayVerbsStatusFiltersByPaneThenName is the one status.go RelayVerbs
// behaviour the plan calls out as needing a real test: bindings are scoped
// to this pane unless All is set, narrowed further by Name, with the same
// DONE-hiding the CLI applies by default.
func TestRelayVerbsStatusFiltersByPaneThenName(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{
		Herdr: &stubHerdr{},
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine-a", CWD: "/repo/mine-a", Planner: store.Endpoint{PaneID: "w2:p3"}, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "mine-done", CWD: "/repo/mine-done", Planner: store.Endpoint{PaneID: "w2:p3"}, Round: 1, State: store.StateDone})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", Planner: store.Endpoint{PaneID: "w9:p9"}, Round: 1, State: store.StateActive})

	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}

	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep, ok := res.(relay.Report)
	if !ok {
		t.Fatalf("result = %#v, want relay.Report", res)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-a" {
		t.Fatalf("default status = %+v, want only mine-a (this pane, DONE hidden)", rep.Bindings)
	}

	res, err = v.Status(context.Background(), StatusArgs{All: true})
	if err != nil {
		t.Fatalf("Status all: %v", err)
	}
	rep = res.(relay.Report)
	if len(rep.Bindings) != 3 {
		t.Fatalf("all status = %d bindings, want 3", len(rep.Bindings))
	}

	res, err = v.Status(context.Background(), StatusArgs{Name: "mine-done"})
	if err != nil {
		t.Fatalf("Status by name: %v", err)
	}
	rep = res.(relay.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-done" {
		t.Fatalf("status by name = %+v, want only mine-done (DONE included when named)", rep.Bindings)
	}

	if _, err := v.Status(context.Background(), StatusArgs{Name: "other"}); err == nil {
		t.Fatal("Status naming a binding on a different pane must error")
	}
}

// TestRelayVerbsSendDryRunHeadless exercises Send's real forwarding into
// relay.SendDryRun: a headless binding needs no herdr at all (only
// Store, Candidates and Runner), so this runs against the real function,
// not a seam.
func TestRelayVerbsSendDryRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relay.Runtime{
		Herdr:      &stubHerdr{},
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, DryRun: true})
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	d, ok := res.(relay.DryRun)
	if !ok {
		t.Fatalf("result = %#v, want relay.DryRun", res)
	}
	if d.Mode != "headless" {
		t.Errorf("Mode = %q, want headless", d.Mode)
	}
	if d.Name != "webshop" || d.Round != 1 {
		t.Errorf("DryRun = %+v, want Name webshop, Round 1", d)
	}
}

// TestRelayVerbsSendRealRunHeadless proves DryRun false takes the real
// relay.Send path, not SendDryRun: for a headless binding this needs no
// live pane either, so it runs to completion against stubRunner.
func TestRelayVerbsSendRealRunHeadless(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relay.Runtime{
		Herdr:      &stubHerdr{},
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sr, ok := res.(relay.SendResult)
	if !ok {
		t.Fatalf("result = %#v, want relay.SendResult", res)
	}
	if sr.Round != 1 {
		t.Errorf("Round = %d, want 1", sr.Round)
	}
}

// TestRelayVerbsAnswerForwardsToRelayAnswer proves Answer forwards Name and
// exactly one of AnswerArgs' three fields into relay.AnswerInput: a headless
// binding's relay.Answer returns ErrHeadlessNoDialog only once resolve()
// (which requires exactly one field set) has already succeeded, so seeing
// that specific error -- not "needs one of" or "exactly one of" -- proves
// Text made it through as the one field set.
func TestRelayVerbsAnswerForwardsToRelayAnswer(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{Herdr: &stubHerdr{}, Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Round:   1, State: store.StateNeedsYou,
	})

	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}
	_, err := v.Answer(context.Background(), AnswerArgs{Name: "webshop", Text: "go ahead"})
	if !errors.Is(err, relay.ErrHeadlessNoDialog) {
		t.Fatalf("Answer error = %v, want ErrHeadlessNoDialog", err)
	}
}

// TestRelayVerbsDoneForwardsAndReportsText proves Done calls relay.Done
// (which flips the binding's stored State to done) and reports relay.DoneText
// alongside the structured DoneResult.
func TestRelayVerbsDoneForwardsAndReportsText(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{Herdr: &stubHerdr{}, Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Round:   1, State: store.StateActive,
	})

	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}
	res, err := v.Done(context.Background(), DoneArgs{Name: "webshop"})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	dr, ok := res.(doneResult)
	if !ok {
		t.Fatalf("result = %#v, want doneResult", res)
	}
	if dr.Text == "" {
		t.Error("Text must be set from relay.DoneText")
	}

	updated, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if updated.State != store.StateDone {
		t.Errorf("State = %q, want done", updated.State)
	}
}

func TestRelayVerbsDoneErrorPropagates(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relay.Runtime{Herdr: &stubHerdr{}, Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	v := &RelayVerbs{RT: rt, Pane: "w2:p3"}

	if _, err := v.Done(context.Background(), DoneArgs{Name: "nonexistent"}); err == nil {
		t.Fatal("Done on a binding that does not exist must error")
	}
}
