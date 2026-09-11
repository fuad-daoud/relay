package relay

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

// providerOf resolves a candidate token to its provider, or "" if the token
// does not parse. It is the one place Gates and appendEntryLocked's history
// mirror agree on what a token's provider is.
func providerOf(tok string) string {
	r, err := candidate.ParseRef(tok)
	if err != nil {
		return ""
	}
	return r.Provider
}

// SpawnFailedCooldown is how long a spawn failure gates its candidate. A
// constant, not config: a failed StartAgent is nearly always a pane race
// or a binary mid-upgrade, and ten minutes outlasts both. #61 step 2 may
// move it to policy.json with the other cooldowns.
const SpawnFailedCooldown = 10 * time.Minute

// mutateLedger loads, prunes, applies fn and saves the ledger under the
// state lock, so a bind recording a failure and a planner running
// `relay unavailable` in another pane serialise on the flock that already
// serialises bind.json (spec §3.3).
func mutateLedger(rt Runtime, fn func(ledger.Ledger) ledger.Ledger) error {
	return rt.Store.WithLock(func(*store.Tx) error {
		return mutateLedgerLocked(rt, fn)
	})
}

// mutateLedgerLocked is mutateLedger for a caller that already holds the
// state lock -- switchBuilder, via resolveBuilder's tx parameter (#61 step
// 6). It does the same load-prune-apply-save without taking Store.WithLock
// itself, since that lock is a plain mutex and is not reentrant: a second
// Lock from the same goroutine that already holds it blocks forever rather
// than erroring.
func mutateLedgerLocked(rt Runtime, fn func(ledger.Ledger) ledger.Ledger) error {
	l, err := ledger.Load(rt.LedgerPath)
	if err != nil {
		return err
	}
	l = fn(l.Prune(rt.Now()))
	return ledger.Save(rt.LedgerPath, l)
}

// appendEntryLocked commits one observation: the ledger entry that gates,
// then its mirror in the history that remembers (#61 step 7). The caller
// holds the store lock. A history failure is printed and dropped -- the
// ledger write is the one that matters, and it already happened.
func appendEntryLocked(rt Runtime, e ledger.Entry) error {
	if err := mutateLedgerLocked(rt, func(l ledger.Ledger) ledger.Ledger { return l.Append(e) }); err != nil {
		return err
	}
	h, err := history.Load(rt.HistoryPath)
	if err == nil {
		err = history.Save(rt.HistoryPath, h.Prune(rt.Now()).Append(history.FromEntry(e, providerOf)))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not record history: %v\n", err)
	}
	return nil
}

// recordSpawnFailureWith notes that relay failed to start token's process
// for binding, committing the record through appendEntryLocked -- taking the
// state lock itself when !locked, or running directly when the caller
// already holds it. recordSpawnFailure and recordSpawnFailureLocked are this
// function with locked threaded through, so the two never drift on what a
// spawn failure looks like or on the never-returns-an-error rule.
//
// It never returns an error: a failed bookkeeping write must not mask the
// spawn error the caller is about to return, so a write failure is printed
// to stderr and dropped instead (spec §4.1).
func recordSpawnFailureWith(rt Runtime, locked bool, token, binding string, cause error) {
	now := rt.Now()
	entry := ledger.Entry{
		Kind:    ledger.SpawnFailed,
		Subject: token,
		At:      now,
		Until:   now.Add(SpawnFailedCooldown),
		Note:    cause.Error(),
		Source:  "relay",
		Binding: binding,
	}
	commit := func() error { return appendEntryLocked(rt, entry) }

	var err error
	if locked {
		err = commit()
	} else {
		err = rt.Store.WithLock(func(*store.Tx) error { return commit() })
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not record spawn failure: %v\n", err)
	}
}

// recordSpawnFailure notes that relay failed to start token's process for
// binding. It takes the state lock itself; a caller that already holds it
// must use recordSpawnFailureLocked instead, or this function deadlocks
// re-entering the lock (#61 step 6).
func recordSpawnFailure(rt Runtime, token, binding string, cause error) {
	recordSpawnFailureWith(rt, false, token, binding, cause)
}

// recordSpawnFailureLocked is recordSpawnFailure for a caller that already
// holds the state lock: switchBuilder, reached through resolveBuilder's tx
// parameter when a switch's replacement spawn fails (#61 step 6). Same
// record, same never-returns-an-error contract, just committed directly
// instead of re-taking Store.WithLock.
func recordSpawnFailureLocked(rt Runtime, token, binding string, cause error) {
	recordSpawnFailureWith(rt, true, token, binding, cause)
}

