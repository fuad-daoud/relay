package relay

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

// withinTree reports whether cwd is tree itself or is nested under it.
//
// The separator guard is load-bearing: a plain HasPrefix would make
// /repo-doctor look like it is inside /repo, and relay would report a
// second binding's builder as foreign to the first.
//
// Paths are compared literally after Clean. A tree reached through a
// symlink under one name and reported by herdr under another will not
// match, and the agent goes unreported. Resolving that would mean hitting
// the filesystem for every agent on every status call; see the spec.
func withinTree(tree, cwd string) bool {
	if tree == "" || cwd == "" {
		return false
	}
	t := filepath.Clean(tree)
	c := filepath.Clean(cwd)
	if c == t {
		return true
	}
	return strings.HasPrefix(c, t+string(filepath.Separator))
}

// ForeignAgent is a live agent occupying a bound working tree that no binding
// accounts for.
//
// relay cannot observe writes -- `herdr agent list` reports a kind, a status,
// a cwd and a title, and nothing more -- so this type reports OCCUPANCY only.
// A sanctioned read-only researcher and a rogue implementer are identical from
// here. Every field is copied from herdr unmodified; nothing on this type is
// derived, and nothing branches on Title.
type ForeignAgent struct {
	PaneID string `json:"pane_id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	CWD    string `json:"cwd"`
	Title  string `json:"title,omitempty"`
}

// knownEndpoints returns every endpoint relay accounts for, across ALL
// bindings rather than just the one being examined.
//
// The breadth is the point. A second binding's planner may legitimately sit in
// this binding's tree, and a nested worktree's builder is inside its parent's
// path; both are known to relay and neither is foreign. Narrowing this to one
// binding's own two endpoints would report both as findings the user cannot
// act on.
func knownEndpoints(bindings []store.Binding) []store.Endpoint {
	eps := make([]store.Endpoint, 0, len(bindings)*2)
	for _, b := range bindings {
		eps = append(eps, b.Planner, b.Builder)
	}
	return eps
}

// ForeignAgents returns the agents inside tree that match no known endpoint,
// sorted by pane id. It returns nil rather than an empty slice so that
// BindingStatus.Foreign is elided by omitempty.
//
// Identity comparison goes through SameAgent, which is relay's single
// definition of "is this the agent I recorded". Reimplementing it here would
// drift: SameAgent prefers a durable session id, falls back to pane+kind, and
// finally to pane alone for bindings written before sessions were recorded.
func ForeignAgents(agents []herdr.Agent, known []store.Endpoint, tree string) []ForeignAgent {
	if tree == "" {
		return nil
	}

	var out []ForeignAgent
	for _, a := range agents {
		if a.CWD == "" || !withinTree(tree, a.CWD) {
			continue
		}
		if isKnownAgent(a, known) {
			continue
		}
		out = append(out, ForeignAgent{
			PaneID: a.PaneID,
			Kind:   a.Kind,
			Status: a.Status,
			CWD:    a.CWD,
			Title:  a.Title,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out
}

func isKnownAgent(a herdr.Agent, known []store.Endpoint) bool {
	for _, ep := range known {
		if SameAgent(a, ep) {
			return true
		}
	}
	return false
}
