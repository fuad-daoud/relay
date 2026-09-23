package relevo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNotAConsultRole reports an ask naming an alias that is a builder. A
// builder is persistent and writes; handing one a prompt with no plan in it
// would start a round that never was.
var ErrNotAConsultRole = errors.New("that role is the builder role; bind it with relevo bind, not relevo ask")

// ErrTreelessUnsupported reports an ask for a role declaring tree "none".
// The field is forward-declared for a future treeless explorer; no treeless
// role ships yet, and silently running one in the binding's tree would put an
// agent somewhere its role did not ask for.
var ErrTreelessUnsupported = errors.New("treeless consult roles are not implemented")

// ErrConsultCap reports an ask that would exceed the binding's running-consult
// cap. An idle harness pane holds roughly 800 MB.
var ErrConsultCap = errors.New("binding is at its consult cap")

// consultHeadlessPrompt is the whole prompt a headless consult runs with. It
// asks for the findings as the final message rather than a file: a
// `read`-tier process may not be able to write one, and relevo extracts that
// message from the stream at exit and writes FindingsPath itself.
const consultHeadlessPrompt = "Read: %s\n\nAnswer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relevo records your final message."

// roundRole is the record label for a consult that asks a closed round's own
// builder (#147 part 2). It is a label, not a role table entry: the session
// the round resumes already fixes what the consult runs as, so no role is
// resolved and no harness.RoleSpec is needed.
const roundRole = "round"

// roundAskPrompt is the whole prompt a round consult runs with. Its origin
// line is a literal because OriginLine has no form for DirToConsult: this
// payload is addressed to a builder, but it is not a round.
const roundAskPrompt = "relevo: consult · to builder of round %d · about binding %q (not the human)\n\nYou built round %d of this binding. Answer from what you did and why; do not change anything, do not run tools that write.\n\nRead: %s\n\nAnswer as your final message, complete, in markdown; relevo records it."

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

// AskResult is what an ask produced, so the CLI can tell the planner where the
// findings will appear without re-deriving the path.
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
//	process exists; ErrConsultCap is raised here and only here. On success
//	exactly one record for the id exists, State is running, Endpoint is
//	headless, and the ask is logged with Confirmed: true. On a spawn
//	failure exactly one record for the id exists, State is silent, and
//	Note names the failure.
//
// Errors: ErrUnknownRole, ErrNotAConsultRole, ErrNoCandidates, ErrRoleNotServed,
//
//	ErrAmbiguousCandidate, candidate.ErrUnknownCandidate,
//	ErrTreelessUnsupported, ErrConsultCap, ErrRunnerUnavailable,
//	store.ErrNotFound, or a wrapped store error from either phase.
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
		return AskResult{}, fmt.Errorf("unknown role %q (known: %v): %w", opts.Role, reg.Names(), ErrUnknownRole)
	}
	if info.Shape != harness.ShapeConsult {
		return AskResult{}, fmt.Errorf("%q: %w", opts.Role, ErrNotAConsultRole)
	}
	if rt.Runner == nil {
		// Phase 0, next to the role check: a consult runs a process, so a
		// runtime with no Runner refuses before anything is reserved.
		return AskResult{}, ErrRunnerUnavailable
	}
	res, err := resolveRole(reg, rt.Candidates, Gates(rt), opts.Candidate, opts.Role)
	if err != nil {
		return AskResult{}, err
	}
	c := res.Candidate
	if c.Tree == "none" {
		return AskResult{}, fmt.Errorf("candidate %q declares tree \"none\": %w", c.Ref().String(), ErrTreelessUnsupported)
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
		newID = randomConsultID
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

	// ── phase 1: reserve ─────────────────────────────── lock held, no external calls
	consult, cwd, owner, err := reserveConsult(rt, opts, id, role.Name,
		store.Endpoint{AgentName: agentName, Kind: l.Kind}, body)
	if err != nil {
		return AskResult{}, err
	}

	// ── phase 2: spawn ──────────────────────────────────────── no lock held
	var spawnErr error
	// The consult is a process, not a pane: no tab, no agent start, no prompt
	// to type. The stream carries its final message and the supervisor's exit
	// trailer, and Endpoint.LogPath names it so consultSource's headless
	// branch reads the stream (#147, #144).
	streamPath := rt.Store.ConsultStreamPath(opts.Name, consult.Round, consult.ID)
	argv, err := headlessLaunch(c, role, tier, consultTimeout,
		fmt.Sprintf(consultHeadlessPrompt, consult.AskPath), cwd, rt.Store.Dir(opts.Name))
	if err != nil {
		consult.State = store.ConsultSilent
		consult.Note = "spawn failed: " + brief(err)
		spawnErr = err
	} else if h, err := rt.Runner.Start(ctx, ProcSpec{
		Dir:        cwd,
		Argv:       argv,
		LogPath:    rt.Store.ConsultLogPath(opts.Name, consult.Round, consult.ID),
		StreamPath: streamPath,
		Scope:      scopeFor(rt, scopeConsult, scopeUnitNameFor(scopeConsult, owner, opts.Name, consult.Round, consult.ID), ""),
	}); err != nil {
		consult.State = store.ConsultSilent
		consult.Note = "spawn failed: " + brief(err)
		spawnErr = fmt.Errorf("start consult %q: %w", consult.Endpoint.AgentName, err)
	} else {
		consult.Endpoint = store.Endpoint{
			AgentName: consult.Endpoint.AgentName,
			Kind:      l.Kind,
			Mode:      store.ModeHeadless,
			PID:       h.PID,
			StartedAt: h.StartedAt.Unix(),
			LogPath:   streamPath,
		}
		consult.State = store.ConsultRunning
	}

	// ── phase 3: record ──────────────────────────────── lock held, no external calls
	var pick *store.LogEntry
	if consult.State == store.ConsultRunning {
		p := pickEntry(rt.Now(), consult.Round, role.Name, res)
		pick = &p
	}
	saveErr := recordConsult(rt, opts.Name, consult, pick, role.Name+" "+consult.ID)
	if saveErr != nil {
		if spawnErr != nil {
			return AskResult{Consult: consult, Binding: opts.Name, Candidate: c.Ref().String(), Resolution: res}, strandError(spawnErr, saveErr)
		}
		return AskResult{Consult: consult, Binding: opts.Name, Candidate: c.Ref().String(), Resolution: res}, fmt.Errorf("consult %s is running as pid %d but could not be recorded: %w", consult.ID, consult.Endpoint.PID, saveErr)
	}

	return AskResult{Consult: consult, Binding: opts.Name, Candidate: c.Ref().String(), Resolution: res}, spawnErr
}

