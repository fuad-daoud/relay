package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// ErrNotAConsultRole reports an ask naming an alias that is a builder. A
// builder is persistent and writes; handing one a prompt with no plan in it
// would start a round that never was.
var ErrNotAConsultRole = errors.New("that role is the builder role; bind it with relay bind, not relay ask")

// ErrTreelessUnsupported reports an ask for a role declaring tree "none".
// The field is forward-declared for a future treeless explorer; no treeless
// role ships yet, and silently running one in the binding's tree would put an
// agent somewhere its role did not ask for.
var ErrTreelessUnsupported = errors.New("treeless consult roles are not implemented")

// ErrConsultCap reports an ask that would exceed the binding's running-consult
// cap. An idle harness pane holds roughly 800 MB.
var ErrConsultCap = errors.New("binding is at its consult cap")

// consultPrompt is what relay types into a freshly spawned consult.
//
// The last line is an instruction to the model, not a constraint relay
// enforces: relay cannot observe writes.
const consultPrompt = `Read: %s

Write your findings to: %s

Reply here with only that path. Do not modify any file in this repository.`

// AskOptions describes one consult request.
type AskOptions struct {
	Role        string // consult role to spawn; required
	Candidate   string // a harness/provider/model token; empty resolves through the one rule in resolveCandidate
	File        string // the question file; required
	Name        string // binding name, already resolved by the caller
	PlannerPane string // $HERDR_PANE_ID; required
	NewTab      bool
	WorkspaceID string
}

// AskResult is what an ask produced, so the CLI can tell the planner where the
// findings will appear without re-deriving the path.
type AskResult struct {
	Consult store.Consult
	Binding string
}

