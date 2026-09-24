package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// queuedRound is one queued round the census found, enough to start it: the
// owner's client id and store root, the binding's name, and when it was
// queued -- the FIFO key (#285).
type queuedRound struct {
	Owner     remote.ClientID
	OwnerRoot string // the owner's store root, what runtimeAt takes
	Name      string
	QueuedAt  time.Time
}

// census is the server's builder count at a point in time: how many
// headless builders are running across all owners, and which rounds are
// waiting for a slot, oldest first (#285).
type census struct {
	Running int
	Queued  []queuedRound // sorted by QueuedAt, then Owner, then Name
}

// cap is the builder cap in effect: cfg.MaxBuilders if set, else the
// policy default (#285).
func (s *Server) cap() int {
	if s.cfg.MaxBuilders > 0 {
		return s.cfg.MaxBuilders
	}
	return s.cfg.Policy.MaxBuildersOrDefault()
}

// census walks every owner's bindings, exactly as Tick does, and counts
// running headless builders and queued rounds. A per-owner List error is
// logged and skipped -- the walk continues over the remaining owners -- and
// the first such error is returned after the walk completes; the census
// itself is still usable (#285).
func (s *Server) census() (census, error) {
	var c census
	var firstErr error

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		bindings, err := s.ownerStore(ownerPath).List()
		if err != nil {
			slog.Warn("census: list owner bindings failed", "owner", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		for _, b := range bindings {
			switch {
			case b.Builder.Headless() && b.Builder.PID != 0 && b.State != store.StateDone && b.State != store.StatePaused:
				c.Running++
			case !b.QueuedAt.IsZero() && b.State == store.StateActive:
				c.Queued = append(c.Queued, queuedRound{
					Owner: id, OwnerRoot: ownerPath, Name: b.Name, QueuedAt: b.QueuedAt,
				})
			}
		}
	}

	sort.Slice(c.Queued, func(i, j int) bool {
		a, b := c.Queued[i], c.Queued[j]
		if !a.QueuedAt.Equal(b.QueuedAt) {
			return a.QueuedAt.Before(b.QueuedAt)
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Name < b.Name
	})

	return c, firstErr
}

// admit starts queued rounds, oldest first, while the running count stays
// below cap (#285). The caller holds s.mu: handleStartRound calls it right
// after Send(Defer), and Tick calls it after every owner has been
// reconciled, so the running count census() computed is a fact, not a race.
func (s *Server) admit(ctx context.Context) error {
	c, cerr := s.census()
	running := c.Running
	limit := s.cap()

	for _, q := range c.Queued {
		if running >= limit {
			break
		}
		err := relevo.Admit(ctx, s.runtimeAt(q.OwnerRoot), q.Name)
		switch {
		case err == nil:
			running++
			slog.Info(fmt.Sprintf("admitted owner=%s binding=%s running=%d/%d", q.Owner, q.Name, running, limit))
		case errors.Is(err, relevo.ErrNotQueued):
			continue
		default:
			slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: %v", q.Owner, q.Name, err))
		}
	}

	return cerr
}

// queuePositionView fills a queued round's place in the queue, for
// handleGetBinding and the 201 of handleStartRound (#285): view.RoundState
// has already been computed by relevo.ServedView. Not found in a fresh
// census (raced -- admitted or unbound between the view and this call)
// leaves the result nil, matching BindingView.Queue's contract.
func (s *Server) queuePositionView(b store.Binding, view remote.BindingView, caller remote.ClientID) *remote.QueueView {
	if view.RoundState != remote.RoundQueued {
		return nil
	}
	c, _ := s.census()
	for i, q := range c.Queued {
		if q.Owner == caller && q.Name == b.Name {
			return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: s.cap(), Since: b.QueuedAt}
		}
	}
	return nil
}
