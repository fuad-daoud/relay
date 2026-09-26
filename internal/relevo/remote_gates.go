package relevo

import (
	"context"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ForwardUnavailable tells every remote binding with an open round that its
// server-side candidate just hit a usage limit, so that server's own
// reconcile can switch or gate it exactly as a local daemon would (§4.6). It
// never fails the caller: a binding relevo could not reach is named in the
// returned lines instead, and `relevo gate <token>`'s local behaviour (the
// ledger gate) proceeds either way.
func ForwardUnavailable(ctx context.Context, rt Runtime, token, reason string) []string {
	if rt.Remote == nil {
		return nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	var lines []string
	for _, b := range bindings {
		if !b.Builder.Remote() {
			continue
		}

		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: read log: %v", b.Name, err))
			continue
		}
		open := HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
			!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)
		if !open {
			continue
		}

		if err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, token, reason); err != nil {
			lines = append(lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
		}
	}

	return lines
}

// ForwardAvailable tells every server this client's bindings name that a
// rate-limit gate can be lifted, so the server-wide ledger stops gating a
// provider the local ledger just cleared. Every remote binding counts, in any
// state and whether or not its round is open: a gate matters most when nothing
// is running. One call and one answer line per distinct server, sorted, and it
// never fails the caller -- a server relevo could not reach is named in the
// returned lines instead, and `relevo gate --clear`'s local behaviour (the ledger
// clear) proceeds either way.
func ForwardAvailable(ctx context.Context, rt Runtime, subject string) []string {
	if rt.Remote == nil {
		return nil
	}

	// A candidate name or token is forwarded as its canonical token, so the
	// server's own ledger clears the same provider; a bare provider is
	// forwarded as itself (A1 §4.2).
	if rt.Candidates != nil {
		if c, err := rt.Candidates.Resolve(subject); err == nil {
			subject = c.Ref().String()
		}
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return []string{fmt.Sprintf("list bindings: %v", err)}
	}

	var servers []string
	seen := make(map[string]bool)
	for _, b := range bindings {
		if !b.Builder.Remote() || seen[b.Builder.Server] {
			continue
		}
		seen[b.Builder.Server] = true
		servers = append(servers, b.Builder.Server)
	}
	sort.Strings(servers)

	var lines []string
	for _, server := range servers {
		resp, err := rt.Remote.Available(ctx, server, subject)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if resp.Removed == 0 {
			lines = append(lines, fmt.Sprintf("%s: nothing was gating %s", server, resp.Provider))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: cleared %s (%d entries)", server, resp.Provider, resp.Removed))
	}

	return lines
}

// ServerInUse names every binding that names server -- the pure rule behind
// `relevo config server rm`'s refusal (§4.7). A pure function over the
// binding list rather than a store read, so the CLI (cmd/relevo) can be
// tested without touching the network -- the caller loads the bindings and
// this function decides.
func ServerInUse(bindings []store.Binding, server string) []string {
	var names []string
	for _, b := range bindings {
		if b.Builder.Remote() && b.Builder.Server == server {
			names = append(names, b.Name)
		}
	}
	return names
}

// matchCandidateView returns the server view v names, by its canonical Token
// or by its Name. Pure, so `addRemote`'s name matching is testable without a
// server (A1 §4.2).
func matchCandidateView(views []remote.CandidateView, v string) (remote.CandidateView, bool) {
	for _, view := range views {
		if view.Token == v || (view.Name != "" && view.Name == v) {
			return view, true
		}
	}
	return remote.CandidateView{}, false
}
