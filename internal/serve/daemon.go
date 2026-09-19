package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// Tick advances every binding across all client owners.
func (s *Server) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	if _, err := os.Stat(bindingsDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, cl := range s.clients.List() {
		rt := s.runtime(cl.ID)
		d := relay.NewDaemon(rt, s.cfg.Interval)
		if err := d.Tick(ctx); err != nil {
			slog.Error("tick owner failed", "owner", cl.ID, "err", err)
		}
	}
	return nil
}

// Run executes the daemon tick loop until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	interval := s.cfg.Interval
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				slog.Error("server tick failed", "err", err)
			}
		}
	}
}
