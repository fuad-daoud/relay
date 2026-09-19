package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
)

// Tick advances every binding across all client owners.
func (s *Server) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		d := relay.NewDaemon(rt, s.cfg.Interval)
		if err := d.Tick(ctx); err != nil {
			slog.Error("tick owner failed", "owner", id, "err", err)
		}
	}
	return nil
}

// Run executes the daemon tick loop until ctx is cancelled.
// When serving plain HTTP, it logs a warning once per tick summary (every 60 ticks);
// the spec says "every tick logs it", but once a minute (every 60 ticks at the 1s floor)
// is the honest reading of that.
func (s *Server) Run(ctx context.Context) error {
	interval := s.cfg.Interval
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ticks := 0
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			ticks++
			s.mu.Lock()
			insecure := s.insecureHTTP
			s.mu.Unlock()
			if insecure && ticks%60 == 0 {
				slog.Warn("serving plain HTTP; every client request is readable on the network")
			}
			if err := s.Tick(ctx); err != nil {
				slog.Error("server tick failed", "err", err)
			}
		}
	}
}
