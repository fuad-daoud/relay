package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// ErrNotAConsultRole reports an ask naming an alias that is a builder. A
// builder is persistent and writes; handing one a prompt with no plan in it
// would start a round that never was.
var ErrNotAConsultRole = errors.New("that alias is a builder, not a consult role")

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
// Postconditions: on success a ConsultRunning record exists on the binding, the
//
//	question is staged at AskPath, one DirToConsult/KindAsk entry
//	is logged, and a pane is running the role. On a VALIDATION
//	failure nothing was spawned. On a SPAWN failure after the pane
//	exists, a ConsultSilent record is written so `relay reap` can
//	close the pane rather than stranding it.
//
// Errors: ErrNotAConsultRole, ErrTreelessUnsupported, ErrConsultCap,
//
//	alias.ErrUnknownAlias, store.ErrNotFound, or a wrapped herdr failure.
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

	spec, err := rt.Aliases.Lookup(opts.Role)
	if err != nil {
		return AskResult{}, err
	}
	if !spec.IsConsult() {
		return AskResult{}, fmt.Errorf("%q is a builder alias: %w", opts.Role, ErrNotAConsultRole)
	}
	if spec.Tree == "none" {
		return AskResult{}, fmt.Errorf("role %q declares tree \"none\": %w", opts.Role, ErrTreelessUnsupported)
	}

	newID := rt.NewID
	if newID == nil {
		newID = randomConsultID
	}

	var out store.Consult

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

		id := newID()
		c := store.Consult{
			ID:           id,
			Role:         spec.Name,
			Round:        b.Round,
			AskPath:      rt.Store.AskPath(b.Name, b.Round, id),
			FindingsPath: rt.Store.FindingsPath(b.Name, b.Round, id),
			State:        store.ConsultRunning,
			SpawnedAt:    rt.Now().UTC(),
		}

		if err := os.WriteFile(c.AskPath, body, 0o644); err != nil {
			return fmt.Errorf("stage question at %s: %w", c.AskPath, err)
		}

		agentName := b.Name + "-" + spec.Name + "-" + id
		pane, err := consultPane(ctx, rt, opts, b.CWD, agentName)
		if err != nil {
			return err
		}
		c.Endpoint = store.Endpoint{AgentName: agentName, PaneID: pane, Kind: spec.Kind}

		// From here the pane exists. Every failure records the consult as
		// silent so `relay reap` can close it; returning the bare error would
		// strand a pane with nothing pointing at it.
		strand := func(reason string, cause error) error {
			c.State, c.Note = store.ConsultSilent, reason+": "+brief(cause)
			b.Consults = append(b.Consults, c)
			if err := tx.Save(b); err != nil {
				return err
			}
			out = c
			return cause
		}

		if err := rt.Herdr.StartAgent(ctx, agentName, spec.Kind, pane, spec.Args); err != nil {
			return strand("start failed", fmt.Errorf("start consult %q: %w", agentName, err))
		}

		// Best effort, exactly as resolveBuilder does it: the lookup races the
		// agent's registration, and failing here would strand a live pane over
		// an id Reconcile backfills on a later tick.
		if agents, err := rt.Herdr.ListAgents(ctx); err == nil {
			if started, ok := FindAgent(agents, store.Endpoint{PaneID: pane}); ok {
				c.Endpoint.SessionID = started.Session.Value
			}
		}

		text := spec.Preamble
		if text != "" {
			text += "\n\n"
		}
		text += fmt.Sprintf(consultPrompt, c.AskPath, c.FindingsPath)

		if err := promptWithRetry(ctx, rt, pane, text); err != nil {
			return strand("prompt failed", fmt.Errorf("prompt consult: %w", err))
		}

		entry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     c.Round,
			Direction: store.DirToConsult,
			Kind:      store.KindAsk,
			Path:      c.AskPath,
			Note:      spec.Name + " " + id,
			Confirmed: true,
		}
		if err := tx.AppendLog(b.Name, entry); err != nil {
			return err
		}

		b.Consults = append(b.Consults, c)
		if err := tx.Save(b); err != nil {
			return err
		}

		out = c
		return nil
	})
	if err != nil {
		return AskResult{Consult: out, Binding: opts.Name}, err
	}

	return AskResult{Consult: out, Binding: opts.Name}, nil
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

// runningConsults counts only the consults still working. A terminal record is
// waiting to be reaped and is not occupying a pane slot the cap cares about --
// it is occupying a pane, but one the human has been told to reap.
func runningConsults(b store.Binding) int {
	n := 0
	for _, c := range b.Consults {
		if c.State == store.ConsultRunning {
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
