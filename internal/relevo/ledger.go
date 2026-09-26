package relevo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNoGates is what a gate write reports when the runtime carries no gates
// store (P3b plan §4.5): a nil Gates means gates read as empty and writes are
// dropped with this error.
var ErrNoGates = errors.New("no gates store configured")

// ledgerLegacyPath, availabilityLegacyPath and latencyLegacyPath are the pre-kv
// files LoadKV imports on the first read of their rows. They live in GatesDir
// -- normally the store root -- and are "" when no directory is set, so an
// import never reads a file out of the process's working directory.
func ledgerLegacyPath(rt Runtime) string {
	if rt.GatesDir == "" {
		return ""
	}
	return filepath.Join(rt.GatesDir, "ledger.json")
}

func availabilityLegacyPath(rt Runtime) string {
	if rt.GatesDir == "" {
		return ""
	}
	return filepath.Join(rt.GatesDir, "availability.json")
}

func latencyLegacyPath(rt Runtime) string {
	if rt.GatesDir == "" {
		return ""
	}
	return filepath.Join(rt.GatesDir, "latency.json")
}

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
// constant, not config: a failed start is nearly always a binary mid-upgrade,
// and ten minutes outlasts that. #61 step 2 may move it to policy.json with
// the other cooldowns.
const SpawnFailedCooldown = 10 * time.Minute

// mutateLedgerLocked loads, prunes, applies fn and saves the ledger for a
// caller that already holds the state lock, so every ledger write
// serialises on the flock that already serialises bind.json (spec §3.3).
// It does not take Store.WithLock itself: that lock is a plain mutex and is
// not reentrant, so a second Lock from the goroutine that already holds it
// blocks forever rather than erroring. (Its unlocked twin, mutateLedger,
// went with #302: Available was its last caller.)
func mutateLedgerLocked(rt Runtime, fn func(ledger.Ledger) ledger.Ledger) error {
	if rt.Gates == nil {
		return ErrNoGates
	}
	l, err := ledger.LoadKV(rt.Gates, ledgerLegacyPath(rt))
	if err != nil {
		return err
	}
	l = fn(l.Prune(rt.Now()))
	return ledger.SaveKV(rt.Gates, l)
}

// appendEntryLocked commits one observation: the ledger entry that gates,
// then its mirror in the history that remembers (#61 step 7). The caller
// holds the store lock. A history failure is printed and dropped -- the
// ledger write is the one that matters, and it already happened.
func appendEntryLocked(rt Runtime, e ledger.Entry) error {
	if err := mutateLedgerLocked(rt, func(l ledger.Ledger) ledger.Ledger { return l.Append(e) }); err != nil {
		return err
	}
	h, err := history.LoadKV(rt.Gates, availabilityLegacyPath(rt))
	if err == nil {
		err = history.SaveKV(rt.Gates, h.Prune(rt.Now()).Append(history.FromEntry(e, providerOf)))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", err)
	}
	return nil
}

// recordSpawnFailureWith notes that relevo failed to start token's process
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
		Source:  "relevo",
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
		fmt.Fprintf(os.Stderr, "relevo: could not record spawn failure: %v\n", err)
	}
}

