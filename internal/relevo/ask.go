package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// AskOptions describes one consult request.
type AskOptions struct {
	Role      string // consult role to spawn; required
	Candidate string // a harness/provider/model token; empty resolves through the one rule in resolveCandidate
	File      string // the question file; required
	Name      string // binding name, already resolved by the caller
	// PlannerID is the caller's --planner value when it has one, and the
	// resolved record's id afterwards. Empty means "resolve this session's
	// planner" (§4.3). A round consult (Round > 0) resumes a recorded
	// session and never resolves a planner.
	PlannerID string
	// Round > 0 asks the builder that built this closed round instead of
	// spawning a role: Role and Candidate are ignored, because the round's
	// recorded session fixes both (#147 part 2).
	Round int
	// Question is an inline question. With Round > 0 exactly one of File and
	// Question must be set; without Round it is unused.
	Question string
}

// AskResult is what an ask produced, so the CLI can print the show command
// that displays the findings.
type AskResult struct {
	Consult store.Consult
	Binding string

	// Candidate is the canonical token the consult was started from, for the
	// CLI's gatedNote (#61 step 1).
	Candidate string

	// Resolution is how the candidate was chosen, for the pick line.
	Resolution Resolution
}

// Ask spawns one read-only, one-shot consult beside a binding's builder and
// returns immediately. The daemon watches for its findings.
//
// Every consult is headless since #303: a one-shot process through the Runner,
// its final message becoming the findings (#147, #144). No consult opens a pane.
//
// With opts.Round > 0 the ask is a round consult (#147 part 2): it resumes the
// harness session that built closed round opts.Round and asks it the question,
// headless and read-only, instead of resolving a role and a candidate. It then
// requires exactly one of opts.File and opts.Question.
//
// Preconditions:  opts.File is readable; opts.Role resolves to a consult spec
//
//	whose Tree is not "none"; the binding exists and is neither broken
//	nor done; running consults are below the binding's cap.
//
// Postconditions: on a validation failure nothing on disk changed and nothing
//
//	was spawned. On a reservation failure no record is written and no
//	process exists; consult.ErrConsultCap is raised there. On success
//	exactly one record for the id exists, State is running, Endpoint is
//	headless, and the ask is logged with Confirmed: true. On a spawn
//	failure exactly one record for the id exists, State is silent, and
//	Note names the failure.
//
// Errors: ErrUnknownRole, consult.ErrNotAConsultRole, ErrNoCandidates,
//
//	ErrRoleNotServed, ErrAmbiguousCandidate, candidate.ErrUnknownCandidate,
//	consult.ErrTreelessUnsupported, consult.ErrConsultCap,
//	ErrRunnerUnavailable, store.ErrNotFound, or a wrapped store error from
//	either phase.
func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error) {
	if opts.Round > 0 {
		return askRound(ctx, rt, opts)
	}

	// The consult itself does not record a planner, but the verb is still a
	// planner verb: it resolves one, and ErrNoPlanner is the hard error.
	if _, haveRec, err := resolveVerbPlanner(rt, opts.PlannerID); err != nil {
		return AskResult{}, err
	} else if !haveRec {
		return AskResult{}, ErrNoPlannerSession
	}

	// Read the caller's file before taking the lock; it is the one input that
	// does not depend on binding state.
	body, err := os.ReadFile(opts.File)
	if err != nil {
		return AskResult{}, fmt.Errorf("read question %s: %w", opts.File, err)
	}

	reg := rt.RoleRegistry()
	info, ok := reg.Role(opts.Role)
	if !ok {
		return AskResult{}, fmt.Errorf("unknown actor %q (known: %v): %w", opts.Role, reg.Names(), ErrUnknownRole)
	}
	if info.Shape != harness.ShapeConsult {
		return AskResult{}, fmt.Errorf("%q: %w", opts.Role, consult.ErrNotAConsultRole)
	}
	if rt.Runner == nil {
		// Phase 0, next to the role check: a consult runs a process, so a
		// runtime with no Runner refuses before anything is reserved.
		return AskResult{}, spawn.ErrRunnerUnavailable
	}
	res, err := resolveRole(reg, rt.Candidates, availability.Gates(AvailabilityDeps(rt)), opts.Candidate, opts.Role)
	if err != nil {
		return AskResult{}, err
	}
	c := res.Candidate
	if c.Tree == "none" {
		return AskResult{}, fmt.Errorf("candidate %q declares tree \"none\": %w", c.Ref().String(), consult.ErrTreelessUnsupported)
	}
	role, err := reg.Spec(opts.Role, c.Harness)
	if err != nil {
		return AskResult{}, fmt.Errorf("%q on %s: %v: %w", opts.Role, c.Ref().String(), err, ErrRoleNotServed)
	}
	h, _ := harness.Lookup(c.Harness)
	tier := resolveRoleTier("", c, reg, opts.Role)
	if err := checkTierCap(tier, rt.Policy, false); err != nil {
		return AskResult{}, err
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return AskResult{}, err
	}

	newID := rt.NewID
	if newID == nil {
		newID = consult.RandomID
	}

	// Mint the id and compose the agent name before the lock, so a name relevo
	// would refuse fails as a validation error before the reservation is
	// written or the question staged: nothing to clean up.
	id := newID()
	agentName := opts.Name + "-" + role.Name + "-" + id
	if err := store.ValidName(agentName); err != nil {
		return AskResult{}, fmt.Errorf(
			"consult agent name %q: %w -- binding %q needs a name of at most %d characters to run %q consults",
			agentName, err, opts.Name, store.MaxAgentNameLen-len("-"+role.Name+"-")-8, role.Name)
	}

	record, err := consult.Spawn(ctx, consultDeps(rt), consult.Request{
		Name:      opts.Name,
		ID:        id,
		Role:      role.Name,
		Endpoint:  store.Endpoint{AgentName: agentName, Kind: l.Kind},
		Body:      body,
		Candidate: c,
		Spec:      role,
		Tier:      tier,
		Pick:      func(round int) *store.LogEntry { p := pickEntry(rt.Now(), round, role.Name, res); return &p },
		AskNote:   role.Name + " " + id,
	})
	if err != nil && record.ID == "" {
		return AskResult{}, err
	}

	return AskResult{Consult: record, Binding: opts.Name, Candidate: c.Ref().String(), Resolution: res}, err
}