// Ask spawns one read-only, one-shot consult beside a binding's builder and
// returns immediately. The daemon watches for its findings.
//
// Preconditions:  opts.PlannerPane names a live agent pane; opts.File is
//
//	readable; opts.Role resolves to a consult spec whose Tree is
//	not "none"; the binding exists and is neither broken nor done;
//	running consults are below the binding's cap.
//
// Postconditions: on a validation failure nothing on disk changed and nothing
//
//	was spawned. On a reservation failure no record is written and no
//	pane exists; ErrConsultCap is raised here and only here. On success
//	exactly one record for the id exists, State is running,
//	Endpoint.PaneID is set, and the ask is logged with Confirmed: true.
//	On a spawn failure after a pane exists, exactly one record for the
//	id exists, State is silent, Endpoint.PaneID is set, and Note names
//	the failure, so `relay reap` can close it. On a spawn failure before
//	a pane exists (SplitPane/CreateTab itself failed), exactly one record
//	for the id exists, State is silent, Endpoint.PaneID is empty, and
//	Note names the failure, so `relay reap` drops it without a close.
//
// Errors: ErrUnknownRole, ErrNotAConsultRole, ErrNoCandidates, ErrRoleNotServed,
//
//	ErrAmbiguousCandidate, candidate.ErrUnknownCandidate,
//	ErrTreelessUnsupported, ErrConsultCap, store.ErrNotFound, a wrapped
//	herdr failure, or a wrapped store error from either phase.
func Ask(ctx context.Context, rt Runtime, opts AskOptions) (AskResult, error) {
	if opts.PlannerPane == "" {
		return AskResult{}, errors.New("no planner pane; is HERDR_PANE_ID set")
	}

	// Read the caller's file before taking the lock; it is the one input that
	// does not depend on binding state.
	body, err := os.ReadFile(opts.File)
	if err != nil {
		return AskResult{}, fmt.Errorf("read question %s: %w", opts.File, err)
	}

	role, ok := harness.RoleByName(opts.Role)
	if !ok {
		return AskResult{}, fmt.Errorf("unknown role %q (known: %v): %w", opts.Role, harness.RoleNames(), ErrUnknownRole)
	}
	if role.Shape != harness.ShapeConsult {
		return AskResult{}, fmt.Errorf("%q: %w", opts.Role, ErrNotAConsultRole)
	}
	c, err := resolveCandidate(rt.Candidates, opts.Candidate, opts.Role)
	if err != nil {
		return AskResult{}, err
	}
	if c.Tree == "none" {
		return AskResult{}, fmt.Errorf("candidate %q declares tree \"none\": %w", c.Ref().String(), ErrTreelessUnsupported)
	}
	h, _ := harness.Lookup(c.Harness)
	l := h.Launch(c.Provider, c.Model, c.ExtraArgs, role)

	newID := rt.NewID
	if newID == nil {
		newID = randomConsultID
	}

	// Mint the id and compose the agent name before the lock, so a name herdr
	// would refuse fails as a validation error before the reservation is
	// written or the question staged: nothing to clean up.
	id := newID()
	agentName := opts.Name + "-" + role.Name + "-" + id
	if err := herdr.ValidateAgentName(agentName); err != nil {
		return AskResult{}, fmt.Errorf(
			"consult agent name %q: %w -- binding %q needs a name of at most %d characters to run %q consults",
			agentName, err, opts.Name, herdr.MaxAgentNameLen-len("-"+role.Name+"-")-8, role.Name)
	}

	// ── phase 1: reserve ─────────────────────────────── lock held, no herdr calls
	var (
		consult store.Consult
		cwd     string
	)
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
		if err != nil {
			return err
		}
		if b.State == store.StateBroken || b.State == store.StateDone {
			return fmt.Errorf("binding %q is %s; a consult needs a live binding to attach to", b.Name, b.State)
		}
		if runningConsults(b) >= consultCap(b) {
			return fmt.Errorf("binding %q has %d running consults (cap %d), `relay reap` to free a slot: %w",
				b.Name, runningConsults(b), consultCap(b), ErrConsultCap)
		}

		consult = store.Consult{
			ID:           id,
			Role:         role.Name,
			Round:        b.Round,
			AskPath:      rt.Store.AskPath(b.Name, b.Round, id),
			FindingsPath: rt.Store.FindingsPath(b.Name, b.Round, id),
			Endpoint:     store.Endpoint{AgentName: agentName, Kind: l.Kind},
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
	if err != nil {
		return AskResult{}, err
	}

	// ── phase 2: spawn ──────────────────────────────────────── no lock held
	var spawnErr error
	pane, err := consultPane(ctx, rt, opts, cwd, consult.Endpoint.AgentName)
	if err != nil {
		consult.State = store.ConsultSilent
		consult.Note = "split failed: " + brief(err)
		spawnErr = err
	} else {
		consult.Endpoint.PaneID = pane
		// IRREVERSIBLE: a pane may now exist. Never closed by relay.
		if err := rt.Herdr.StartAgent(ctx, consult.Endpoint.AgentName, l.Kind, pane, l.Args); err != nil {
			consult.State = store.ConsultSilent
			consult.Note = "start failed: " + brief(err)
			spawnErr = fmt.Errorf("start consult %q: %w", consult.Endpoint.AgentName, err)
		} else {
			text := l.Preamble
			if text != "" {
				text += "\n\n"
			}
			text += fmt.Sprintf(consultPrompt, consult.AskPath, consult.FindingsPath)

			if err := promptWithRetry(ctx, rt, pane, text); err != nil {
				consult.State = store.ConsultSilent
				consult.Note = "prompt failed: " + brief(err)
				spawnErr = fmt.Errorf("prompt consult: %w", err)
			} else {
				consult.State = store.ConsultRunning
			}
		}
	}

	// ── phase 3: record ──────────────────────────────── lock held, no herdr calls
	saveErr := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(opts.Name)
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
			entry := store.LogEntry{
				TS:        rt.Now().UTC(),
				Round:     consult.Round,
				Direction: store.DirToConsult,
				Kind:      store.KindAsk,
				Path:      consult.AskPath,
				Note:      role.Name + " " + consult.ID,
				Confirmed: true,
			}
			if err := tx.AppendLog(b.Name, entry); err != nil {
				return err
			}
		}

		return tx.Save(b)
	})
	if saveErr != nil {
		if spawnErr != nil {
			return AskResult{Consult: consult, Binding: opts.Name}, strandError(spawnErr, saveErr)
		}
		return AskResult{Consult: consult, Binding: opts.Name}, fmt.Errorf("consult %s is running in pane %s but could not be recorded: %w", consult.ID, consult.Endpoint.PaneID, saveErr)
	}

	return AskResult{Consult: consult, Binding: opts.Name}, spawnErr
}

// consultPane makes somewhere for the consult to live: its own tab when asked,
// otherwise a sibling pane beside the planner. Focus stays with the planner
// either way. It mirrors builderPane; they are kept separate because a consult
// takes its cwd from the binding rather than from bind options.
func consultPane(ctx context.Context, rt Runtime, opts AskOptions, cwd, agentName string) (string, error) {
	if opts.NewTab {
		pane, err := rt.Herdr.CreateTab(ctx, opts.WorkspaceID, cwd, agentName)
		if err != nil {
			return "", fmt.Errorf("create tab for consult: %w", err)
		}
		return pane, nil
	}

	pane, err := rt.Herdr.SplitPane(ctx, opts.PlannerPane, splitDirection, cwd)
	if err != nil {
		return "", fmt.Errorf("split pane for consult: %w", err)
	}
	return pane, nil
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
