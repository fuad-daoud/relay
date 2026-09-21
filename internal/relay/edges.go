package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// AddEdge validates e against source and appends it under the store lock,
// minting an id and logging the declaration (#37).
//
// Target must name a different, existing binding; Prompt must be an
// absolute, readable file; Round defaults to source's current round and can
// never be earlier than it -- a closed round can never fire again; When
// must be one of report, diff, done or gate; Then must be "send", the only
// Then this round supports; Mode must be "queue" or "fire", defaulting to
// "queue". AddEdge refuses everything it can check up front so a
// mis-declared edge never silently no-ops.
func AddEdge(ctx context.Context, rt Runtime, source string, e store.Edge) (store.Edge, error) {
	switch e.When {
	case "report", "diff", "done", "gate":
	default:
		return store.Edge{}, fmt.Errorf("edge: --when must be report, diff, done or gate, got %q", e.When)
	}
	if e.Then != "send" {
		return store.Edge{}, fmt.Errorf("edge: --then must be send, got %q", e.Then)
	}
	switch e.Mode {
	case "":
		e.Mode = "queue"
	case "queue", "fire":
	default:
		return store.Edge{}, fmt.Errorf("edge: --mode must be queue or fire, got %q", e.Mode)
	}
	if e.Target == "" {
		return store.Edge{}, errors.New("edge: --target is required")
	}
	if e.Target == source {
		return store.Edge{}, fmt.Errorf("edge: --target %q must not be the source binding", e.Target)
	}
	if !filepath.IsAbs(e.Prompt) {
		return store.Edge{}, fmt.Errorf("edge: --prompt must be an absolute path, got %q", e.Prompt)
	}
	if _, err := os.ReadFile(e.Prompt); err != nil {
		return store.Edge{}, fmt.Errorf("edge: read prompt %s: %w", e.Prompt, err)
	}

	newID := rt.NewID
	if newID == nil {
		newID = randomEdgeID
	}
	id := newID()

	var added store.Edge
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(source)
		if err != nil {
			return fmt.Errorf("edge: source %w", err)
		}
		if _, err := tx.Load(e.Target); err != nil {
			return fmt.Errorf("edge: target %w", err)
		}

		round := e.Round
		if round == 0 {
			round = b.Round
		}
		if round < b.Round {
			return fmt.Errorf("edge: --round %d is before %s's current round %d; a closed round can never fire again", round, source, b.Round)
		}

		added = store.Edge{
			ID:      id,
			Round:   round,
			When:    e.When,
			Then:    e.Then,
			Target:  e.Target,
			Prompt:  e.Prompt,
			Mode:    e.Mode,
			AddedAt: rt.Now().UTC(),
		}
		b.Edges = append(b.Edges, added)

		note := fmt.Sprintf("edge added %s: round %d %s -> send %s (%s)",
			added.ID, added.Round, added.When, added.Target, added.Mode)
		if err := tx.AppendLog(source, store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToPlanner,
			Kind: store.KindEdge, Confirmed: true, Note: note,
		}); err != nil {
			return err
		}

		return tx.Save(b)
	})
	if err != nil {
		return store.Edge{}, err
	}
	return added, nil
}

// ListEdges returns source's declared edges, in declaration order.
func ListEdges(rt Runtime, source string) ([]store.Edge, error) {
	b, err := rt.Store.Load(source)
	if err != nil {
		return nil, err
	}
	return b.Edges, nil
}

// RemoveEdge drops the edge id from source. Removing a fired edge is fine;
// an unknown id is an error.
func RemoveEdge(ctx context.Context, rt Runtime, source, id string) error {
	return rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(source)
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range b.Edges {
			if e.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("edge: no edge %q on %q", id, source)
		}
		b.Edges = append(b.Edges[:idx], b.Edges[idx+1:]...)
		return tx.Save(b)
	})
}

// firePending is one fire-mode edge evaluateEdges armed: ready to Send, but
// not yet run. Send takes the target's own lock, which would nest inside
// the source's if it ran while the source's WithLock was still held, so
// firing happens outside any lock, via runFires.
type firePending struct {
	Source string
	Edge   store.Edge
}

// evaluateEdges resolves every one of b's edges whose Round matches
// closedRound and that has not already fired (#37): if When's artifact is
// missing, the edge is marked Fired with Result "skipped: no <when>" and
// never reconsidered (at close, a round's artifacts are final -- an edge
// that missed its artifact this tick will not find it later). Otherwise a
// queue-mode edge is queued as a DirToPlanner payload naming the exact
// send command and marked Fired with Result "queued"; a fire-mode edge is
// armed -- appended to the returned pendings and marked Result "firing"
// with Fired left false, so a caller that cannot run Send here (Reconcile,
// which the Daemon calls under its own WithLock) can still persist the
// binding, and whoever runs runFires later marks it Fired.
//
// evaluateEdges never calls Send. Reconcile and reconcileHeadless call this
// and discard the pendings return value: production firing happens in the
// Daemon, after the binding is saved, which is the only place a fire-mode
// edge's Send can run without nesting the store lock. Tests that want to
// exercise the fire path call evaluateEdges and then runFires directly.
func evaluateEdges(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, closedRound int) (store.Binding, []firePending, error) {
	var pendings []firePending
	for i := range b.Edges {
		e := b.Edges[i]
		if e.Fired || e.Round != closedRound {
			continue
		}

		path, err := edgeArtifactPath(rt, b.Name, closedRound, e.When)
		if err != nil {
			return b, pendings, err
		}
		if _, err := os.Stat(path); err != nil {
			b.Edges[i].Fired = true
			b.Edges[i].FiredAt = rt.Now().UTC()
			b.Edges[i].Result = "skipped: no " + e.When
			continue
		}

		if e.Mode == "fire" {
			b.Edges[i].Result = "firing"
			pendings = append(pendings, firePending{Source: b.Name, Edge: b.Edges[i]})
			continue
		}

		payload := edgePayload(e.ID, b.Name, closedRound, e.When, e.Target, e.Prompt)
		if err := Queue(ctx, rt, tx, b.Name, store.LogEntry{
			Round: closedRound, Direction: store.DirToPlanner, Kind: store.KindEdge,
			Path: e.Prompt, Payload: payload, Note: "edge " + e.ID + " queued",
		}); err != nil {
			return b, pendings, err
		}
		b.Edges[i].Fired = true
		b.Edges[i].FiredAt = rt.Now().UTC()
		b.Edges[i].Result = "queued"
	}
	return b, pendings, nil
}

