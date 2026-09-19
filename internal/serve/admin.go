package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
)

type OwnerStatus struct {
	Owner  remote.ClientID
	Label  string       // Clients.LabelOf
	Report relay.Report // relay.Status over that owner's runtime
}

// AdminStatus returns the status of every owner who has a bindings directory, sorted by Label.
func AdminStatus(ctx context.Context, s *Server) ([]OwnerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var owners []OwnerStatus
	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)
		rep, err := relay.Status(ctx, rt)
		if err != nil {
			return nil, err
		}
		label := s.clients.LabelOf(id)
		owners = append(owners, OwnerStatus{
			Owner:  id,
			Label:  label,
			Report: rep,
		})
	}

	sort.Slice(owners, func(i, j int) bool {
		return owners[i].Label < owners[j].Label
	})

	return owners, nil
}

// RenderAdminStatus formats the admin status for all owners.
// For each owner: a header line "<label>  (<id>)" then relay.RenderStatus(report)
// indented two spaces; owners with no bindings print "<label>  no bindings".
// Empty input prints "no owners\n".
func RenderAdminStatus(owners []OwnerStatus) string {
	if len(owners) == 0 {
		return "no owners\n"
	}

	var sb strings.Builder
	for _, o := range owners {
		if len(o.Report.Bindings) == 0 {
			fmt.Fprintf(&sb, "%s  no bindings\n", o.Label)
			continue
		}
		fmt.Fprintf(&sb, "%s  (%s)\n", o.Label, string(o.Owner))
		rendered := relay.RenderStatus(o.Report)
		trimmed := strings.TrimRight(rendered, "\n")
		for _, line := range strings.Split(trimmed, "\n") {
			if line == "" {
				sb.WriteString("\n")
			} else {
				sb.WriteString("  " + line + "\n")
			}
		}
	}
	return sb.String()
}

type GCAbandonedResult struct {
	Owner    remote.ClientID
	Label    string
	Name     string
	LastSeen time.Time
	Archive  string // path, "" on dry run
}

// GCAbandoned unbinds (archives) owned bindings that are older than olderThan and not running.
// If dryRun is true, it only lists what would be archived without actually unbinding.
func GCAbandoned(ctx context.Context, s *Server, olderThan time.Duration, now time.Time, dryRun bool) ([]GCAbandonedResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	cutoff := now.Add(-olderThan)
	var results []GCAbandonedResult

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			continue
		}
		label := s.clients.LabelOf(id)
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		bindings, err := rt.Store.List()
		if err != nil {
			return nil, err
		}

		for _, b := range bindings {
			lastSeen := b.RoundStartedAt
			if b.Serve != nil && !b.Serve.LastSeen.IsZero() {
				lastSeen = b.Serve.LastSeen
			}
			if !lastSeen.Before(cutoff) {
				continue
			}

			logEntries, _ := rt.Store.ReadLog(b.Name)
			if relay.RoundStateOf(b, logEntries) == remote.RoundRunning {
				continue
			}

			archivePath := ""
			if !dryRun {
				res, err := relay.Unbind(ctx, rt, b.Name, true)
				if err != nil {
					return nil, err
				}
				archivePath = res.ArchivedTo
			}

			results = append(results, GCAbandonedResult{
				Owner:    id,
				Label:    label,
				Name:     b.Name,
				LastSeen: lastSeen,
				Archive:  archivePath,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Label != results[j].Label {
			return results[i].Label < results[j].Label
		}
		return results[i].Name < results[j].Name
	})

	return results, nil
}