// reserveConsult is the reserve phase every consult shares: under the lock,
// load the binding, refuse a remote one, a closed one and one at its consult
// cap, write the record, stage the question, and save. roleName is the
// record's role label and endpoint its endpoint minus the fields the spawn
// fills in: every consult but a round one takes both from the candidate it
// resolved. Nothing here reads the role table or a harness.RoleSpec:
// roleName is a label, which is what lets a round consult call itself
// roundRole. It also returns the loaded binding's Owner, for the consult's
// scope unit name (#313), so the caller needs no second load.
func reserveConsult(rt Runtime, opts AskOptions, id, roleName string, endpoint store.Endpoint, body []byte) (store.Consult, string, string, error) {
	var (
		consult store.Consult
		cwd     string
		owner   string
	)
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		owner = b.Owner
		if b.Builder.Remote() {
			return errors.New("consults are local-only")
		}
		if b.State == store.StateBroken || b.State == store.StateDone || b.State == store.StatePaused {
			return fmt.Errorf("binding %q is %s; a consult needs a live binding to attach to", b.Name, b.State)
		}
		if runningConsults(b) >= consultCap(b) {
			return fmt.Errorf("binding %q has %d running consults (cap %d), wait for one to finish: %w",
				b.Name, runningConsults(b), consultCap(b), ErrConsultCap)
		}

		consult = store.Consult{
			ID:           id,
			Role:         roleName,
			Round:        b.Round,
			AskPath:      rt.Store.AskPath(b.Name, b.Round, id),
			FindingsPath: rt.Store.FindingsPath(b.Name, b.Round, id),
			Endpoint:     endpoint,
			State:        store.ConsultSpawning,
			SpawnedAt:    rt.Now().UTC(),
		}

		if err := os.WriteFile(consult.AskPath, body, 0o644); err != nil {
			return fmt.Errorf("stage question at %s: %w", consult.AskPath, err)
		}

		b.Consults = append(b.Consults, consult)
		if err := tx.Save(b); err != nil {
			return err
		}
		cwd = b.CWD
		return nil
	})
	return consult, cwd, owner, err
}

// recordConsult is the record phase every consult shares: upsert the record
// and, when it is running, append its log entries. pick is the candidate-pick
// entry a consult chosen from a candidate writes; nil for the round path,
// which resumes a named session rather than resolving one. askNote is the ask
// entry's Note, and the only thing that tells the two apart in the log.
func recordConsult(rt Runtime, name string, consult store.Consult, pick *store.LogEntry, askNote string) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		found := false
		for i, existing := range b.Consults {
			if existing.ID == consult.ID {
				b.Consults[i] = consult
				found = true
				break
			}
		}
		if !found {
			b.Consults = append(b.Consults, consult)
		}

		if consult.State == store.ConsultRunning {
			if pick != nil {
				if err := tx.AppendLog(b.Name, *pick); err != nil {
					return err
				}
			}

			entry := store.LogEntry{
				TS:        rt.Now().UTC(),
				Round:     consult.Round,
				Direction: store.DirToConsult,
				Kind:      store.KindAsk,
				Path:      consult.AskPath,
				Note:      askNote,
				Confirmed: true,
			}
			if err := tx.AppendLog(b.Name, entry); err != nil {
				return err
			}
		}

		return tx.Save(b)
	})
}