// recordSpawnFailure notes that relevo failed to start token's process for
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
// subscription or key, not per model (spec §1). token (a candidate name or a
// canonical token) must resolve to a configured candidate: a typo is refused
// rather than recorded.
func Unavailable(rt Runtime, token string, until time.Time, reason string) (provider string, err error) {
	c, err := rt.Candidates.Resolve(token)
	if err != nil {
		return "", err
	}
	ref := c.Ref()

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
// be a candidate token or a bare provider name; ResolveClearSubject decides
// what it names and refuses one relevo knows nothing about (#301). A clear
// that removed anything is recorded in the availability history as a
// Cleared event whose Source is source (#302). Zero removed is not an error
// and records nothing -- the caller reports that nothing was gating.
//
// source must be ClearedByPlanner or ClearedByServer; anything else is a
// programming error and returns an error before any write.
func Available(rt Runtime, subject, source string) (provider string, removed int, err error) {
	if source != ClearedByPlanner && source != ClearedByServer {
		return "", 0, fmt.Errorf("available: unknown clear source %q", source)
	}
	if rt.Gates == nil {
		return "", 0, ErrNoGates
	}

	var oldest time.Time

	err = rt.Store.WithLock(func(*store.Tx) error {
		l, lerr := ledger.LoadKV(rt.Gates, ledgerLegacyPath(rt))
		if lerr != nil {
			return lerr
		}
		l = l.Prune(rt.Now())

		provider, err = ResolveClearSubject(rt.Candidates, l, subject)
		if err != nil {
			return err
		}

		for _, e := range l.Entries {
			if e.Kind != ledger.RateLimited || e.Subject != provider {
				continue
			}
			removed++
			if oldest.IsZero() || e.At.Before(oldest) {
				oldest = e.At
			}
		}

		if serr := ledger.SaveKV(rt.Gates, l.Clear(ledger.RateLimited, provider)); serr != nil {
			return serr
		}

		if removed > 0 {
			ev := history.Event{
				At:       rt.Now(),
				Kind:     history.Cleared,
				Provider: provider,
				Source:   source,
				Note:     fmt.Sprintf("cleared %d entries", removed),
				Since:    oldest,
			}
			h, herr := history.LoadKV(rt.Gates, availabilityLegacyPath(rt))
			if herr == nil {
				herr = history.SaveKV(rt.Gates, h.Prune(rt.Now()).Append(ev))
			}
			if herr != nil {
				fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", herr)
			}
		}

		return nil
	})
	if err != nil {
		return provider, 0, err
	}

	return provider, removed, nil
}

// ledgerGates projects the live ledger onto tokens, whether or not the configured set holds them:
// a rate limit gates every token of its provider, a spawn failure gates its own token.
func ledgerGates(rt Runtime, tokens []string) []ledger.Gate {
	if rt.Gates == nil {
		return nil
	}

	l, err := ledger.LoadKV(rt.Gates, ledgerLegacyPath(rt))
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read ledger: %v\n", err)
		return nil
	}

	return ledger.Gated(l, tokens, providerOf, rt.Now())
}

// Gates is what every reader renders from: the live ledger projected onto
// the configured candidates. A load error is reported once on stderr and
// read as an empty ledger -- status, candidates and doctor must not go
// down over a bookkeeping file (spec §6).
func Gates(rt Runtime) []ledger.Gate {
	if rt.Candidates == nil {
		return nil
	}

	gates := ledgerGates(rt, rt.Candidates.Refs())
	gates = append(gates, rolesMissingGates(rt)...)

	// A1 §4.4, round 3 F3: every gate carries the candidate's short name
	// when the set holds its token, so the gates block and `relevo serve
	// gates` can print it. A token no longer configured leaves Name empty
	// and the renderers fall back to printing the token. The token stays the
	// gate's identity.
	for i := range gates {
		if name, ok := rt.Candidates.NameFor(gates[i].Token); ok {
			gates[i].Name = name
		}
	}
	return gates
}

// rolesMissingGates synthesises an in-memory ledger.RolesMissing gate for
// every (candidate, role) pair whose role's resolved definitions are not all
// on disk, per rt.Roles (#238), the same way ledger.ExitedNoReport is
// synthesised from Binding.RoundExcluded rather than read from the ledger
// file. Each gate carries the role it belongs to, so resolveRole can ignore
// the gates of every other role. nil when rt.Roles is nil: no checker
// configured (every test that does not set one, and every caller before
// cmd/relevo wires harness.OSRoleChecker()).
func rolesMissingGates(rt Runtime) []ledger.Gate {
	if rt.Roles == nil || rt.Candidates == nil {
		return nil
	}

	reg := rt.RoleRegistry()

	// Missing is called once per distinct (kind, definition list), not once
	// per candidate or role: several candidates commonly share a kind, and a
	// role's definition list is usually the shipped one.
	cache := map[string][]string{}

	var out []ledger.Gate
	for _, ref := range rt.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		kind := r.Harness
		for _, role := range reg.Names() {
			if !reg.Serves(role, r) {
				continue
			}
			spec, err := reg.Spec(role, kind)
			if err != nil {
				continue
			}
			key := kind + "\x00" + strings.Join(spec.Definitions, ",")
			paths, ok := cache[key]
			if !ok {
				paths = rt.Roles.Missing(kind, spec.Definitions)
				cache[key] = paths
			}
			if len(paths) == 0 {
				continue
			}
			out = append(out, ledger.Gate{
				Token:  ref,
				Kind:   ledger.RolesMissing,
				Role:   role,
				Since:  rt.Now(),
				Note:   rolesMissingNote(role, kind, spec.Definitions, paths),
				Source: "relevo",
			})
		}
	}
	return out
}

