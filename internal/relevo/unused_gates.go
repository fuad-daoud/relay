package relevo

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/ledger"
)

// ProviderGate is one live rate-limit gate on a provider no configured
// candidate uses. It is the ledger entry's own facts, unprojected: with no
// candidate token on that provider there is nothing to project them onto.
type ProviderGate struct {
	Provider string
	Since    time.Time
	Until    time.Time
	Note     string
	Source   string
	Binding  string
}

// UnusedProviderGates returns the live rate limits on providers outside the
// configured candidate set. A missing gates store or candidate set is nil:
// with nothing to compare against there are no unused providers. A ledger load
// failure reads as nil too, because Gates reports that error once on stderr
// already and a bookkeeping file must not take status down.
func UnusedProviderGates(rt Runtime) []ProviderGate {
	if rt.Gates == nil || rt.Candidates == nil {
		return nil
	}

	l, err := ledger.LoadKV(rt.Gates, ledgerLegacyPath(rt))
	if err != nil {
		return nil
	}

	used := make(map[string]bool)
	for _, p := range rt.Candidates.Providers() {
		used[p] = true
	}

	var out []ProviderGate
	for _, e := range l.Prune(rt.Now()).Entries {
		if e.Kind != ledger.RateLimited || used[e.Subject] {
			continue
		}
		out = append(out, ProviderGate{
			Provider: e.Subject,
			Since:    e.At,
			Until:    e.Until,
			Note:     e.Note,
			Source:   e.Source,
			Binding:  e.Binding,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}
