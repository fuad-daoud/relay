// Package planner owns relay's planner identity (#303 step 1a): the records
// that say which harness process and session a planner is, the rules that
// mint and validate their ids and names, and the lookups every other verb
// resolves a planner through. Nothing here launches a process, and planner.go
// does no I/O at all: a Record is a value, and the rules over it are pure.
//
// The design is #303 §3.1, §3.4, §3.5
// and §4.1–§4.4. Records live under $XDG_STATE_HOME/relay/planners, one JSON
// file per record (registry.go), and are ingested into relay's sqlite
// `planner` table by id.
package planner

import (
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"
)

// The sentinel errors every caller switches on (§6.1). ErrNoPlanner carries
// the fix in its text: it is the message a planner sees when the relay plugin
// is missing, and "no planner" alone would not say what to do about it.
var (
	// ErrNotFound reports that no record matched a lookup.
	ErrNotFound = errors.New("planner not found")
	// ErrNameTaken reports that another record already holds that name.
	ErrNameTaken = errors.New("planner name taken")
	// ErrSessionTaken reports that another record already holds that
	// (harness_kind, session_id) as its current session.
	ErrSessionTaken = errors.New("planner session taken")
	// ErrHostTaken reports that another record already holds that live
	// (host_pid, host_started_at).
	ErrHostTaken = errors.New("planner host taken")
	// ErrInUse reports that a binding that is not DONE still names the record.
	ErrInUse = errors.New("planner in use")
	// ErrInvalid reports a record or value that failed validation.
	ErrInvalid = errors.New("invalid planner")
	// ErrNoPlanner is what resolving without a match returns. Its text is the
	// fix (§4.3).
	ErrNoPlanner = errors.New("no relay planner for this session: is the relay plugin enabled (`relay doctor`)? Or run `relay planner init`")
)

// ErrUnknownPlanner reports a --planner or $RELAY_PLANNER value that matches
// no record at all, which is a different failure from resolving nothing.
type ErrUnknownPlanner struct {
	// Ref is the flag or environment value that matched nothing.
	Ref string
}

func (e ErrUnknownPlanner) Error() string {
	return fmt.Sprintf("unknown planner %q: no planner record has that id or name", e.Ref)
}

// SessionRef is one earlier session of a planner, kept so history still joins
// after the planner moves on (§3.1). To is when the move happened; From is
// when the session it names began.
type SessionRef struct {
	SessionID string    `json:"session_id"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
}

// Record is one relay planner: the identity every binding, claim and db row
// keys on (§3.1). Its JSON names are the file's field names and the db's
// source.
type Record struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	HarnessKind       string       `json:"harness_kind"`
	SessionID         string       `json:"session_id"`
	Sessions          []SessionRef `json:"sessions"`
	HostPID           int          `json:"host_pid"`
	HostStartedAt     int64        `json:"host_started_at"`
	CWD               string       `json:"cwd"`
	TranscriptLocator string       `json:"transcript_locator,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	SeenAt            time.Time    `json:"seen_at"`
}

// MaxNameLen is the longest name ValidName accepts: the §3.1 rule
// [a-z][a-z0-9-]{0,31}.
const MaxNameLen = 32

// MaxSessions caps a record's session history: appending a 21st drops the
// oldest (§3.1).
const MaxSessions = 20

var (
	// idRe is §3.1's id rule for ids NewID mints: `pl_` plus 12 lowercase
	// RFC 4648 base32 characters.
	idRe = regexp.MustCompile(`^pl_[a-z2-7]{12}$`)
	// legacyIDRe is the shape of every planner id that already exists in a
	// real relay.db: a 26-character Crockford base32 ULID minted by
	// internal/db/ulid.go (alphabet 0123456789ABCDEFGHJKMNPQRSTVWXYZ, so no
	// I, L, O or U). §3.5 requires reusing those ids, so ValidID accepts
	// this shape too.
	legacyIDRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	// nameRe is §3.1's name rule.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// namePrefixRe is what a string must look like to become the `<agent>`
	// half of a default name. DefaultName also checks it leaves room for the
	// "-<n>" suffix.
	namePrefixRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// opencodeSessionRe is §3.1's opencode session rule.
	opencodeSessionRe = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)
)

// base32Lower is RFC 4648 base32 with the alphabet lowercased: exactly the
// [a-z2-7] ids are drawn from. Padding is off, so the encoding is a plain run
// of characters.
var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// idChars is how many characters of the base32 encoding an id carries.
const idChars = 12

