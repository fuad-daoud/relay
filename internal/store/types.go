// Package store owns relevo's on-disk state: the bindings and the append-only
// round log.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

// State is a binding's display and control state.
type State string

const (
	StateActive   State = "active"    // someone is working
	StateNeedsYou State = "needs_you" // stalled on a human decision
	StateBroken   State = "broken"    // the builder process is gone
	StateDone     State = "done"      // planner declared the work verified
	// StatePaused is the third lifecycle state between ACTIVE and DONE:
	// worktree released by `relevo pause`; branch and log kept;
	// `relevo bind --resume` restores it.
	StatePaused State = "paused"
)

// Mode is the shape of a builder: a process relevo runs itself (#99), or one
// hosted on a remote relevo server. "" is a pre-#303 pane binding, kept
// loadable so an old bind.json still reads back unchanged.
type Mode string

const (
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

// Rusage is what the supervisor measured for a round's systemd scope
// (#244, #216): the cgroup's cpu.stat usage_usec and memory.peak, read
// after the builder exits.
type Rusage struct {
	CPUMS        int64 `json:"cpu_ms,omitempty"`
	PeakMemBytes int64 `json:"peak_mem_bytes,omitempty"`
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

// StreamSegment is one process's byte range in a round's builder stream: the
// offset the stream had when the process was spawned (the endpoint's
// StreamStart) and the harness kind that wrote from there on. See
// Endpoint.StreamSegments.
type StreamSegment struct {
	// Start is the byte offset in the round's builder stream where this
	// process's output begins. It equals the stream's size when the process
	// was spawned (StreamStart).
	Start int64 `json:"start"`
	// Kind is the harness kind of that process, i.e. Endpoint.Kind at spawn.
	Kind string `json:"kind"`
}

// Endpoint is one side of a binding. PaneID moves when a pane is moved between
// workspaces; SessionID does not, so it is the durable identity.
//
// A headless builder (#99) has no session: Mode is ModeHeadless, SessionID
// is "", and the process fields below describe the current round's process
// -- PID 0 between rounds. Every one of them is omitempty so an endpoint
// written before these fields existed is byte-identical to what it was.
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
	// LogPath is the file the current process's stderr is appended to: the
	// round's stream (Store.BuilderStreamPath), or, for a round that already
	// had a NNN-builder.log when the process started, that log; "" between
	// rounds.
	LogPath string `json:"log_path,omitempty"`
	// StreamRound is the round whose builder stream (Store.BuilderStreamPath)
	// the daemon is rendering into that round's log, and StreamOffset how
	// many bytes of it are rendered (#168). They belong to the round's file,
	// not to the process or to Binding.Round: switchBuilder's carryStream
	// moves them onto the replacement endpoint, clearProcess keeps them,
	// finishRound's Round++ keeps them, and only startRound on a later round
	// moves them. 0 means no stream was started.
	StreamRound  int   `json:"stream_round,omitempty"`
	StreamOffset int64 `json:"stream_offset,omitempty"`
	// StreamStart is the byte length of the round's stream
	// (Store.BuilderStreamPath(name, round)) when this process was spawned.
	// 0 for a round's first process.
	StreamStart int64 `json:"stream_start,omitempty"`
	// StreamSegments is one entry per process spawned in StreamRound, in
	// spawn order; the drain renders each stream line with the Kind of the
	// last segment whose Start <= the line's offset. Empty for a round
	// started before segments existed; the drain then uses Kind. It belongs
	// to the round's file like StreamRound and StreamOffset: a mid-round
	// switch keeps it, and a later round starts a new list.
	StreamSegments []StreamSegment `json:"stream_segments,omitempty"`

	// StreamSessionID is the session id the round's stream announced, set
	// once per round by drainStream from the harness's own event (#147).
	// Headless only; startRound clears it, because a new process begins a new
	// session. A pre-#303 pane binding keeps its SessionID field.
	StreamSessionID string `json:"stream_session_id,omitempty"`

	// Remote builder endpoint fields
	Server       string `json:"server,omitempty"`
	LastShipped  string `json:"last_shipped,omitempty"`
	LastKnown    string `json:"last_known,omitempty"`
	RemoteStatus string `json:"remote_status,omitempty"`
	// RemoteQueue is what the server's last GET said while the round was
	// queued (#285); nil in every other round state.
	RemoteQueue *QueueFacts `json:"remote_queue,omitempty"`
	RemoteLive  *LiveFacts  `json:"remote_live,omitempty"`

	// TranscriptLocator is the harness's own transcript file path for this
	// endpoint's session, resolved at bind time when possible. Set on
	// Planner only, for the coming history database
	// (docs/specs/2026-09-20-persistence-design.md §5.4); "" when it could
	// not be resolved.
	TranscriptLocator string `json:"transcript_locator,omitempty"`
}

// QueueFacts is what the server's last GET said about a queued round's
// place in its builder queue (#285), copied onto Endpoint.RemoteQueue.
type QueueFacts struct {
	Position int       `json:"position"` // 1-based
	Ahead    int       `json:"ahead"`
	Running  int       `json:"running"`
	Cap      int       `json:"cap"`
	Since    time.Time `json:"since"`
}

// DiffFacts has the same three ints as remote.DiffStat.
type DiffFacts struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// LiveFacts is copied from BindingView.Live by observeRemote on each running poll;
// nil in every other round state.
type LiveFacts struct {
	At             time.Time    `json:"at"`
	PID            int          `json:"pid,omitempty"`
	StartedAt      time.Time    `json:"started_at,omitzero"`
	ExitCode       string       `json:"exit_code,omitempty"`
	Tail           []string     `json:"tail,omitempty"`
	Usage          *usage.Usage `json:"usage,omitempty"`
	PriorTokens    usage.Tokens `json:"prior_tokens,omitzero"`
	Diff           *DiffFacts   `json:"diff,omitempty"`
	LastProgressAt time.Time    `json:"last_progress_at,omitzero"`
	ExploringSince time.Time    `json:"exploring_since,omitzero"`
	GatingSince    time.Time    `json:"gating_since,omitzero"`
}

// Headless reports whether this endpoint is a process relevo runs rather than
// a pane it watches. "" is pane.
func (e Endpoint) Headless() bool { return e.Mode == ModeHeadless }

// Remote reports whether this endpoint is hosted on a remote relevo server.
func (e Endpoint) Remote() bool { return e.Mode == ModeRemote }

// Progress is the current round's sampled progress clock (#135): the tree
// fingerprint and the builder's output, each with the time it last changed.
// It is the raw material for the stalled and exploring labels; relevo never
// acts on it.
type Progress struct {
	// SampledAt is when the last sample was taken. The daemon samples at most
	// once per policy.json's progress_interval_ms; every other tick returns
	// the binding unchanged.
	SampledAt time.Time `json:"sampled_at"`
	// Tree is the last tree fingerprint: HEAD and the porcelain status,
	// hashed. "" when no tree signal was ever readable.
	Tree string `json:"tree,omitempty"`
	// TreeAt is when Tree last changed, and the round's start when it never
	// has.
	TreeAt time.Time `json:"tree_at"`
	// Output is a pre-#303 pane builder's last screen fingerprint; nothing
	// writes it now and only an old bind.json carries one. A headless
	// builder's output is the stream file's mtime, carried in OutputAt alone.
	Output string `json:"output,omitempty"`
	// OutputAt is the stream's last activity for a headless builder; it was
	// the pane's last screen change before #303.
	OutputAt time.Time `json:"output_at"`
}

// Binding ties one planner to one builder over one working tree.
type Binding struct {
	// Format is the on-disk format this record was written at (#372): 0 (a
	// missing key) is format 1, today's shape, and from 2 on the number is
	// written. save refuses to overwrite a Format it does not know, so a
	// newer relevo's fields survive an older binary's rewrite.
	Format int `json:"format,omitempty"`

	Name    string   `json:"name"`
	CWD     string   `json:"cwd"`
	Planner Endpoint `json:"planner"`
	// PlannerID names the relevo planner record this binding belongs to
	// (#303 §3.2, §5.3): the id in $XDG_STATE_HOME/relevo/planners/<id>.json
	// and in the db's planner table. Empty on every binding written before
	// #303 step 1. A remote binding carries the client planner's id too: the
	// planner never goes over the wire, so the server-side binding is
	// planner-less, but the client's record of it belongs to the client's
	// planner exactly like a local binding's. bind/add/fork/ask set it from
	// planner.Resolve, and the daemon back-fills it by planner session.
	PlannerID string   `json:"planner_id,omitempty"`
	Builder   Endpoint `json:"builder"`
	// BuilderCandidate is the harness/provider/model token the builder was
	// started from (#80); empty for an adopted builder and for any binding
	// written before the field existed.
	BuilderCandidate string `json:"builder_candidate,omitempty"`
	// Role is the writer role this binding runs (#382). "" means builder, and a
	// builder binding stores "", so its JSON is byte-identical to one written
	// before the field existed.
	Role string `json:"role,omitempty"`
	// Tier is the effective permission tier the binding's builder launches at
	// (#141), resolved once at bind/add/fork. "" on bindings written before the
	// field existed and means harness.
	Tier string `json:"tier,omitempty"`
	// RoundTier overrides Tier for the CURRENT round of a headless binding,
	// written by Send --tier and cleared by queueReport with RoundSwitches.
	RoundTier string `json:"round_tier,omitempty"`
	// RoundCPU is the core the CURRENT round is pinned to (#314): the lowest
	// free core from scope.allowed_cpus. nil means none -- no pool, an
	// exhausted pool, a census error, or no round started yet. It is a pointer
	// because core 0 is valid, and an old binding without the key decodes as
	// nil. startRound sets it; queueReport clears it with RoundTier.
	RoundCPU *int `json:"round_cpu,omitempty"`
	// Gate is the acceptance command relevo runs in the worktree when the
	// round's completion marker appears (#132); "" means no gate. Run through
	// `sh -c`, so it may be any shell line. Set at bind/add/fork; never changed
	// by relevo.
	Gate string `json:"gate,omitempty"`
	// GateTimeoutMS bounds one gate run; 0 means policy.GateTimeout().
	GateTimeoutMS int `json:"gate_timeout_ms,omitempty"`
	// GateRun is the gate process for the CURRENT round while it runs; nil
	// otherwise. Transient: written when the gate starts, cleared when the
	// round closes.
	GateRun *GateRun `json:"gate_run,omitempty"`

	// RoundVerify is true when the CURRENT round was sent with --verify (or
	// policy.json verify.default): its close starts a read-only reviewer in a
	// throwaway worktree (#144). Set by Send, cleared by queueReport at round
	// close, after the verify consult has been started.
	RoundVerify bool `json:"round_verify,omitempty"`
	// LastVerdict is the newest reviewer verdict (#144). status shows it
	// while Round-1 == LastVerdict.Round: the round it judged was the one just
	// closed. Nil on a binding that never ran a verify consult.
	LastVerdict *Verdict `json:"last_verdict,omitempty"`

	// Regate is the maximum number of automatic repair rounds relevo opens
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
	// QueuedAt is non-zero while the current round is accepted on a server
	// and waiting for a builder slot (#285). Zeroed by relevo.Admit. Never set
	// off the server.
	QueuedAt time.Time `json:"queued_at,omitempty"`
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
	// missing pane explains it, and HaltAt is when relevo decided so. Halt is
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
	// relevo never acts on it -- killing stays the human's decision. Since
	// #135 it is set by progressStep for every local builder mode, from the
	// later of the tree's and the output's last change.
	StalledSince time.Time `json:"stalled_since,omitempty"`

	// StopRequestedAt is when `relevo stop` asked this round's builder to
	// wrap up, and StopGraceMS is what it was called with (#138). Both
	// are zero on a binding that was never stopped; a stop is cleared by Send
	// (a new round) and by the round close (queueReport), so a stale request
	// never outlives the round it was made for.
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	StopGraceMS     int       `json:"stop_grace_ms,omitempty"`
	// Progress is the current round's sampled progress clock (#135): the
	// tree fingerprint and the builder's output, each with the time it last
	// changed. Transient: nil before the round's first sample, and cleared by
	// Send, resume/rebind and round close.
	Progress *Progress `json:"progress,omitempty"`

	// ExploringSince is the tree's last change when the daemon judged the
	// builder exploring (#135): output or screen is moving while the tree has
	// not for policy.json's explore_after_ms. Zero means not exploring. It is
	// a label only -- no hook event -- because some plans are read-heavy.
	// Cleared by Send, resume/rebind and round close.
	ExploringSince time.Time `json:"exploring_since,omitempty"`

	// StaleSince is when a NEEDS YOU binding last changed state (the halt
	// time, or the newest log entry, whichever is available) once it has been
	// unacted for policy.json's stale_after_ms (#135). Zero means not stale.
	// Cleared by Send, resume/rebind and round close.
	StaleSince time.Time `json:"stale_since,omitempty"`

	// StaleNotifiedAt is when relevo last raised the one stale notification for
	// the current episode (#135), so a stale binding notifies once and not once
	// per tick. Reset with StaleSince.
	StaleNotifiedAt time.Time `json:"stale_notified_at,omitempty"`

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
	// legacy: pre-#303 bind.json. BuilderScreen, BuilderScreenAt,
	// PlannerScreen, PlannerScreenAt and HeldGrace recorded terminal screen
	// fingerprints and the grace clock for the pane delivery path. Nothing
	// writes them now; they stay in the struct only so an old bind.json still
	// loads, and are removed after one minor release.
	BuilderScreen   string        `json:"builder_screen,omitempty"`
	BuilderScreenAt time.Time     `json:"builder_screen_at,omitempty"`
	PlannerScreen   string        `json:"planner_screen,omitempty"`
	PlannerScreenAt time.Time     `json:"planner_screen_at,omitempty"`
	HeldGrace       time.Duration `json:"held_grace,omitempty"`

	// preamble_pending (pre-#85) is ignored on load: the role is selected at
	// launch now.

	// Worktree is the git worktree RELEVO created for this binding, and is therefore
	// the only directory relevo may ever remove. Empty for every binding relevo did
	// not create a tree for -- including a fork bound to a directory the human
	// supplied. Never infer ownership from the path.
	Worktree string `json:"worktree,omitempty"`

	// ForkedFrom is the binding this one was forked from, for provenance only.
	// Nothing reads it to make a decision: a fork is an ordinary binding the
	// moment it exists, and the source may be unbound while the fork runs on.
	ForkedFrom string `json:"forked_from,omitempty"`

	// ForkedAtRound is the source round this binding's history was copied through.
	ForkedAtRound int `json:"forked_at_round,omitempty"`

	// Branch is the branch relevo created for this binding's worktree
	// (relevo/<name>), and Base the commit it was cut at. Written once by add
	// and fork; empty for a --cwd binding, an adopted bind, and every
	// bind.json written before the fields existed. Display and provenance
	// today; the branch-integration verbs key on them (#130).
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`

	// BaseRef is the branch name add/fork cut the worktree's branch from --
	// the branch the source repository had checked out at cut time (e.g.
	// "main") -- and is what `relevo land` rebases onto when --onto is not
	// given (#136). "" for every binding written before the field existed,
	// for a --cwd binding, for a --branch adoption, and for a detached HEAD
	// in the source repo; land then requires an explicit --onto.
	BaseRef string `json:"base_ref,omitempty"`

	// LandedAt is when `relevo land` last pushed this binding's branch, and
	// LandedPR is the PR URL when it created one with --pr (#136). Status
	// reads "landed" until the next Send clears both: a new round moves the
	// branch again, so the old land says nothing about it.
	LandedAt time.Time `json:"landed_at,omitempty"`
	LandedPR string    `json:"landed_pr,omitempty"`

	// ExistingBranch is true when add --branch adopted a branch relevo did not
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
	// internal/relevo/fork.go, internal/relevo/status.go and
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

	// Edges is read from records written before relevo edge was removed; nothing writes it.
	Edges []Edge `json:"edges,omitempty"`

	// Owner is the enrolled client id that created this binding on a relevo
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
	// Attempt is 0 for this round's first gate run and 1 for the single
	// re-run allowed after a daemon restart took the gate with it (#370,
	// spec §4.4). It is never greater than 1, and a bind.json written before
	// the field existed decodes as 0, the first run.
	Attempt int `json:"attempt,omitempty"`
}

// Verdict is one reviewer's verdict on a closed round (#144): what a verify
// consult's findings parsed to, recorded on the binding so `relevo status` can
// show the newest one until the round after next.
type Verdict struct {
	Round int `json:"round"`
	// Verdict is the parsed verdict: "accepted", "rejected", or
	// "unstructured" when the findings carried no readable block.
	Verdict string `json:"verdict"`
	// Reasons are the reviewer's reasons, from the block when it parsed.
	Reasons []string `json:"reasons,omitempty"`
	// Findings is the round file key the verdict was parsed from;
	// "" when the consult wrote none.
	Findings string `json:"findings"`
}

type ServeFacts struct {
	RepoID       string    `json:"repo_id"`                 // remote.RepoID of the client's repo
	BareRepo     string    `json:"bare_repo"`               // absolute path of the bare repo
	ClosedRound  int       `json:"closed_round,omitempty"`  // last round closed by the daemon; 0 none
	ResultCommit string    `json:"result_commit,omitempty"` // refs/heads/relevo/<name> at that close
	DirtyCommit  string    `json:"dirty_commit,omitempty"`  // refs/relevo/<name>/round-<ClosedRound>, "" if clean
	AckedRound   int       `json:"acked_round,omitempty"`   // last round the owner acked; 0 none
	LastSeen     time.Time `json:"last_seen,omitempty"`     // last signed request from the owner about this binding

	// AuthorName and AuthorEmail are the client's git identity, carried on
	// the create request so every builder relevo starts for this binding
	// commits as the client (#335). Both empty means no identity was
	// carried: an old client, or a binding created before #335.
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
}

// DefaultConsultCap bounds how many consults may be RUNNING on one binding at
// once. It exists for the reason RoundCap does: an idle harness process holds
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
// "Read-only" describes how the role is configured, not something relevo
// enforces: a consult's writes are not observable to relevo, so the role is a
// contract with the harness, not a sandbox.
type Consult struct {
	// ID is 8 lowercase hex characters, unique within one binding. It appears
	// in the log, in both filenames, and in `relevo reap`, so it is short enough
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

	AskPath string `json:"ask_path"`
	// FindingsPath is the round file key read through Store.ReadFile.
	FindingsPath string `json:"findings_path"`

	State ConsultState `json:"state"`

	SpawnedAt time.Time `json:"spawned_at"`

	// NudgedAt is when relevo sent this consult its single nudge; zero means it
	// has not been nudged. A consult runs for a minute or two, so it does not
	// inherit the builder's screen-fingerprint quiescence or scrape fallback.
	// (no omitempty: encoding/json never omits a struct, so the option read as
	// a promise the zero time would vanish from bind.json. It never did.)
	NudgedAt time.Time `json:"nudged_at"`

	// Note is why a silent consult gave up. Empty for running and done.
	Note string `json:"note,omitempty"`
}

// Edge is read from records written before relevo edge was removed; nothing writes it.
type Edge struct {
	ID      string    `json:"id"`
	Round   int       `json:"round"`
	When    string    `json:"when"`
	Then    string    `json:"then"`
	Target  string    `json:"target"`
	Prompt  string    `json:"prompt"`
	Mode    string    `json:"mode"`
	AddedAt time.Time `json:"added_at"`
	Fired   bool      `json:"fired,omitempty"`
	FiredAt time.Time `json:"fired_at,omitempty"`
	Result  string    `json:"result,omitempty"`
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
