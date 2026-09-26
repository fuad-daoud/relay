package store

import (
	"time"
)

// RepoRef identifies the git repository a binding works in; nil means it could
// not be determined and never fails the caller.
type RepoRef struct {
	OriginURL string `json:"origin_url,omitempty"`
	CommonDir string `json:"common_dir,omitempty"`
}

// ForkRef records the source binding and round a fork was cut from.
type ForkRef struct {
	Name  string `json:"name"`
	Round int    `json:"round"`
}

// Rusage is the cgroup's usage_usec and memory.peak for a round's systemd
// scope, read after the builder exits.
type Rusage struct {
	CPUMS        int64 `json:"cpu_ms,omitempty"`
	PeakMemBytes int64 `json:"peak_mem_bytes,omitempty"`
}

// Progress is the current round's sampled progress clock. relevo never acts on
// it.
type Progress struct {
	SampledAt time.Time `json:"sampled_at"`
	Tree      string    `json:"tree,omitempty"`
	TreeAt    time.Time `json:"tree_at"`
	// Output is a pre-pane-removal builder's last screen fingerprint; only an
	// old bind.json carries one.
	Output   string    `json:"output,omitempty"`
	OutputAt time.Time `json:"output_at"`
}

// GateRun is the gate process for the CURRENT round while it runs.
type GateRun struct {
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	Round     int    `json:"round"`
	Command   string `json:"command"`
	// Attempt is 0 for this round's first gate run and 1 for the single re-run
	// allowed after a daemon restart took the gate with it; never greater.
	Attempt int `json:"attempt,omitempty"`
}

// Verdict is one reviewer's verdict on a closed round, shown by status until
// the round after next.
type Verdict struct {
	Round   int      `json:"round"`
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`
	// Findings is the round file key the verdict was parsed from.
	Findings string `json:"findings"`
}

type ServeFacts struct {
	RepoID       string    `json:"repo_id"`
	BareRepo     string    `json:"bare_repo"`
	ClosedRound  int       `json:"closed_round,omitempty"`
	ResultCommit string    `json:"result_commit,omitempty"`
	DirtyCommit  string    `json:"dirty_commit,omitempty"`
	AckedRound   int       `json:"acked_round,omitempty"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
	// AuthorName and AuthorEmail are the client's git identity, so every
	// builder relevo starts for this binding commits as the client.
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
}