// runFires executes every armed fire-mode edge in pendings: Send(ctx, rt,
// target, prompt, SendOptions{}), then settles the result under the
// SOURCE binding's own lock -- never the target's, and never the lock
// evaluateEdges ran under, which has already been released by the time a
// caller has pendings to run. A failed fire never blocks the source's round
// or tick: it is downgraded to the same queue-mode payload evaluateEdges
// would have queued, with the failure named, so the planner still sees the
// handoff. Either way the edge ends Fired, with a Result that has stopped
// changing.
func runFires(ctx context.Context, rt Runtime, pendings []firePending) {
	for _, p := range pendings {
		res, sendErr := Send(ctx, rt, p.Edge.Target, p.Edge.Prompt, SendOptions{})

		err := rt.Store.WithLock(func(tx *store.Tx) error {
			b, err := tx.Load(p.Source)
			if err != nil {
				// The source binding is gone (a concurrent unbind); nothing
				// left to mark.
				return nil
			}
			idx := -1
			for i, e := range b.Edges {
				if e.ID == p.Edge.ID {
					idx = i
					break
				}
			}
			if idx == -1 {
				// The edge itself is gone (a concurrent `relay edge rm`).
				return nil
			}

			if sendErr == nil {
				b.Edges[idx].Fired = true
				b.Edges[idx].FiredAt = rt.Now().UTC()
				b.Edges[idx].Result = fmt.Sprintf("sent round %d to %s", res.Round, p.Edge.Target)
				note := fmt.Sprintf("edge %s fired: sent round %d to %s", p.Edge.ID, res.Round, p.Edge.Target)
				if err := tx.AppendLog(p.Source, store.LogEntry{
					TS: rt.Now().UTC(), Round: b.Edges[idx].Round, Direction: store.DirToPlanner,
					Kind: store.KindEdge, Confirmed: true, Note: note,
				}); err != nil {
					return err
				}
				return tx.Save(b)
			}

			payload := edgePayload(p.Edge.ID, p.Source, p.Edge.Round, p.Edge.When, p.Edge.Target, p.Edge.Prompt) +
				fmt.Sprintf(" (fire failed: %v)", sendErr)
			if err := Queue(ctx, rt, tx, p.Source, store.LogEntry{
				Round: p.Edge.Round, Direction: store.DirToPlanner, Kind: store.KindEdge,
				Path: p.Edge.Prompt, Payload: payload, Note: "edge " + p.Edge.ID + " fire failed, queued",
			}); err != nil {
				return err
			}
			b.Edges[idx].Fired = true
			b.Edges[idx].FiredAt = rt.Now().UTC()
			b.Edges[idx].Result = fmt.Sprintf("fire failed: %v; queued", sendErr)
			return tx.Save(b)
		})
		if err != nil {
			slog.Error("edge fire settle failed", "source", p.Source, "edge", p.Edge.ID, "err", err)
		}
	}
}

// edgeArtifactPath maps an edge's When to the store path evaluateEdges
// checks for existence.
func edgeArtifactPath(rt Runtime, name string, round int, when string) (string, error) {
	switch when {
	case "report":
		return rt.Store.ReportPath(name, round), nil
	case "diff":
		return rt.Store.DiffPath(name, round), nil
	case "done":
		return rt.Store.DonePath(name, round), nil
	case "gate":
		return rt.Store.GateLogPath(name, round), nil
	default:
		return "", fmt.Errorf("edge: unknown when %q", when)
	}
}

// edgePayload is the queue-mode payload text (#37 §1): the held delivery
// the planner sees, naming the exact command that carries the handoff.
func edgePayload(id, source string, round int, when, target, prompt string) string {
	return fmt.Sprintf("relay: edge %s · %s round %d %s is ready · run: relay send --name %s --file %s",
		id, source, round, when, target, prompt)
}

// randomEdgeID returns 6 hex characters, unique within one binding's Edges.
// It mirrors randomConsultID's crypto/rand-with-clock-fallback pattern at a
// shorter length: a failed CSPRNG read is not a reason to refuse an edge.
func randomEdgeID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%06x", uint32(time.Now().UnixNano())&0xffffff)
	}
	return hex.EncodeToString(b[:])
}