// askRound is `ask --round N`: it resumes the session that built closed round
// N, read-only where the harness has a read tier, and asks it one question,
// headless. The round is closed, so the resumed turn cannot race a live
// builder; claude and agy continue that session in place, opencode forks.
// Everything after the resolution -- the reservation, the staged question, the
// ask entry, and the findings the headless reconcile delivers -- is the
// headless consult's: the round consult differs in its argv, its role label and
// its prompt, and in nothing else.
func askRound(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error) {
	if (opts.File == "") == (opts.Question == "") {
		return AskResult{}, errors.New("ask --round needs --file or -q")
	}
	body := []byte(opts.Question)
	if opts.File != "" {
		var err error
		body, err = os.ReadFile(opts.File)
		if err != nil {
			return AskResult{}, fmt.Errorf("read question %s: %w", opts.File, err)
		}
	}

	if rt.Runner == nil {
		// Phase 0: a round consult runs a process, so a runtime with no
		// Runner refuses before anything is reserved.
		return AskResult{}, spawn.ErrRunnerUnavailable
	}

	b, err := rt.Store.Load(opts.Name)
	if err != nil {
		return AskResult{}, err
	}
	if b.Builder.Remote() {
		return AskResult{}, errors.New("consults are local-only")
	}
	if opts.Round >= b.Round {
		return AskResult{}, fmt.Errorf("round %d is the open round; talk to the live builder or wait for it to close", opts.Round)
	}

	entries, err := rt.Store.ReadLog(opts.Name)
	if err != nil {
		return AskResult{}, err
	}
	s, ok := roundSession(entries, opts.Round)
	if !ok {
		return AskResult{}, fmt.Errorf("round %d of %q recorded no builder session (built before relevo recorded sessions, or the harness printed none); nothing to resume", opts.Round, opts.Name)
	}

	newID := rt.NewID
	if newID == nil {
		newID = consult.RandomID
	}
	id := newID()
	agentName := opts.Name + "-" + consult.RoundRole + "-" + id
	if err := store.ValidName(agentName); err != nil {
		return AskResult{}, fmt.Errorf(
			"consult agent name %q: %w -- binding %q needs a name of at most %d characters to run %q consults",
			agentName, err, opts.Name, store.MaxAgentNameLen-len("-"+consult.RoundRole+"-")-8, consult.RoundRole)
	}

	// claude and agy have a read tier and resume in it. opencode has none, so
	// it runs at harness and --fork is what keeps the original session
	// untouched. codex has no verified resume form and Resume refuses it.
	h, _ := harness.Lookup(s.Kind)
	tier := harness.TierHarness
	if s.Kind == "claude" || s.Kind == "agy" {
		tier = harness.TierRead
	}
	askPath := rt.Store.AskPath(opts.Name, b.Round, id)
	// The question goes in the prompt when the whole prompt fits inline;
	// otherwise the prompt reads it back from the file Spawn stages at this
	// same askPath.
	prompt, inline := consult.RoundQuestion(opts.Round, opts.Name, body, askPath)
	argv, err := h.Resume(s.ID, prompt, tier)
	if err != nil {
		return AskResult{}, fmt.Errorf("%s session %s: %w", s.Kind, short8(s.ID), err)
	}

	record, err := consult.Spawn(ctx, consultDeps(rt), consult.Request{
		Name:     opts.Name,
		ID:       id,
		Role:     consult.RoundRole,
		Endpoint: store.Endpoint{AgentName: agentName, Kind: s.Kind, SessionID: s.ID},
		Body:     body,
		Round:    opts.Round,
		Argv:     append([]string{h.Binary}, argv...),
		Inline:   inline,
		AskNote:  fmt.Sprintf("round %d session %s:%s", opts.Round, s.Kind, short8(s.ID)),
	})
	if err != nil && record.ID == "" {
		return AskResult{}, err
	}

	return AskResult{Consult: record, Binding: opts.Name}, err
}
