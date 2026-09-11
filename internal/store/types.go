// Package store owns relay's on-disk state: the bindings and the append-only
// round log. Everything else relay knows is queried live from herdr.
package store

import (
	"bytes"
	"encoding/json"
	"time"
)

// State is a binding's display and control state.
type State string

const (
	StateActive   State = "active"    // someone is working
	StateHeld     State = "held"      // payload ready, human is in the planner pane
	StateNeedsYou State = "needs_you" // stalled on a human decision
	StateBroken   State = "broken"    // builder pane is gone
	StateOrphaned State = "orphaned"  // planner session is gone
	StateDone     State = "done"      // planner declared the work verified
)

// Endpoint is one side of a binding. PaneID moves when a pane is moved between
// workspaces; SessionID does not, so it is the durable identity.
type Endpoint struct {
	AgentName string `json:"agent_name,omitempty"`
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"`
}

// Binding ties one planner pane to one builder pane over one working tree.
type Binding struct {
	Name           string    `json:"name"`
	CWD            string    `json:"cwd"`
	Planner        Endpoint  `json:"planner"`
	Builder        Endpoint  `json:"builder"`
	BuilderAlias   string    `json:"builder_alias"`
	Round          int       `json:"round"`
	State          State     `json:"state"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	RoundStartedAt time.Time `json:"round_started_at"`
	// HaltNotifiedRound is the round a halt notification has already been sent
	// for. It is deliberately NOT derived from State: every earlier attempt to
	// dedupe halt notices on State was defeated by a later step in the same
	// tick rewriting State, which turned one notice into one per poll.
	HaltNotifiedRound int `json:"halt_notified_round,omitempty"`
	// RoundBaselineTree is the git tree object the CURRENT round started from,
	// written by Send and consumed (then cleared) when the round's report is
	// queued. Empty means no baseline was captured for this round -- a non-git
	// tree, an unavailable git binary, or a binding created before diff capture
	// existed -- and the round simply produces no diff.
	RoundBaselineTree string `json:"round_baseline_tree,omitempty"`
	// RoundClosedTree is the git tree object the PREVIOUS round ended at,
	// written by queueReport and consumed, then cleared, by the next
	// successful Send. Empty means no drift origin exists and the next send
	// says nothing. It is deliberately not derived from RoundBaselineTree: the
	// two describe different instants, and the round advance clears one while
	// setting the other.
	RoundClosedTree string `json:"round_closed_tree,omitempty"`
	// BuilderScreen is a fingerprint of the builder's terminal as relay last
	// observed it, and BuilderScreenAt is when that observation was taken. They
	// exist to tell a builder that has STOPPED from one that is merely quiet:
	// herdr's idle status means "not currently emitting", which a builder waiting
	// on its own subagents satisfies while very much alive.
	//
	// Both are transient per-round state, written when relay nudges and refreshed
	// whenever the screen is seen to move. queueReport clears them with the round.
	BuilderScreen   string    `json:"builder_screen,omitempty"`
	BuilderScreenAt time.Time `json:"builder_screen_at,omitempty"`

	// PreamblePending forces the builder alias's preamble onto the next plan even
	// when the round is not 1. A replacement builder is a NEW agent session that
	// has never seen the preamble, and for harnesses with no role flag -- agy --
	// that preamble is the only thing that selects the builder's role. Without
	// this, a rebound builder would silently run the round as a plain assistant.
	//
	// It defaults false, so no existing binding's behaviour changes: round 1 keeps
	// its own unconditional preamble.
	PreamblePending bool `json:"preamble_pending,omitempty"`

	// Worktree is the git worktree RELAY created for this binding, and is therefore
	// the only directory relay may ever remove. Empty for every binding relay did
	// not create a tree for -- including a fork bound to a directory the human
	// supplied. Never infer ownership from the path.
	Worktree string `json:"worktree,omitempty"`

	// ForkedFrom is the binding this one was forked from, for provenance only.
	// Nothing reads it to make a decision: a fork is an ordinary binding the
	// moment it exists, and the source may be unbound while the fork runs on.
	ForkedFrom string `json:"forked_from,omitempty"`

	// ForkedAtRound is the source round this binding's history was copied through.
	ForkedAtRound int `json:"forked_at_round,omitempty"`

	// Consults are the read-only one-shot agents attached to this binding,
	// running and awaiting-reap alike. omitempty keeps every bind.json written
	// before consults existed byte-identical until its first consult.
	Consults []Consult `json:"consults,omitempty"`

	// ConsultCap bounds RUNNING consults; zero means DefaultConsultCap.
	ConsultCap int `json:"consult_cap,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultConsultCap bounds how many consults may be RUNNING on one binding at
// once. It exists for the reason RoundCap does: an idle harness pane holds
// roughly 800 MB, so an unbounded fan-out is a memory failure, not a workspace.
const DefaultConsultCap = 8

// ConsultState is where one consult has got to.
//
// spawning and running are non-terminal and both occupy a cap slot; only done
// and silent are reapable; a finished record is kept because it is the reap
// worklist.
type ConsultState string

const (
	ConsultSpawning ConsultState = "spawning" // slot reserved; no pane yet
	ConsultRunning  ConsultState = "running"  // spawned; no findings yet
	ConsultDone     ConsultState = "done"     // findings queued to the planner
	ConsultSilent   ConsultState = "silent"   // gave up; "no findings" reported
)

// Consult is one ephemeral, read-only, one-shot agent attached to a binding.
//
// It is deliberately not a Binding. A consult has no round counter, no diff
// baseline, no worktree, no screen fingerprint and no persistent session, and
// modelling it as a Binding would leave every one of those fields dead while
// forcing Status, gc, Fork, doctor and the UI to learn to filter it out.
//
// "Read-only" describes how the role is configured, not something relay
// enforces: `herdr agent list` reports a kind, a status, a cwd and a title, and
// nothing more, so relay cannot observe writes.
type Consult struct {
	// ID is 8 lowercase hex characters, unique within one binding. It appears
	// in the log, in both filenames, and in `relay reap`, so it is short enough
	// to read aloud.
	ID string `json:"id"`

	// Role is the alias name that was asked, recorded as a name rather than a
	// resolved spec: the spec can change under the record, and the record
	// should stay truthful about what was intended.
	Role string `json:"role"`

	Endpoint Endpoint `json:"endpoint"`

	// Round is the owning binding's round at spawn. It is a label for audit and
	// filenames and nothing else -- a consult never advances a round, never
	// stamps RoundStartedAt, and never touches a diff baseline.
	Round int `json:"round"`

	AskPath      string `json:"ask_path"`
	FindingsPath string `json:"findings_path"`

	State ConsultState `json:"state"`

	SpawnedAt time.Time `json:"spawned_at"`

	// NudgedAt is when relay sent this consult its single nudge; zero means it
	// has not been nudged. A consult runs for a minute or two, so it does not
	// inherit the builder's screen-fingerprint quiescence or scrape fallback.
	// (no omitempty: encoding/json never omits a struct, so the option read as
	// a promise the zero time would vanish from bind.json. It never did.)
	NudgedAt time.Time `json:"nudged_at"`

	// Note is why a silent consult gave up. Empty for running and done.
	Note string `json:"note,omitempty"`
}

// SameBinding reports whether two bindings hold the same state, by comparing
// their serialised forms.
//
// Binding stopped being comparable with == when Consults, a slice, was added.
// The daemon's per-tick short-circuit needs an equality test that survives that
// and every field added later.
//
// A hand-written field-by-field Equal was rejected: it keeps compiling when a
// field is added and silently stops noticing changes to it, which is the same
// bug with no compiler error to catch it. Comparing the serialised form asks
// what the caller means -- would this write a different bind.json -- and cannot
// drift as fields come and go.
//
// A marshal error reports "not the same", so a caller saves rather than
// skipping a write it needed.
func SameBinding(a, b Binding) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}

	return bytes.Equal(ra, rb)
}
