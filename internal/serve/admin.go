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

// RenderClients formats `relay serve clients`: one line per client,
//
//	"<id>  <label>  enrolled YYYY-MM-DD[  revoked YYYY-MM-DD]\n"
//
// in the order given. Empty input prints "no clients\n" -- the same shape
// RenderAdminStatus gives an empty owner list.
func RenderClients(clients []Client) string {
	if len(clients) == 0 {
		return "no clients\n"
	}
	var sb strings.Builder
	for _, cl := range clients {
		line := fmt.Sprintf("%s  %s  enrolled %s", cl.ID, cl.Label, cl.EnrolledAt.Format("2006-01-02"))
		if !cl.RevokedAt.IsZero() {
			line += fmt.Sprintf("  revoked %s", cl.RevokedAt.Format("2006-01-02"))
		}
		sb.WriteString(line + "\n")
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

// AdminUnbind removes a stale server binding: the admin's answer to a
// client's `add --server` whose 409 means the server already holds a
// binding by that name for that client, with no local counterpart to
// resume it (#100). owner resolves by exact client label or exact id; a
// label shared by two clients is refused rather than guessed at. A running
// round is refused unless force is set, matching relay unbind's own
// guardedness; archiving (not deleting) keeps log.jsonl and every round
// file, same as relay unbind --archive.
func AdminUnbind(ctx context.Context, s *Server, owner string, name string, force bool) (relay.UnbindResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.resolveOwner(owner)
	if err != nil {
		return relay.UnbindResult{}, err
	}

	rt, err := s.runtime(id)
	if err != nil {
		return relay.UnbindResult{}, err
	}

	b, err := rt.Store.Load(name)
	if err != nil {
		return relay.UnbindResult{}, err
	}

	if !force {
		logEntries, _ := rt.Store.ReadLog(b.Name)
		if relay.RoundStateOf(b, logEntries) == remote.RoundRunning {
			return relay.UnbindResult{}, fmt.Errorf("round %d is running; wait, or --force", b.Round)
		}
	}

	return relay.Unbind(ctx, rt, name, true)
}

// resolveOwner finds the one client owner names, by exact label or exact
// client id. Two clients sharing a label is an error naming both ids rather
// than a silent pick between them.
func (s *Server) resolveOwner(owner string) (remote.ClientID, error) {
	clients := s.clients.List()

	for _, c := range clients {
		if string(c.ID) == owner {
			return c.ID, nil
		}
	}

	var matches []Client
	for _, c := range clients {
		if c.Label == owner {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", ErrNoSuchClient
	default:
		ids := make([]string, len(matches))
		for i, c := range matches {
			ids[i] = string(c.ID)
		}
		return "", fmt.Errorf("label %q is ambiguous: %s", owner, strings.Join(ids, ", "))
	}
}