// NewID mints a planner id: `pl_` plus 12 lowercase base32 characters drawn
// from rand, which production wires to crypto/rand (§3.1). It takes the
// reader as an argument so tests can pin it.
func NewID(rand io.Reader) (string, error) {
	// 12 base32 characters carry 60 bits; 8 bytes is the whole number of
	// bytes that encodes to at least that many characters.
	var buf [8]byte
	if _, err := io.ReadFull(rand, buf[:]); err != nil {
		return "", fmt.Errorf("planner: mint id: %w", err)
	}
	return "pl_" + base32Lower.EncodeToString(buf[:])[:idChars], nil
}

// ValidID reports whether id names a planner record: §3.1's `pl_` plus 12
// lowercase base32 characters, or the 26-character Crockford base32 ULID
// internal/db/ulid.go mints. The second shape is §3.5's upgrade path -- every
// id already in relay.db is a ULID, and a record reusing one is
// `<ULID>.json` on disk. Both shapes are path-safe (no separator, no dot), so
// an id can never become a path.
func ValidID(id string) error {
	if !idRe.MatchString(id) && !legacyIDRe.MatchString(id) {
		return fmt.Errorf("planner id %q must be pl_ followed by 12 characters of [a-z2-7], or a 26-character Crockford base32 ULID: %w", id, ErrInvalid)
	}
	return nil
}

// ValidName reports whether name has §3.1's shape:
// [a-z][a-z0-9-]{0,31}.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("planner name %q must match [a-z][a-z0-9-]{0,31}: %w", name, ErrInvalid)
	}
	return nil
}

// validNamePrefix reports whether agent can be the `<agent>` half of a
// default name: a valid name fragment that still leaves room for the "-<n>"
// suffix inside the length cap.
func validNamePrefix(agent string) bool {
	if !namePrefixRe.MatchString(agent) {
		return false
	}
	return len(agent)+2 <= MaxNameLen
}

// DefaultName is the first free `<base>-<n>`, counting n up from 1, where base
// is agent when it can be a name prefix and kind otherwise (§3.1). taken
// reports whether a candidate is already in use; nil means nothing is.
func DefaultName(agent, kind string, taken func(string) bool) string {
	base := agent
	if !validNamePrefix(base) {
		base = kind
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%d", base, n)
		if taken == nil || !taken(name) {
			return name
		}
	}
}

// Validate checks the §3.1 required constraints, the opencode session rule and
// the one cross-field rule: host_started_at is 0 whenever host_pid is 0.
// Everything it rejects wraps ErrInvalid.
func (r Record) Validate() error {
	if err := ValidID(r.ID); err != nil {
		return err
	}
	if err := ValidName(r.Name); err != nil {
		return err
	}
	if r.HarnessKind == "" {
		return fmt.Errorf("planner: harness_kind is required: %w", ErrInvalid)
	}
	if r.SessionID == "" {
		return fmt.Errorf("planner: session_id is required: %w", ErrInvalid)
	}
	if r.HarnessKind == "opencode" && !opencodeSessionRe.MatchString(r.SessionID) {
		return fmt.Errorf("planner: opencode session_id %q must match ^ses_[A-Za-z0-9]+$: %w", r.SessionID, ErrInvalid)
	}
	if r.CWD == "" || !filepath.IsAbs(r.CWD) {
		return fmt.Errorf("planner: cwd %q must be an absolute path: %w", r.CWD, ErrInvalid)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("planner: created_at is required: %w", ErrInvalid)
	}
	if r.SeenAt.IsZero() {
		return fmt.Errorf("planner: seen_at is required: %w", ErrInvalid)
	}
	if r.HostPID < 0 {
		return fmt.Errorf("planner: host_pid %d must be >= 0: %w", r.HostPID, ErrInvalid)
	}
	if r.HostPID == 0 && r.HostStartedAt != 0 {
		return fmt.Errorf("planner: host_started_at must be 0 when host_pid is 0: %w", ErrInvalid)
	}
	return nil
}

// appendSession appends ref to a record's session history, dropping the oldest
// entry when the cap would be exceeded (§3.1: max 20, oldest dropped). It
// always returns a fresh slice, so appending never writes through a caller's
// array.
func appendSession(sessions []SessionRef, ref SessionRef) []SessionRef {
	out := make([]SessionRef, 0, len(sessions)+1)
	out = append(out, sessions...)
	out = append(out, ref)
	if len(out) > MaxSessions {
		out = out[len(out)-MaxSessions:]
	}
	return out
}

// sessionStartedAt is when this record's current session began: the end of the
// last recorded move, or the record's own creation when it has never moved.
// §3.1's SessionRef carries From and To, but a record stores no separate start
// time for its current session, so this chain is the only one available.
func (r Record) sessionStartedAt() time.Time {
	if n := len(r.Sessions); n > 0 {
		return r.Sessions[n-1].To
	}
	return r.CreatedAt
}
