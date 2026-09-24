package relevo

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// TabEntry is one log entry with the binding it came from; cmd/relevo
// collects them from live logs and archives, TabRows sums them. Owner is the
// owner's label when the entries come from a server (`relevo serve tab`); it
// is "" on a client.
type TabEntry struct {
	Binding string
	Owner   string
	Entry   store.LogEntry
}

// TabRow is one group's total.
type TabRow struct {
	Group string      `json:"group"`
	Spend usage.Spend `json:"spend"`
}

// TabReport is what `relevo tab --json` prints.
type TabReport struct {
	Since *time.Time  `json:"since"`
	By    string      `json:"by"`
	Rows  []TabRow    `json:"rows"`
	Total usage.Spend `json:"total"`
}

var (
	// ErrBadSince is histq.ErrBadSince. ParseSince's body moved to
	// internal/histq so the query language can share it; the alias keeps
	// errors.Is(err, ErrBadSince) true for every caller written before.
	ErrBadSince = histq.ErrBadSince
	ErrBadBy    = errors.New("--by wants binding, model, provider or owner")
)

// ParseSince turns "" (zero: no cut), "24h", "7d" or "2026-09-01" into
// the instant before which entries are ignored. The body lives in
// internal/histq now, shared with `relevo history -q`; this wrapper is what
// every existing caller and test in this package keeps using.
func ParseSince(s string, now time.Time) (time.Time, error) { return histq.ParseSince(s, now) }

// TabEntries gathers the entries `relevo tab` sums, from rt's live bindings'
// logs and then from its archived records. An archived record older than cut
// is skipped whole, since every entry in it predates the archive itself. An
// unreadable archived log is reported through warn and skipped, not failed. It
// is the gather half shared by the client's `relevo history --tab` and the
// server's `relevo serve tab` (P3d §4.4).
func TabEntries(rt Runtime, cut time.Time, warn func(string)) ([]TabEntry, error) {
	var entries []TabEntry
	live, err := rt.Store.List()
	if err != nil {
		return nil, err
	}
	for _, b := range live {
		log, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name, err)
		}
		for _, e := range log {
			entries = append(entries, TabEntry{Binding: b.Name, Entry: e})
		}
	}
	archived, err := rt.Store.ListArchived()
	if err != nil {
		return nil, err
	}
	for _, a := range archived {
		if !cut.IsZero() && a.ArchivedAt.Before(cut) {
			continue // every entry in it predates the archive itself
		}
		log, err := rt.Store.ArchivedLog(a.RecordID)
		if err != nil {
			warn(fmt.Sprintf("%s: %v", a.Binding.Name, err))
			continue
		}
		for _, e := range log {
			entries = append(entries, TabEntry{Binding: a.Binding.Name, Entry: e})
		}
	}
	return entries, nil
}

// modelSansEffort cuts a model at its first "#", the candidate's effort
// suffix, or returns it unchanged when it has none. A ":effort" suffix
// belongs to the model and stays, as it does in ingest's stripEffortHash.
func modelSansEffort(model string) string {
	if i := strings.IndexByte(model, '#'); i >= 0 {
		return model[:i]
	}
	return model
}

func tabKey(e TabEntry, by string) string {
	u := e.Entry.Usage
	switch by {
	case "binding":
		return e.Binding
	case "owner":
		return e.Owner
	case "provider":
		if u.Provider == "" {
			return "unknown"
		}
		return u.Provider
	default: // model
		if u.Provider == "" && u.Model == "" {
			return "unknown"
		}
		return strings.Trim(u.Provider+"/"+modelSansEffort(u.Model), "/")
	}
}

// TabRows groups report and findings entries that carry usage, from since
// on (zero: all), by "binding", "model", "provider" or "owner", and returns
// the rows sorted by group and the grand total.
func TabRows(entries []TabEntry, by string, since time.Time) ([]TabRow, usage.Spend, error) {
	switch by {
	case "binding", "model", "provider", "owner":
	default:
		return nil, usage.Spend{}, fmt.Errorf("%q: %w", by, ErrBadBy)
	}
	groups := map[string]usage.Spend{}
	var total usage.Spend
	for _, e := range entries {
		u := e.Entry.Usage
		if u == nil || (e.Entry.Kind != store.KindReport && e.Entry.Kind != store.KindFindings) {
			continue
		}
		if !since.IsZero() && e.Entry.TS.Before(since) {
			continue
		}
		one := usage.Sum([]usage.Usage{*u}, []bool{e.Entry.Kind == store.KindFindings})
		k := tabKey(e, by)
		groups[k] = groups[k].Add(one)
		total = total.Add(one)
	}
	rows := make([]TabRow, 0, len(groups))
	for k, s := range groups {
		rows = append(rows, TabRow{Group: k, Spend: s})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Group < rows[j].Group })
	return rows, total, nil
}

// RenderTab prints the table. Empty cells stay empty where a zero is
// noise -- no "$0.00" for a group with nothing measured, no "0" for zero
// plan or unknown rounds -- while the token cells print the split they
// name, "0" included, so a reader learns the column exists (#234). The
// step cells follow the token rule's other half (#323, #324): an empty
// `steps` at 0 and an empty `calls/st` when the group recorded no steps.
func RenderTab(r TabReport) string {
	if len(r.Rows) == 0 {
		return "no rounds with usage\n"
	}
	width := 5
	for _, row := range r.Rows {
		if len(row.Group) > width {
			width = len(row.Group)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*s  %6s  %6s  %8s  %7s  %7s  %7s  %7s  %9s  %9s  %4s  %7s\n", width, "group", "rounds", "steps", "calls/st", "in", "cache", "write", "out", "measured", "estimated", "plan", "unknown")
	line := func(group string, s usage.Spend) {
		rounds := strconv.Itoa(s.Rounds)
		if s.Consults > 0 {
			rounds += fmt.Sprintf("+%dc", s.Consults)
		}
		cell := func(n int) string {
			if n == 0 {
				return ""
			}
			return strconv.Itoa(n)
		}
		callsPerStep := ""
		if s.Steps > 0 {
			callsPerStep = fmt.Sprintf("%.2f", float64(s.ToolCalls)/float64(s.Steps))
		}
		money := func(usd float64, basis usage.Basis) string {
			if usd == 0 {
				return ""
			}
			return usage.Money(usage.Cost{USD: usd, Basis: basis})
		}
		fmt.Fprintf(&sb, "%-*s  %6s  %6s  %8s  %7s  %7s  %7s  %7s  %9s  %9s  %4s  %7s\n", width, group, rounds, cell(s.Steps), callsPerStep,
			usage.ShortTokens(s.Tokens.In), usage.ShortTokens(s.Tokens.CacheRead),
			usage.ShortTokens(s.Tokens.CacheWrite), usage.ShortTokens(s.Tokens.Out),
			money(s.Measured, usage.Measured), money(s.Estimated, usage.Estimated), cell(s.Plan), cell(s.Unknown))
	}
	for _, row := range r.Rows {
		line(row.Group, row.Spend)
	}
	line("total", r.Total)
	return sb.String()
}