// Unavailable records that token's provider is rate-limited, so every
// candidate sharing that provider shows as gated -- a quota is enforced per
// subscription or key, not per model (spec §1). token must resolve to a
// configured candidate: a typo is refused rather than recorded.
func Unavailable(rt Runtime, token string, until time.Time, reason string) (provider string, err error) {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return "", err
	}
	if _, err := rt.Candidates.Lookup(ref); err != nil {
		return "", err
	}

	now := rt.Now()
	entry := ledger.Entry{
		Kind:    ledger.RateLimited,
		Subject: ref.Provider,
		At:      now,
		Until:   until,
		Note:    reason,
		Source:  "planner",
	}
	if err := rt.Store.WithLock(func(*store.Tx) error { return appendEntryLocked(rt, entry) }); err != nil {
		return "", err
	}

	return ref.Provider, nil
}

// Available clears every rate-limit gate on subject's provider. subject may
// be a candidate token or a bare provider name; a token is resolved to its
// provider first. Zero removed is not an error -- the caller reports that
// nothing was gating the provider.
func Available(rt Runtime, subject string) (provider string, removed int, err error) {
	provider = subject
	if ref, perr := candidate.ParseRef(subject); perr == nil {
		provider = ref.Provider
	}

	err = mutateLedger(rt, func(l ledger.Ledger) ledger.Ledger {
		for _, e := range l.Entries {
			if e.Kind == ledger.RateLimited && e.Subject == provider {
				removed++
			}
		}
		return l.Clear(ledger.RateLimited, provider)
	})
	if err != nil {
		return provider, 0, err
	}

	return provider, removed, nil
}

// Gates is what every reader renders from: the live ledger projected onto
// the configured candidates. A load error is reported once on stderr and
// read as an empty ledger -- status, candidates and doctor must not go
// down over a bookkeeping file (spec §6).
func Gates(rt Runtime) []ledger.Gate {
	if rt.Candidates == nil {
		return nil
	}

	l, err := ledger.Load(rt.LedgerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not read ledger: %v\n", err)
		return nil
	}

	return ledger.Gated(l, rt.Candidates.Refs(), providerOf, rt.Now())
}

// GateKindText is the human wording for a gate kind in status, candidates
// and doctor, so the three never drift: "spawn failed", "rate-limited".
func GateKindText(k ledger.Kind) string {
	switch k {
	case ledger.SpawnFailed:
		return "spawn failed"
	case ledger.RateLimited:
		return "rate-limited"
	default:
		return string(k)
	}
}

// GateUntilText renders Until as "until HH:MM" in local time, or
// "until cleared" for a zero Until.
func GateUntilText(until time.Time) string {
	if until.IsZero() {
		return "until cleared"
	}
	return "until " + until.Local().Format("15:04")
}

// gatedNote is the one advisory line bind, add, fork and ask print after a
// successful spawn of a candidate the ledger says is gated. Advisory only:
// the agent is already running, and refusing is #61 step 4's job.
func gatedNote(rt Runtime, token string) string {
	var parts []string
	for _, g := range Gates(rt) {
		if g.Token != token {
			continue
		}
		part := fmt.Sprintf("%s since %s %s", GateKindText(g.Kind), g.Since.Local().Format("15:04"), GateUntilText(g.Until))
		if g.Note != "" {
			part += ": " + g.Note
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("note: %s is gated: %s; proceeding", token, strings.Join(parts, "; "))
}

// GatedNote is gatedNote exported for cmd/relay, which prints it to stderr
// after bind, add, fork and ask spawn successfully (T4).
func GatedNote(rt Runtime, token string) string {
	return gatedNote(rt, token)
}

// BindingsOnProvider names the active bindings with an open round whose
// builder runs on provider, sorted: the ones the daemon will switch once
// that provider is gated (spec §4.6). Pure, for cmdUnavailable's note.
func BindingsOnProvider(bindings []store.Binding, provider string) []string {
	var names []string
	for _, b := range bindings {
		if b.State != store.StateActive {
			continue
		}
		if b.RoundStartedAt.IsZero() {
			continue
		}
		if b.BuilderCandidate == "" {
			continue
		}
		ref, err := candidate.ParseRef(b.BuilderCandidate)
		if err != nil || ref.Provider != provider {
			continue
		}
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}
