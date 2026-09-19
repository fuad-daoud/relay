package serve

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

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
