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
type Ledger struct {
	Entries []Entry `json:"entries"`
}

// Load reads and validates the availability ledger from disk. A missing file
// returns an empty Ledger without error, as a fresh install records no events yet.
// Load validates entry schema but does not prune expired entries; callers prune
// against their own notion of time (#61 step 1).
func Load(path string) (Ledger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Ledger{}, nil
	}
	if err != nil {
		return Ledger{}, fmt.Errorf("read ledger %s: %w", path, err)
	}

	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return Ledger{}, fmt.Errorf("decode ledger %s: %w", path, err)
	}

	for i, e := range l.Entries {
		var why string
		switch {
		case e.Kind != SpawnFailed && e.Kind != RateLimited:
			why = fmt.Sprintf("unknown kind %q", e.Kind)
		case e.Subject == "":
			why = "subject is empty"
		case e.At.IsZero():
			why = "at is zero"
		case !e.Until.IsZero() && e.Until.Before(e.At):
			why = "until precedes at"
		case e.Source != "relay" && e.Source != "planner":
			why = fmt.Sprintf("source must be \"relay\" or \"planner\" (got %q)", e.Source)
		}
		if why != "" {
			return Ledger{}, fmt.Errorf("ledger %s: entry %d: %s: %w", path, i, why, ErrBadEntry)
		}
	}

	return l, nil
}

// Save writes the ledger to disk atomically via a temporary file and rename,
// ensuring concurrent readers never observe torn writes. It creates any missing
// parent directories so callers need not ensure state root existence (#61 step 1).
func Save(path string, l Ledger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}

	data, err := json.MarshalIndent(l, "", "  ")
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
// It does not mutate the receiver's slice.
func (l Ledger) Prune(now time.Time) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if !e.Expired(now) {
			kept = append(kept, e)
		}
	}
	return Ledger{Entries: append([]Entry(nil), kept...)}
}

// Append returns a new Ledger with e added to the end.
// It performs no deduplication: two spawn failures are two events; step 7 counts them.
// It does not mutate the receiver's slice.
func (l Ledger) Append(e Entry) Ledger {
	cp := append([]Entry(nil), l.Entries...)
	return Ledger{Entries: append(cp, e)}
}

// Clear returns a new Ledger without every entry whose Kind == kind and Subject == subject.
// It does not mutate the receiver's slice.
func (l Ledger) Clear(kind Kind, subject string) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if e.Kind == kind && e.Subject == subject {
			continue
		}
		kept = append(kept, e)
	}
	return Ledger{Entries: append([]Entry(nil), kept...)}
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