// Binding ties one planner to one builder over one working tree.
type Binding struct {
	// Format is the on-disk format: 0 (a missing key) is format 1, today's
	// shape; save refuses to overwrite a Format it does not know.
	Format int `json:"format,omitempty"`

	Name    string   `json:"name"`
	CWD     string   `json:"cwd"`
	Planner Endpoint `json:"planner"`
	// PlannerID names the relevo planner record this binding belongs to. A
	// remote binding carries the client planner's id too, even though the
	// planner never goes over the wire.
	PlannerID        string   `json:"planner_id,omitempty"`
	Builder          Endpoint `json:"builder"`
	BuilderCandidate string   `json:"builder_candidate,omitempty"`
	// Role "" means builder, and stores as "".
	Role string `json:"role,omitempty"`
	// Tier "" means harness.
	Tier      string `json:"tier,omitempty"`
	RoundTier string `json:"round_tier,omitempty"`
	// RoundCPU is a pointer because core 0 is valid; nil means none.
	RoundCPU *int `json:"round_cpu,omitempty"`
	// Gate is the acceptance command run through `sh -c` when the round's
	// completion marker appears; "" means no gate.
	Gate          string   `json:"gate,omitempty"`
	GateTimeoutMS int      `json:"gate_timeout_ms,omitempty"`
	GateRun       *GateRun `json:"gate_run,omitempty"`

	RoundVerify bool `json:"round_verify,omitempty"`
	// LastVerdict is shown by status while Round-1 == LastVerdict.Round.
	LastVerdict *Verdict `json:"last_verdict,omitempty"`

	// Regate is the maximum number of automatic repair rounds relevo opens
	// after a failing gate; 0 means off. A human send or a gate pass resets
	// RepairCount, not this: the budget is a property of the binding.
	Regate      int `json:"regate,omitempty"`
	RepairCount int `json:"repair_count,omitempty"`
	// LastGateSig is the sha256 hex of the normalised output of the last
	// FAILED gate: the stall bound compares the next failure against it.
	LastGateSig string `json:"last_gate_sig,omitempty"`

	Round          int       `json:"round"`
	State          State     `json:"state"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	RoundStartedAt time.Time `json:"round_started_at"`
	// QueuedAt is non-zero while the current round is accepted on a server and
	// waiting for a builder slot.
	QueuedAt time.Time `json:"queued_at,omitempty"`
	// HaltNotifiedRound is deliberately NOT derived from State, which a later
	// step in the same tick may rewrite.
	HaltNotifiedRound int `json:"halt_notified_round,omitempty"`

	// FinishPending is deliberately NOT derived from State, like
	// HaltNotifiedRound.
	FinishPending bool `json:"finish_pending,omitempty"`

	// Halt is meaningful only while State == needs_you, so a stale value is
	// harmless.
	Halt   string    `json:"halt,omitempty"`
	HaltAt time.Time `json:"halt_at,omitempty"`

	RoundSwitches int `json:"round_switches,omitempty"`

	// RoundExcluded are candidate tokens that exited without a report during
	// the CURRENT round, so a switch never lands the pick back on a builder
	// that just proved it cannot finish the round.
	RoundExcluded []string `json:"round_excluded,omitempty"`

	// BuilderMissingSince is stamped on the first miss and cleared on any hit,
	// so a detection flicker never accumulates toward a switch.
	BuilderMissingSince time.Time `json:"builder_missing_since,omitempty"`

	// StalledSince: relevo never acts on it -- killing stays the human's
	// decision.
	StalledSince time.Time `json:"stalled_since,omitempty"`

	// StopRequestedAt and StopGraceMS are cleared by Send and by the round
	// close, so a stale request never outlives its round.
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	StopGraceMS     int       `json:"stop_grace_ms,omitempty"`
	Progress        *Progress `json:"progress,omitempty"`

	// ExploringSince is a label only -- no hook event -- because some plans
	// are read-heavy.
	ExploringSince time.Time `json:"exploring_since,omitempty"`

	// StaleSince is when a NEEDS YOU binding last changed state once it has
	// been unacted for policy.json's stale_after_ms.
	StaleSince time.Time `json:"stale_since,omitempty"`

	StaleNotifiedAt time.Time `json:"stale_notified_at,omitempty"`

	// RoundBaselineTree empty means no baseline was captured and the round
	// produces no diff.
	RoundBaselineTree string `json:"round_baseline_tree,omitempty"`
	// RoundClosedTree is deliberately not derived from RoundBaselineTree: the
	// two describe different instants.
	RoundClosedTree   string `json:"round_closed_tree,omitempty"`
	RoundBaselineHead string `json:"round_baseline_head,omitempty"`
	// legacy: bind.json from before pane builders were removed.
	BuilderScreen   string        `json:"builder_screen,omitempty"`
	BuilderScreenAt time.Time     `json:"builder_screen_at,omitempty"`
	PlannerScreen   string        `json:"planner_screen,omitempty"`
	PlannerScreenAt time.Time     `json:"planner_screen_at,omitempty"`
	HeldGrace       time.Duration `json:"held_grace,omitempty"`

	// Worktree is the only directory relevo may ever remove.
	Worktree      string `json:"worktree,omitempty"`
	ForkedFrom    string `json:"forked_from,omitempty"`
	ForkedAtRound int    `json:"forked_at_round,omitempty"`

	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	// BaseRef "" means `relevo land` requires an explicit --onto.
	BaseRef string `json:"base_ref,omitempty"`

	LandedAt       time.Time `json:"landed_at,omitempty"`
	LandedPR       string    `json:"landed_pr,omitempty"`
	ExistingBranch bool      `json:"existing_branch,omitempty"`

	// Repo empty disables worktree-escape detection.
	Repo string `json:"repo,omitempty"`

	// RepoRef is distinct from Repo, the add/fork source checkout.
	RepoRef *RepoRef `json:"repo_ref,omitempty"`

	Feature string `json:"feature,omitempty"`

	// Consults omitempty keeps every bind.json written before consults existed
	// byte-identical until its first consult.
	Consults []Consult `json:"consults,omitempty"`

	// ConsultCap zero means DefaultConsultCap.
	ConsultCap int `json:"consult_cap,omitempty"`

	// Edges is read from records written before relevo edge was removed;
	// nothing writes it.
	Edges []Edge `json:"edges,omitempty"`

	// Owner is the enrolled client id that created this binding on a relevo
	// server; empty on every local binding.
	Owner string `json:"owner,omitempty"`

	// Serve is nil on local bindings.
	Serve *ServeFacts `json:"serve,omitempty"`

	RemoteUnreachableSince time.Time `json:"remote_unreachable_since,omitempty"`
	RemoteAbsorbFailures   int       `json:"remote_absorb_failures,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
