package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/fuad-daoud/relay/internal/usage"
)

// Version is the remote protocol version.
const Version = 1

// ContentTypeGitBundle is the MIME content type for git bundles.
const ContentTypeGitBundle = "application/x-git-bundle"

// RoundState represents the execution state of a round on the server.
type RoundState string

const (
	RoundIdle     RoundState = "idle"
	RoundRunning  RoundState = "running"
	RoundClosed   RoundState = "closed"
	RoundNeedsYou RoundState = "needs_you"
)

// WhoAmI represents the response to an authentication identity check.
type WhoAmI struct {
	ID            ClientID `json:"id"`
	Label         string   `json:"label"`
	ServerVersion int      `json:"server_version"`
	Transports    []string `json:"transports"` // ["git-bundle"]

	// Features, BuilderTier and MaxTier are additive (#141 remote half): a
	// pre-tier server omits them, so a client can tell the two apart before
	// creating anything.
	Features    []string `json:"features,omitempty"`     // ["tier"] on a server with this change
	BuilderTier string   `json:"builder_tier,omitempty"` // ServedBuilderTier(rt): the tier a headless builder
	// launches at when the client sends none
	MaxTier string `json:"max_tier,omitempty"` // policy.MaxTierOrDefault()
}

// CreateBindingRequest holds the parameters for creating a new binding on the server.
type CreateBindingRequest struct {
	Name           string `json:"name"`        // required; store name rules apply on the server
	RepoID         string `json:"repo_id"`     // required; RepoID()
	BaseCommit     string `json:"base_commit"` // required; 40 hex
	Candidate      string `json:"candidate,omitempty"`
	RoundCap       int    `json:"round_cap,omitempty"`
	RoundTimeoutMS int    `json:"round_timeout_ms,omitempty"`
	Tier           string `json:"tier,omitempty"` // "" = server's choice; else harness|read|edit|yolo
}

// BindingView is the server's wire representation of a binding's state.
type BindingView struct {
	Name          string     `json:"name"`
	State         string     `json:"state"` // the store's State string
	Round         int        `json:"round"`
	RoundState    RoundState `json:"round_state"`
	ClosedRound   int        `json:"closed_round"`
	Halt          string     `json:"halt,omitempty"`
	ResultCommit  string     `json:"result_commit,omitempty"`
	DirtyCommit   string     `json:"dirty_commit,omitempty"`
	ReportOutcome string     `json:"report_outcome,omitempty"` // relay.ReportTail.Status or "unstructured"
	// DiffNote, DiffCommits and DiffTree are the closed round's diff facts,
	// from the newest KindDiff entry for Serve.ClosedRound -- the same facts
	// DiffSummary wrote to the server's own log at close. Empty/zero on any
	// binding that is not closed, or whose close wrote no diff entry.
	DiffNote       string    `json:"diff_note,omitempty"`
	DiffCommits    int       `json:"diff_commits,omitempty"`
	DiffTree       string    `json:"diff_tree,omitempty"`
	AckedRound     int       `json:"acked_round"`
	Candidate      string    `json:"candidate,omitempty"`
	RoundStartedAt time.Time `json:"round_started_at,omitempty"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	Tier           string    `json:"tier,omitempty"` // effectiveTier(b) on the server; "" from a pre-tier server

	// Usage is the closed round's usage as the server recorded it on its
	// report entry (usage.Usage is already JSON-tagged; it is the same
	// struct store.LogEntry.Usage holds). nil from a pre-usage server, or
	// when the closed round has no report entry.
	Usage *usage.Usage `json:"usage,omitempty"`
}

// UnavailableRequest reports builder unavailability with a diagnostic reason.
type UnavailableRequest struct {
	Token  string `json:"token"`
	Reason string `json:"reason"`
}

// AvailableRequest lifts recorded unavailability: subject is a provider name
// or a candidate token, as relay.Available takes.
type AvailableRequest struct {
	Subject string `json:"subject"`
}

// AvailableResponse reports what Available lifted: the provider it resolved
// subject to, and how many rate-limit entries it removed.
type AvailableResponse struct {
	Provider string `json:"provider"`
	Removed  int    `json:"removed"`
}

type CandidateView struct {
	Token string `json:"token"` // canonical harness/provider/model token
	Kind  string `json:"kind"`  // harness kind: agy | claude | opencode
	Gated bool   `json:"gated"` // a live limit gate on the ledger
	Pick  bool   `json:"pick"`  // what the policy order would pick right now for the builder role
}

type CandidatesResponse struct {
	Candidates []CandidateView `json:"candidates"`
}

// Code represents a structured error code returned by the remote protocol.
type Code string

const (
	CodeNotEnrolled    Code = "not_enrolled"
	CodeRevoked        Code = "revoked"
	CodeBadSignature   Code = "bad_signature"
	CodeStale          Code = "stale"
	CodeNotFound       Code = "not_found"
	CodeRoundOpen      Code = "round_open"
	CodeRoundStarted   Code = "round_started"
	CodeNotFastForward Code = "not_fast_forward"
	CodeNoRunner       Code = "no_runner"
	CodeSpawnFailed    Code = "spawn_failed"
	CodeTooLarge       Code = "too_large"
	CodeVersion        Code = "version"
	CodeInvalid        Code = "invalid"
	CodeTierAboveMax   Code = "tier_above_max"
)

// FeatureTier is the WhoAmI.Features token a server with the permission-tier
// wire fields advertises (#141 remote half).
const FeatureTier = "tier"

// ErrorBody represents a JSON error response returned by the server.
type ErrorBody struct {
	Code    Code   `json:"error"`
	Message string `json:"message"`
}

// Error implements error, returning Message.
func (e ErrorBody) Error() string {
	return e.Message
}

// CodeOf maps auth sentinels to their respective error codes, returning "" for unknown errors.
func CodeOf(err error) Code {
	switch {
	case errors.Is(err, ErrUnknownClient):
		return CodeNotEnrolled
	case errors.Is(err, ErrRevoked):
		return CodeRevoked
	case errors.Is(err, ErrBadSignature):
		return CodeBadSignature
	case errors.Is(err, ErrStale):
		return CodeStale
	default:
		return ""
	}
}

// ErrNoRoot indicates that an empty root commit SHA was passed to RepoID.
var ErrNoRoot = errors.New("empty root commit")

// RepoID returns the lower-case hex representation of sha256(rootCommit), where
// rootCommit is the ASCII 40-hex string. Returns ErrNoRoot if rootCommit is empty.
func RepoID(rootCommit string) (string, error) {
	if rootCommit == "" {
		return "", ErrNoRoot
	}
	sum := sha256.Sum256([]byte(rootCommit))
	return hex.EncodeToString(sum[:]), nil
}
