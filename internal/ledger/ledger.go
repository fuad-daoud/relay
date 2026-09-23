// Package ledger records when a candidate could not be used
// and why: spawn failures relay observed, rate limits the planner reported.
// It records and answers; it never decides (#61 step 1).
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Kind classifies why a candidate could not be used.
// Each kind fixes what its Subject means (#61 step 1).
type Kind string

const (
	// SpawnFailed records that relay failed to start a candidate process or
	// that the process died during initialization. Subject is a candidate token.
	SpawnFailed Kind = "spawn_failed"

	// RateLimited records that a provider is currently rejecting requests
	// due to quota or concurrency limits. Subject is a provider name.
	RateLimited Kind = "rate_limited"

	// ExitedNoReport records that a headless builder exited without a
	// report during the current round. It is never written to the ledger
	// file: it is synthesised in memory, per round, by switchBuilder from
	// Binding.RoundExcluded (#191), so Load never needs to validate it.
	ExitedNoReport Kind = "exited_no_report"

	// RolesMissing records that a candidate's harness kind is missing role
	// files (definitions) relay needs to run it as a builder. Like
	// ExitedNoReport, it is never written to the ledger file: it is
	// synthesised in memory by relay.Gates from an injectable
	// Runtime.Roles checker, so Load never needs to validate it (#238).
	RolesMissing Kind = "roles_missing"
)

// ErrBadEntry reports a ledger entry that fails validation.
var ErrBadEntry = errors.New("bad ledger entry")

