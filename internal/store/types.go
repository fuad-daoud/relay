// Package store owns relay's on-disk state: the bindings and the append-only
// round log. Everything else relay knows is queried live from herdr.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// StatePaused is the third lifecycle state between ACTIVE and DONE:
	// worktree and pane released by `relay pause`; branch and log kept;
	// `relay bind --resume` restores it.
	StatePaused State = "paused"
)

// Mode is the shape of a builder: a herdr pane relay watches, or a process
// relay runs itself (#99). "" reads as pane so every binding written before
// the field existed is unchanged.
type Mode string

const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
	ModeRemote   Mode = "remote"
)

// RepoRef identifies the git repository a binding works in, for the coming
// history database (docs/specs/2026-09-20-persistence-design.md §5.4). It is
// captured best-effort at bind/add/fork; a nil RepoRef means it could not be
// determined and never fails the caller.
type RepoRef struct {
	// OriginURL is the normalised `git remote get-url origin`; "" when the
	// repo has no origin.
	OriginURL string `json:"origin_url,omitempty"`
	// CommonDir is the absolute path of the main worktree's .git
	// (`git rev-parse --git-common-dir`, made absolute).
	CommonDir string `json:"common_dir,omitempty"`
}

// ForkRef records the source binding and round a fork was cut from, as a
// structured field for the history database. It sits beside the existing
// free-text "forked from %s at round %d" log note, which stays exactly as it
// was.
type ForkRef struct {
	Name  string `json:"name"`  // the source binding
	Round int    `json:"round"` // the source round copied through
}

// ValidFeature reports whether s is a valid --feature label: 1..64 bytes,
// every byte in [A-Za-z0-9._ -], no leading or trailing space.
func ValidFeature(s string) error {
	const errText = "feature: 1-64 chars of letters, digits, '.', '_', '-' and spaces"

	if len(s) == 0 || len(s) > 64 {
		return fmt.Errorf("%s", errText)
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return fmt.Errorf("%s", errText)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.', c == '_', c == ' ', c == '-':
		default:
			return fmt.Errorf("%s", errText)
		}
	}
	return nil
}

// Endpoint is one side of a binding. PaneID moves when a pane is moved between
// workspaces; SessionID does not, so it is the durable identity.
//
// A headless builder (#99) has no pane and no session: Mode is ModeHeadless,
// PaneID and SessionID are "", and the process fields below describe the
// current round's process -- PID 0 between rounds. Every one of them is
// omitempty so a pane endpoint's JSON is byte-identical to what it was.
type Endpoint struct {
	AgentName string `json:"agent_name,omitempty"`
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"`

	Mode Mode `json:"mode,omitempty"`
	// PID is the headless supervisor's pid; 0 when no process is running.
	PID int `json:"pid,omitempty"`
	// StartedAt is the process's start time as the OS reports it, in Unix
	// seconds, for pid-reuse defence. Seconds, not time.Time: encoding/json
	// never omits a struct, and the OS reports start time at one-second
	// resolution anyway.
	StartedAt int64 `json:"started_at,omitempty"`
	// LogPath is the current round's builder log (Store.BuilderLogPath);
	// "" between rounds.
	LogPath string `json:"log_path,omitempty"`
	// StreamRound is the round whose builder stream (Store.BuilderStreamPath)
	// the daemon is rendering into that round's log, and StreamOffset how
	// many bytes of it are rendered (#168). They belong to the round's file,
	// not to the process or to Binding.Round: a mid-round switch keeps them,
	// clearProcess keeps them, finishRound's Round++ keeps them, and only
	// startRound on a later round moves them. 0 means no stream was started.
	StreamRound  int   `json:"stream_round,omitempty"`
	StreamOffset int64 `json:"stream_offset,omitempty"`

	// Remote builder endpoint fields
	Server       string `json:"server,omitempty"`
	LastShipped  string `json:"last_shipped,omitempty"`
	LastKnown    string `json:"last_known,omitempty"`
	RemoteStatus string `json:"remote_status,omitempty"`

	// TranscriptLocator is the harness's own transcript file path for this
	// endpoint's session, resolved at bind time when possible. Set on
	// Planner only, for the coming history database
	// (docs/specs/2026-09-20-persistence-design.md §5.4); "" when it could
	// not be resolved.
	TranscriptLocator string `json:"transcript_locator,omitempty"`
}