// rolesMissingNote is one roles-missing gate's note: which of role's
// definitions are missing on kind, and how to fix each class of them. A
// shipped path is installed by `relevo config agents`; a custom one may be
// rendered from a source agent by that same command, or be the user's own
// native definition, so its fix names both.
func rolesMissingNote(role, kind string, defs, paths []string) string {
	var shipped, custom []string
	for _, path := range paths {
		if definitionIsShipped(kind, defs, path) {
			shipped = append(shipped, path)
			continue
		}
		custom = append(custom, path)
	}

	var fixes []string
	if len(shipped) > 0 {
		fixes = append(fixes, "run relevo config agents --kind "+kind)
	}
	if len(custom) > 0 {
		fixes = append(fixes, "run relevo config agents --kind "+kind+" for a custom agent relevo renders, or install "+strings.Join(custom, ", ")+" yourself")
	}
	return "roles missing for " + role + ": " + strings.Join(paths, ", ") + "; " + strings.Join(fixes, "; ")
}

// definitionIsShipped reports whether path is one of defs' shipped paths for
// kind: it resolves every name through DefinitionPath and asks IsShipped about
// the one that lands on path.
func definitionIsShipped(kind string, defs []string, path string) bool {
	for _, name := range defs {
		p, ok := harness.DefinitionPath(kind, name)
		if !ok || p != path {
			continue
		}
		return harness.IsShipped(kind, name)
	}
	return false
}

// GateKindText is the human wording for a gate kind in status, candidates
// and doctor, so the three never drift: "spawn failed", "rate-limited".
func GateKindText(k ledger.Kind) string {
	switch k {
	case ledger.SpawnFailed:
		return "spawn failed"
	case ledger.RateLimited:
		return "rate-limited"
	case ledger.ExitedNoReport:
		return "exited without a report"
	case ledger.RolesMissing:
		return "roles missing"
	default:
		return string(k)
	}
}

// gateClock is the "now" GateTimeText compares a gate time against. Tests
// override it and restore it with t.Cleanup.
var gateClock = time.Now

// SetGateClock replaces the clock gate times are formatted against and
// returns a func that restores the previous one. For tests that render at a
// fixed time, in this package and others (internal/ui); production never
// calls it. Not safe to use from parallel tests.
func SetGateClock(now func() time.Time) (restore func()) {
	prev := gateClock
	gateClock = now
	return func() { gateClock = prev }
}

// GateTimeText renders a gate time in local time: the clock time alone when
// it falls on today's local date, the date as well otherwise, so a gate
// hours or days out never reads as later today.
func GateTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt, ln := t.Local(), gateClock().Local()
	if lt.Year() == ln.Year() && lt.Month() == ln.Month() && lt.Day() == ln.Day() {
		return lt.Format("15:04")
	}
	if lt.Year() == ln.Year() {
		return lt.Format("Jan 2 15:04")
	}
	return lt.Format("2006-01-02 15:04")
}

// GateUntilText renders Until as "until <gate time>" in local time, or
// "until cleared" for a zero Until.
func GateUntilText(until time.Time) string {
	if until.IsZero() {
		return "until cleared"
	}
	return "until " + GateTimeText(until)
}

// gatedNote is the one advisory line bind, add, fork and ask print after a
// successful spawn of a candidate the ledger says is gated. Advisory only:
// the agent is already running, and refusing is #61 step 4's job.
func gatedNote(rt Runtime, token string) string {
	var parts []string
	for _, g := range append(ledgerGates(rt, []string{token}), rolesMissingGates(rt)...) {
		if g.Token != token {
			continue
		}
		part := fmt.Sprintf("%s since %s %s", GateKindText(g.Kind), GateTimeText(g.Since), GateUntilText(g.Until))
		if g.Note != "" {
			part += ": " + g.Note
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}

	// A1 §4.4: the note names the candidate by its short name; a token no
	// longer configured reads as itself (NameOf returns it unchanged).
	return fmt.Sprintf("note: %s is gated: %s; proceeding", rt.Candidates.NameOf(token), strings.Join(parts, "; "))
}

// GatedNote is gatedNote exported for cmd/relevo, which prints it to stderr
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