// Entry records a single availability event for a candidate or provider.
type Entry struct {
	Kind    Kind      `json:"kind"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Until   time.Time `json:"until,omitempty"`
	Note    string    `json:"note,omitempty"`
	Source  string    `json:"source"`
	Binding string    `json:"binding,omitempty"`
}

// Expired reports whether the entry has expired at the given time.
// An entry with a zero Until never expires automatically.
func (e Entry) Expired(now time.Time) bool {
	return !e.Until.IsZero() && !now.Before(e.Until)
}

// Ledger holds an ordered collection of availability entries.
//
// Entries are the ones this binary understands. Other holds entries with an
// unknown kind or source, preserved verbatim so an older relay never erases a
// newer one's records (#372 §4.2): they are invisible to every reader, never
// pruned or cleared, and written back byte-for-byte.
type Ledger struct {
	Entries []Entry           `json:"-"`
	Other   []json.RawMessage `json:"-"`
}

// knownKind reports whether k is a kind this binary reads from the ledger
// file. ExitedNoReport and RolesMissing are synthesised in memory, never
// written, so they are not known here either.
func knownKind(k Kind) bool {
	return k == SpawnFailed || k == RateLimited
}

// knownSource reports whether s is a source this binary reads from the ledger
// file.
func knownSource(s string) bool {
	return s == "relay" || s == "planner"
}

// Load reads and validates the availability ledger from disk. A missing file
// returns an empty Ledger without error, as a fresh install records no events yet.
// Load validates entry schema but does not prune expired entries; callers prune
// against their own notion of time (#61 step 1).
//
// An entry whose kind or source is unknown to this binary is preserved raw in
// Other rather than rejected, so a ledger written by a newer relay survives a
// rollback (#372 §4.2). Malformed JSON is still an error.
func Load(path string) (Ledger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Ledger{}, nil
	}
	if err != nil {
		return Ledger{}, fmt.Errorf("read ledger %s: %w", path, err)
	}

	var doc struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Ledger{}, fmt.Errorf("decode ledger %s: %w", path, err)
	}

	var l Ledger
	for i, raw := range doc.Entries {
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return Ledger{}, fmt.Errorf("decode ledger %s: entry %d: %w", path, i, err)
		}

		if !knownKind(e.Kind) || !knownSource(e.Source) {
			l.Other = append(l.Other, raw)
			continue
		}

		var why string
		switch {
		case e.Subject == "":
			why = "subject is empty"
		case e.At.IsZero():
			why = "at is zero"
		case !e.Until.IsZero() && e.Until.Before(e.At):
			why = "until precedes at"
		}
		if why != "" {
			return Ledger{}, fmt.Errorf("ledger %s: entry %d: %s: %w", path, i, why, ErrBadEntry)
		}
		l.Entries = append(l.Entries, e)
	}

	return l, nil
}

// Save writes the ledger to disk atomically via a temporary file and rename,
// ensuring concurrent readers never observe torn writes. It creates any missing
// parent directories so callers need not ensure state root existence (#61 step 1).
//
// The known entries are marshalled as before; Other's raw bytes follow
// verbatim (#372 §4.2).
func Save(path string, l Ledger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}

	var entries []json.RawMessage
	for _, e := range l.Entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal ledger entry: %w", err)
		}
		entries = append(entries, raw)
	}
	entries = append(entries, l.Other...)

	doc := struct {
		Entries []json.RawMessage `json:"entries"`
	}{Entries: entries}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ledger: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write ledger temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename ledger into place: %w", err)
	}

	return nil
}

// Prune returns a new Ledger containing every non-expired entry, in order.
// It does not mutate the receiver's slice. Other is carried through untouched:
// it is never pruned (#372 §4.2).
func (l Ledger) Prune(now time.Time) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if !e.Expired(now) {
			kept = append(kept, e)
		}
	}
	return Ledger{Entries: append([]Entry(nil), kept...), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Append returns a new Ledger with e added to the end.
// It performs no deduplication: two spawn failures are two events; step 7 counts them.
// It does not mutate the receiver's slice. Other is carried through untouched.
func (l Ledger) Append(e Entry) Ledger {
	cp := append([]Entry(nil), l.Entries...)
	return Ledger{Entries: append(cp, e), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Clear returns a new Ledger without every entry whose Kind == kind and Subject == subject.
// It does not mutate the receiver's slice. Other is never cleared (#372 §4.2).
func (l Ledger) Clear(kind Kind, subject string) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if e.Kind == kind && e.Subject == subject {
			continue
		}
		kept = append(kept, e)
	}
	return Ledger{Entries: append([]Entry(nil), kept...), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Gate is one candidate's exposure to one live ledger entry: which token it
// gates, why, and since when. A candidate can carry several gates; the
// renderer shows them all.
type Gate struct {
	Token   string // the gated candidate, canonical ref
	Kind    Kind
	Since   time.Time // Entry.At
	Until   time.Time // zero = until cleared
	Note    string
	Source  string
	Binding string // Entry.Binding; "" for planner entries
	// Role scopes this gate to one role (#374 §5): non-empty means it applies
	// only to that role, "" means every role -- which covers every gate read
	// from the ledger file and every gate Gated produces. Only
	// rolesMissingGates sets it.
	Role string `json:"Role,omitempty"`
}

// Gated is the one view every renderer uses: for each live entry, which
// configured candidates it gates. A spawn failure gates its own token; a
// rate limit gates every candidate of its provider, because the quota is
// the provider's, not the model's (spec §1).
func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time) []Gate {
	var gates []Gate

	for _, e := range l.Prune(now).Entries {
		switch e.Kind {
		case SpawnFailed:
			if slices.Contains(refs, e.Subject) {
				gates = append(gates, Gate{
					Token:   e.Subject,
					Kind:    e.Kind,
					Since:   e.At,
					Until:   e.Until,
					Note:    e.Note,
					Source:  e.Source,
					Binding: e.Binding,
				})
			}
		case RateLimited:
			for _, ref := range refs {
				if providerOf(ref) == e.Subject {
					gates = append(gates, Gate{
						Token:   ref,
						Kind:    e.Kind,
						Since:   e.At,
						Until:   e.Until,
						Note:    e.Note,
						Source:  e.Source,
						Binding: e.Binding,
					})
				}
			}
		}
	}

	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Token != gates[j].Token {
			return gates[i].Token < gates[j].Token
		}
		return gates[i].Since.Before(gates[j].Since)
	})

	return gates
}
