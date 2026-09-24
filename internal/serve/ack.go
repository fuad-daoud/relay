package serve

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// settleServed confirms every unconfirmed planner-bound log entry for name
// whose round is at most upTo, using route "ack". It is the one helper the ack
// handler, the done handler and the startup backfill share.
//
// The caller MUST hold the store lock (it is inside rt.Store.WithLock): the
// indices handed to ConfirmIndex are stable only while nothing appends, and
// ConfirmIndex rewrites a line in place without changing the entry count.
//
// upTo <= 0 is a no-op that returns 0: a binding with no closed round and no
// ack has nothing to settle.
func settleServed(tx *store.Tx, name string, upTo int) (int, error) {
	if upTo <= 0 {
		return 0, nil
	}

	entries, err := tx.ReadLog(name)
	if err != nil {
		return 0, fmt.Errorf("settle %s: %w", name, err)
	}

	n := 0
	for i, e := range entries {
		if e.Direction != store.DirToPlanner || e.Confirmed || e.Round > upTo {
			continue
		}
		if err := tx.ConfirmIndex(name, i, "ack"); err != nil {
			return n, fmt.Errorf("settle %s: %w", name, err)
		}
		n++
	}
	return n, nil
}

// settleAllServed backfills the ack older servers never wrote. For every owner
// it settles each served binding's planner-bound log entries up to the round
// the owner acked -- or, for a DONE binding, up to the round the daemon last
// closed, whichever is further.
//
// Pre: the caller holds s.mu, or no request is being served yet.
//
// A per-owner failure is logged at Warn and the walk continues: one bad owner
// must not keep the rest of the server's reports pending forever. The only
// returned error is an unreadable bindings dir; a missing dir returns nil, as
// Tick does.
func (s *Server) settleAllServed() error {
	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		settled := 0
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			bindings, err := tx.List()
			if err != nil {
				return err
			}
			for _, b := range bindings {
				if b.Serve == nil {
					continue
				}
				upTo := b.Serve.AckedRound
				if b.State == store.StateDone && b.Serve.ClosedRound > upTo {
					upTo = b.Serve.ClosedRound
				}
				n, err := settleServed(tx, b.Name, upTo)
				if err != nil {
					return err
				}
				settled += n
			}
			return nil
		})
		if err != nil {
			slog.Warn("settle served reports failed", "owner", id, "err", err)
			continue
		}
		if settled > 0 {
			slog.Info("settled served reports", "owner", id, "entries", settled)
		}
	}
	return nil
}