// Headless reports whether this endpoint is a process relay runs rather than
// a pane it watches. "" is pane.
func (e Endpoint) Headless() bool { return e.Mode == ModeHeadless }

// Remote reports whether this endpoint is hosted on a remote relay server.
func (e Endpoint) Remote() bool { return e.Mode == ModeRemote }

// Binding ties one planner pane to one builder pane over one working tree.
type Binding struct {
	Name    string   `json:"name"`
	CWD     string   `json:"cwd"`
	Planner Endpoint `json:"planner"`
	Builder Endpoint `json:"builder"`
	// BuilderCandidate is the harness/provider/model token the builder was
	// started from (#80); empty for an adopted builder and for any binding
	// written before the field existed.
	BuilderCandidate string `json:"builder_candidate,omitempty"`
	// Tier is the effective permission tier the binding's builder launches at
	// (#141), resolved once at bind/add/fork. "" on bindings written before the
	// field existed and means harness.
	Tier string `json:"tier,omitempty"`
	// RoundTier overrides Tier for the CURRENT round of a headless binding,
	// written by Send --tier and cleared by queueReport with RoundSwitches.
	RoundTier string `json:"round_tier,omitempty"`
	// Gate is the acceptance command relay runs in the worktree when the
	// round's completion marker appears (#132); "" means no gate. Run through
	// `sh -c`, so it may be any shell line. Set at bind/add/fork; never changed
	// by relay.
	Gate string `json:"gate,omitempty"`
	// GateTimeoutMS bounds one gate run; 0 means policy.GateTimeout().
	GateTimeoutMS int `json:"gate_timeout_ms,omitempty"`
	// GateRun is the gate process for the CURRENT round while it runs; nil
	// otherwise. Transient: written when the gate starts, cleared when the
	// round closes.
	GateRun *GateRun `json:"gate_run,omitempty"`

	// Regate is the maximum number of automatic repair rounds relay opens
	// after a failing gate (#132 part 2); 0 means off. Set at bind/add/fork
	// (the policy.json gate.regate default, or an explicit --regate) or by
	// send --regate. A human send or a passing gate resets RepairCount, not
	// this: the budget is a property of the binding.
	Regate int `json:"regate,omitempty"`
	// RepairCount counts the repair rounds started since the last human send
	// or gate pass; compared against Regate. Transient bookkeeping.
	RepairCount int `json:"repair_count,omitempty"`
	// LastGateSig is the sha256 hex of the normalised gate output of the last
	// FAILED gate (#132 part 2): the stall bound compares the next failure
	// against it, so a builder that changed nothing that mattered ends the
	// loop instead of buying another round. "" after a pass or a human send.
	LastGateSig string `json:"last_gate_sig,omitempty"`

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

	// FinishPending is true from the moment Send opens a round until the
	// daemon has raised the one "all rounds finished" toast for this
	// binding's planner (#182). It is deliberately NOT derived from State,
	// for the same reason HaltNotifiedRound is not: a later step in the same
	// tick may rewrite State. A binding written before this field existed
	// has FinishPending == false and never toasts until its next Send.
	FinishPending bool `json:"finish_pending,omitempty"`

	// Halt is why the binding is NEEDS YOU when neither a blocked dialog nor a
	// missing pane explains it, and HaltAt is when relay decided so. Halt is
	// the sentence haltBinding already composes for the toast, minus the
	// leading "<name>: " prefix (the reader knows the name). Written by
	// haltBinding and Send's headless spawn-failure branch; cleared at round
	// close, by a successful Send, and by `bind --resume --rebind`.
	// Meaningful only while State == needs_you; readers check the state
	// first, so a stale value is harmless. A binding written before this
	// field existed has Halt == "".
	Halt   string    `json:"halt,omitempty"`
	HaltAt time.Time `json:"halt_at,omitempty"`

	// RoundSwitches counts builder switches in the current round (#61 step
	// 6). Reset when the round advances. Compared against
	// policy.Policy.SwitchLimit().
	RoundSwitches int `json:"round_switches,omitempty"`

	// RoundExcluded are candidate tokens (canonical ref strings, as
	// BuilderCandidate holds them) that exited without a report during the
	// CURRENT round (#191). switchBuilder folds them into the gate list so a
	// mid-round switch never lands the pick back on a builder that just
	// proved it cannot finish the round; queueReport clears the slice with
	// RoundSwitches when the round advances.
	RoundExcluded []string `json:"round_excluded,omitempty"`

	// BuilderMissingSince is when the daemon first failed to locate the
	// builder during the current absence; zero while it is located. Stamped
	// on the first miss and cleared on any hit, so a detection flicker never
	// accumulates toward a switch.
	BuilderMissingSince time.Time `json:"builder_missing_since,omitempty"`

	// StalledSince is the headless stream's last activity time when the
	// daemon judged the live-but-quiet builder stalled (#252): zero means not
	// stalled. Set/cleared only by reconcileHeadless (and copied from the
	// server view for remote bindings); cleared by Send and round close.
	// relay never acts on it -- killing stays the human's decision.
	StalledSince time.Time `json:"stalled_since,omitempty"`

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
	// RoundBaselineHead is the commit HEAD pointed at when the CURRENT round
	// was sent, written by Send next to RoundBaselineTree and cleared with it
	// by queueReport. Empty means no commit count is possible for the round:
	// a non-git tree, an unborn HEAD, git unavailable, or a round sent before
	// the field existed (#130).
	RoundBaselineHead string `json:"round_baseline_head,omitempty"`
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

	// PlannerScreen is a fingerprint of the planner's visible screen as relay
	// last observed it while HOLDING a payload, and PlannerScreenAt is when the
	// screen was last seen to change. They exist to tell a human who is typing
	// from one who has walked away with the pane focused: the hold ends when the
	// screen has been unchanged for HeldGrace.
	//
	// Both are transient: set only while a payload is held on a focused planner,
	// and cleared by every DeliverPending return that is not such a hold.
	PlannerScreen   string    `json:"planner_screen,omitempty"`
	PlannerScreenAt time.Time `json:"planner_screen_at,omitempty"`

	// HeldGrace is the grace the daemon was running with when it started the
	// PlannerScreen clock. It exists so `relay status`, which runs in another
	// process and never sees `--held-grace`, can print "quiet 23s of 1m0s"
	// rather than a bare duration. Written with PlannerScreen, cleared with it.
	HeldGrace time.Duration `json:"held_grace,omitempty"`

	// preamble_pending (pre-#85) is ignored on load: the role is selected at
	// launch now.

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

	// Branch is the branch relay created for this binding's worktree
	// (relay/<name>), and Base the commit it was cut at. Written once by add
	// and fork; empty for a --cwd binding, an adopted bind, and every
	// bind.json written before the fields existed. Display and provenance
	// today; the branch-integration verbs key on them (#130).
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`

	// ExistingBranch is true when add --branch adopted a branch relay did not
	// create. Informational: every teardown path already leaves branches
	// alone; this records that the branch predates the binding.
	ExistingBranch bool `json:"existing_branch,omitempty"`

	// Repo is the source checkout the worktree was cut from -- the caller's
	// cwd at add/fork time. Empty for a --cwd binding, an adopted bind, and
	// every bind.json written before the field existed; empty disables
	// worktree-escape detection (#192), since there is nothing to compare
	// the worktree's drift against.
	Repo string `json:"repo,omitempty"`

	// RepoRef identifies the git repository this binding works in --
	// normalised origin URL and common dir -- captured best-effort at
	// bind/add/fork (docs/specs/2026-09-20-persistence-design.md §5.4). Note
	// the Go field is RepoRef, not Repo: Repo (above) already exists and is
	// the add/fork source checkout #192's escape detection keys on; the two
	// coexist and nothing in this round reads or writes Repo. Nil when it
	// could not be determined.
	RepoRef *RepoRef `json:"repo_ref,omitempty"`

	// Feature is the human-given label grouping this binding with others
	// (--feature), inherited by fork when none is given. "" means
	// ungrouped. For the coming history database.
	Feature string `json:"feature,omitempty"`

	// NOTE (round 3, #172): the plan for this round asked for a
	// `ForkedFrom *ForkRef` field here (structured source binding + round,
	// json "forked_from") for the coming history database. Binding already
	// has ForkedFrom (string, json "forked_from") and ForkedAtRound (int)
	// above -- the existing free-text provenance pair that
	// internal/relay/fork.go, internal/relay/status.go and
	// internal/ui/rail.go read -- so a second field of the same Go name and
	// JSON tag cannot coexist with it; that is a compile error, not a style
	// choice. None of those three files is in this round's declared file
	// list, so resolving the collision (rename one side, or fold the two
	// into one) is a decision for the plan, not this round. See the round 3
	// report for the halt.

	// Consults are the read-only one-shot agents attached to this binding,
	// running and awaiting-reap alike. omitempty keeps every bind.json written
	// before consults existed byte-identical until its first consult.
	Consults []Consult `json:"consults,omitempty"`

	// ConsultCap bounds RUNNING consults; zero means DefaultConsultCap.
	ConsultCap int `json:"consult_cap,omitempty"`

	// Owner is the enrolled client id that created this binding on a relay
	// server (remote-builders spec §2.1). Empty on every local binding. When
	// set, the binding has no planner: deliverAndSettle leaves payloads queued
	// and the owner collects them over the wire.
	Owner string `json:"owner,omitempty"`

	// Serve is what the server records about the binding beyond the local
	// fields; nil on local bindings.
	Serve *ServeFacts `json:"serve,omitempty"`

	// RemoteUnreachableSince is when the remote server first failed to respond;
	// zero when reachable.
	RemoteUnreachableSince time.Time `json:"remote_unreachable_since,omitempty"`
	// RemoteAbsorbFailures counts consecutive absorb failures from the server.
	RemoteAbsorbFailures int `json:"remote_absorb_failures,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GateRun is the gate process for the CURRENT round while it runs (#132).
type GateRun struct {
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"` // Unix seconds, as Endpoint.StartedAt
	Round     int    `json:"round"`
	Command   string `json:"command"`
}

type ServeFacts struct {
	RepoID       string    `json:"repo_id"`                 // remote.RepoID of the client's repo
	BareRepo     string    `json:"bare_repo"`               // absolute path of the bare repo
	ClosedRound  int       `json:"closed_round,omitempty"`  // last round closed by the daemon; 0 none
	ResultCommit string    `json:"result_commit,omitempty"` // refs/heads/relay/<name> at that close
	DirtyCommit  string    `json:"dirty_commit,omitempty"`  // refs/relay/<name>/round-<ClosedRound>, "" if clean
	AckedRound   int       `json:"acked_round,omitempty"`   // last round the owner acked; 0 none
	LastSeen     time.Time `json:"last_seen,omitempty"`     // last signed request from the owner about this binding
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

	// Role is the role-table name that was asked (`reviewer`), recorded as a
	// name rather than the candidate that ran it: the candidate is the
	// planner's choice at the time, the role is what was intended.
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
