package relay

import (
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
)

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
		l, err := ledger.Load(rt.LedgerPath)
		if err != nil {
			return err
		}
		l = fn(l.Prune(rt.Now()))
		return ledger.Save(rt.LedgerPath, l)
	})
}

// recordSpawnFailure notes that relay failed to start token's process for
// binding. It never returns an error: a failed bookkeeping write must not
// mask the spawn error the caller is about to return, so a write failure is
// printed to stderr and dropped instead (spec §4.1).
func recordSpawnFailure(rt Runtime, token, binding string, cause error) {
	now := rt.Now()
	err := mutateLedger(rt, func(l ledger.Ledger) ledger.Ledger {
		return l.Append(ledger.Entry{
			Kind:    ledger.SpawnFailed,
			Subject: token,
			At:      now,
			Until:   now.Add(SpawnFailedCooldown),
			Note:    cause.Error(),
			Source:  "relay",
			Binding: binding,
		})
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay: could not record spawn failure: %v\n", err)
	}
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
	if err := mutateLedger(rt, func(l ledger.Ledger) ledger.Ledger {
		return l.Append(ledger.Entry{
			Kind:    ledger.RateLimited,
			Subject: ref.Provider,
			At:      now,
			Until:   until,
			Note:    reason,
			Source:  "planner",
		})
	}); err != nil {
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
