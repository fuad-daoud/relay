package relevo

import (
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/latency"
	"github.com/fuad-daoud/relevo/internal/stats"
)

// StatsInputs assembles stats.Inputs for the window starting at since (zero =
// all) ending at rt.Now(). rt.DB must be open. Read failures of latency and
// history are returned as warnings, never as errors. It is exactly the input
// assembly `relevo history --stats` used, moved here so the CLI and the
// cockpit's `:stats` view cannot disagree (cockpit C2b §4.1).
func StatsInputs(rt Runtime, since time.Time) (in stats.Inputs, warnings []string, err error) {
	rows, err := rt.DB.Query(db.Filter{Since: since})
	if err != nil {
		return stats.Inputs{}, nil, err
	}
	landed := map[string]bool{}
	done, err := rt.DB.Bindings(db.Filter{State: "done"})
	if err != nil {
		return stats.Inputs{}, nil, err
	}
	for _, b := range done {
		landed[b.ID] = true
	}

	// Latency is read exactly as formatCandidates reads it: a failure warns
	// and leaves the report without ttft values.
	var lat latency.History
	if rt.Latency != nil {
		loaded, lerr := latency.LoadKV(rt.Latency, LegacyGatesPath(rt.GatesDir, "latency.json"))
		if lerr != nil {
			warnings = append(warnings, "could not read latency: "+lerr.Error())
		} else {
			lat = loaded
		}
	}
	// The shell's clock is read once, so the prune and the report's open end
	// agree. rt.Now is time.Now in production; the guard keeps a zero Runtime
	// (a test) usable.
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	lat = lat.Prune(now)

	hist, herr := LoadHistory(rt)
	if herr != nil {
		warnings = append(warnings, "could not read history: "+herr.Error())
	}

	return stats.Inputs{
		Rows:    rows,
		Landed:  landed,
		History: hist,
		Gates:   Gates(rt),
		TTFT: func(token string) (int64, bool) {
			s := lat.Summary(token)
			return s.TTFTP50MS, s.N > 0
		},
		IsPlan: func(token string) bool {
			if rt.Candidates == nil {
				return false
			}
			ref, rerr := candidate.ParseRef(token)
			if rerr != nil {
				return false
			}
			c, lerr := rt.Candidates.Lookup(ref)
			if lerr != nil {
				return false
			}
			return c.Plan
		},
		Since: since,
		Until: now,
		Loc:   time.Local,
	}, warnings, nil
}

// LoadHistory reads the availability history for display, treating an
// unreadable record as an error the caller reports -- the same rule Gates
// applies to the ledger. It is the moved body of cmd/relevo's loadHistory
// (cockpit C2b §4.1).
func LoadHistory(rt Runtime) (history.History, error) {
	if rt.Gates == nil {
		return history.History{}, nil
	}
	h, err := history.LoadKV(rt.Gates, LegacyGatesPath(rt.GatesDir, "availability.json"))
	if err != nil {
		return history.History{}, err
	}
	return h.Prune(rt.Now()), nil
}

// LegacyGatesPath is <dir>/<name> for the pre-kv gate documents (ledger.json,
// availability.json, latency.json), or "" when no gates directory is
// configured -- so an import never reads a file out of the process's working
// directory (P3b plan §4.5). It is the moved body of cmd/relevo's
// legacyGatesPath (cockpit C2b §4.1).
func LegacyGatesPath(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}
