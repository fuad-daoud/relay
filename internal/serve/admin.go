package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

type OwnerStatus struct {
	Owner  remote.ClientID
	Label  string       // Clients.LabelOf
	Report relay.Report // relay.Status over that owner's runtime
}

// AdminStatus returns the status of every owner who has a bindings
// directory, sorted by Label, plus the server's builder census (#285):
// running and queued counts against cap. Every queued row's BindingStatus
// gains Queued (the same position handleGetBinding computes for the wire)
// and its BuilderStatus is overwritten from "idle" to "queued <age> (<ahead>
// ahead)", read from the one census() walk this call makes -- RenderAdminStatus
// prints that text unchanged, and FlatStatus's flattened rows carry it too.
func AdminStatus(ctx context.Context, s *Server) ([]OwnerStatus, remote.BuildersView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	builders := remote.BuildersView{Cap: s.cap()}

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, builders, nil
		}
		return nil, remote.BuildersView{}, err
	}

	c, _ := s.census()
	builders.Running = c.Running
	builders.Queued = len(c.Queued)

	now := s.cfg.Now()
	position := make(map[string]int, len(c.Queued))
	for i, q := range c.Queued {
		position[string(q.Owner)+"/"+q.Name] = i
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
			return nil, remote.BuildersView{}, err
		}
		for ri := range rep.Bindings {
			row := &rep.Bindings[ri]
			i, ok := position[string(id)+"/"+row.Name]
			if !ok {
				continue
			}
			q := c.Queued[i]
			row.Queued = &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: builders.Cap, Since: q.QueuedAt}
			row.BuilderStatus = fmt.Sprintf("queued %s (%d ahead)", relay.AgeText(now.Sub(q.QueuedAt)), i)
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

	return owners, builders, nil
}

// FlatStatus is the whole fleet as one report: every owner's bindings with
// Owner/OwnerLabel stamped on each row. Owners arrive label-sorted from
// AdminStatus and their rows keep their Report order, so a client's cards
// stay contiguous. Gated is the first owner's slice -- the server-wide
// ledger projects identically into every owner's report -- and is nil when
// there are no owners. DoneHidden is 0 and HerdrError empty: neither
// filter applies to a flattened fleet.
func FlatStatus(ctx context.Context, s *Server) (relay.Report, error) {
	owners, _, err := AdminStatus(ctx, s)
	if err != nil {
		return relay.Report{}, err
	}

	rows := make([]relay.BindingStatus, 0)
	for _, o := range owners {
		label := o.Label
		if label == "" {
			label = relay.ShortOwner(string(o.Owner))
		}
		for _, row := range o.Report.Bindings {
			row.Owner = string(o.Owner)
			row.OwnerLabel = label
			rows = append(rows, row)
		}
	}

	out := relay.Report{Bindings: rows}
	if len(owners) > 0 {
		out.Gated = owners[0].Report.Gated
	}
	return out, nil
}

// RenderAdminStatus formats the admin status for all owners. The first line
// is the server's builder census (#285): "builders <running>/<cap>, queued
// <n>". Then, for each owner: a header line "<label>  (<id>)" then
// relay.RenderStatus(report) indented two spaces; owners with no bindings
// print "<label>  no bindings". "no owners\n" follows the census line when
// there are none.
func RenderAdminStatus(owners []OwnerStatus, builders remote.BuildersView) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "builders %d/%d, queued %d\n", builders.Running, builders.Cap, builders.Queued)

	if len(owners) == 0 {
		sb.WriteString("no owners\n")
		return sb.String()
	}

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
			switch relay.RoundStateOf(b, logEntries) {
			case remote.RoundRunning, remote.RoundQueued:
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

// ledgerRuntime is the runtime the server-side gate verbs run on: the one
// server-wide ledger at <root>/ledger.json, not any owner's. Its store is
// over the serve root only because relay's ledger mutation takes its lock
// through rt.Store; these verbs never list bindings from it.
//
// store.New creates nothing on its own -- the root and its .lock file appear
// only once WithLock runs -- and neither is one of Initialised's markers
// (clients.json, server.key, bindings), so locking through this store cannot
// make an uninitialised root report as initialised.
func ledgerRuntime(s *Server) relay.Runtime {
	return relay.Runtime{
		Candidates:       s.cfg.Candidates,
		Policy:           s.cfg.Policy,
		Store:            store.New(s.cfg.Root),
		LedgerPath:       filepath.Join(s.cfg.Root, "ledger.json"),
		AvailabilityPath: filepath.Join(s.cfg.Root, "availability.json"),
		Now:              s.cfg.Now,
		Herdr:            stubHerdr{},
	}
}

// AdminGates lists the server-wide ledger's live gates, projected onto the
// configured candidates. Nil candidates means there is nothing to project
// onto, which relay.Gates already answers as nil.
func AdminGates(s *Server) []ledger.Gate {
	return relay.Gates(ledgerRuntime(s))
}

// AdminAvailable clears every rate-limit gate on subject's provider in the
// server-wide ledger.
func AdminAvailable(s *Server, subject string) (provider string, removed int, err error) {
	return relay.Available(ledgerRuntime(s), subject)
}

// AdminUnavailable records a rate-limit gate on token's provider in the
// server-wide ledger.
func AdminUnavailable(s *Server, token string, until time.Time, reason string) (provider string, err error) {
	return relay.Unavailable(ledgerRuntime(s), token, until, reason)
}

// RenderGates formats `relay serve gates`: one line per gate,
//
//	"<token>  <kind>  <until>  <note>\n"
//
// in the order given, using the same wording status, candidates and doctor
// use (GateKindText, GateUntilText). Empty input prints "no gates\n", the
// shape RenderAdminStatus and RenderClients give an empty list.
func RenderGates(gates []ledger.Gate, now time.Time) string {
	if len(gates) == 0 {
		return "no gates\n"
	}

	var sb strings.Builder
	for _, g := range gates {
		fmt.Fprintf(&sb, "%s  %s  %s  %s\n", g.Token, relay.GateKindText(g.Kind), relay.GateUntilText(g.Until), g.Note)
	}
	return sb.String()
}