// askRound is `ask --round N`: it resumes the session that built closed round
// N, read-only where the harness has a read tier, and asks it one question,
// headless. The round is closed, so the resumed turn cannot race a live
// builder; claude and agy continue that session in place, opencode forks.
// Everything else -- the reservation, the staged question, the ask entry, and
// the findings the headless reconcile delivers -- is the headless consult's:
// the round consult differs in its argv, its role label and its prompt, and
// in nothing else.
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
		return AskResult{}, ErrRunnerUnavailable
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
		newID = randomConsultID
	}
	id := newID()
	agentName := opts.Name + "-" + roundRole + "-" + id
	if err := store.ValidName(agentName); err != nil {
		return AskResult{}, fmt.Errorf(
			"consult agent name %q: %w -- binding %q needs a name of at most %d characters to run %q consults",
			agentName, err, opts.Name, store.MaxAgentNameLen-len("-"+roundRole+"-")-8, roundRole)
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
	prompt := fmt.Sprintf(roundAskPrompt, opts.Round, opts.Name, opts.Round, askPath)
	argv, err := h.Resume(s.ID, prompt, tier)
	if err != nil {
		return AskResult{}, fmt.Errorf("%s session %s: %w", s.Kind, short8(s.ID), err)
	}

	// ── phase 1: reserve ─────────────────────────────── lock held, no external calls
	consult, cwd, owner, err := reserveConsult(rt, opts, id, roundRole,
		store.Endpoint{AgentName: agentName, Kind: s.Kind, SessionID: s.ID}, body)
	if err != nil {
		return AskResult{}, err
	}

	// ── phase 2: spawn ──────────────────────────────────────── no lock held
	var spawnErr error
	streamPath := rt.Store.ConsultStreamPath(opts.Name, consult.Round, consult.ID)
	if handle, err := rt.Runner.Start(ctx, ProcSpec{
		Dir:        cwd,
		Argv:       append([]string{h.Binary}, argv...),
		LogPath:    rt.Store.ConsultLogPath(opts.Name, consult.Round, consult.ID),
		StreamPath: streamPath,
		Scope:      scopeFor(rt, scopeConsult, scopeUnitNameFor(scopeConsult, owner, opts.Name, consult.Round, consult.ID), ""),
	}); err != nil {
		consult.State = store.ConsultSilent
		consult.Note = "spawn failed: " + brief(err)
		spawnErr = fmt.Errorf("start consult %q: %w", consult.Endpoint.AgentName, err)
	} else {
		consult.Endpoint = store.Endpoint{
			AgentName: consult.Endpoint.AgentName,
			SessionID: consult.Endpoint.SessionID,
			Kind:      consult.Endpoint.Kind,
			Mode:      store.ModeHeadless,
			PID:       handle.PID,
			StartedAt: handle.StartedAt.Unix(),
			LogPath:   streamPath,
		}
		consult.State = store.ConsultRunning
	}

	// ── phase 3: record ──────────────────────────────── lock held, no external calls
	saveErr := recordConsult(rt, opts.Name, consult, nil,
		fmt.Sprintf("round %d session %s:%s", opts.Round, s.Kind, short8(s.ID)))
	result := AskResult{Consult: consult, Binding: opts.Name}
	if saveErr != nil {
		if spawnErr != nil {
			return result, strandError(spawnErr, saveErr)
		}
		return result, fmt.Errorf("consult %s is running but could not be recorded: %w", consult.ID, saveErr)
	}

	return result, spawnErr
}

// runningConsults counts ConsultSpawning as well as ConsultRunning. A
// reservation occupies a slot the cap cares about, or two concurrent asks
// would both see it free.
func runningConsults(b store.Binding) int {
	n := 0
	for _, c := range b.Consults {
		if c.State == store.ConsultSpawning || c.State == store.ConsultRunning {
			n++
		}
	}
	return n
}

func consultCap(b store.Binding) int {
	if b.ConsultCap > 0 {
		return b.ConsultCap
	}
	return store.DefaultConsultCap
}

// randomConsultID returns 8 hex characters. A failed CSPRNG read is not a
// reason to refuse a consult: the id only has to be unique within one binding,
// and the clock is sufficient for that.
func randomConsultID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}

// strandError combines the failure that stranded a consult with any failure to
// record it. Returning only the save error hides the half that explains what
// actually went wrong.
func strandError(cause, saveErr error) error {
	if saveErr != nil {
		return fmt.Errorf("%w (also failed to record the consult: %v)", cause, saveErr)
	}
	return cause
}
