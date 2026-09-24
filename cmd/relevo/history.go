package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/stats"
)

// historyJSONRow is one `relevo history --json` row: db.RoundRow's fields
// plus the candidate's short name beside the token (A1 §4.4). The embedded
// struct keeps every token field where it always was.
type historyJSONRow struct {
	db.RoundRow
	BuilderName string `json:"BuilderName,omitempty"`
}

// historyJSONRows wraps each row with its candidate's short name as set
// resolves it. A token the set no longer holds stays as the name, exactly as
// the text listing renders it (Set.NameOf never errors).
func historyJSONRows(rows []db.RoundRow, set *candidate.Set) []historyJSONRow {
	out := make([]historyJSONRow, len(rows))
	for i, r := range rows {
		row := historyJSONRow{RoundRow: r}
		if r.BuilderCandidate != nil {
			row.BuilderName = set.NameOf(*r.BuilderCandidate)
		}
		out[i] = row
	}
	return out
}

const historyUsage = `usage: relevo history [--here|--repo <url|dir>] [--feature L] [--binding N] [--planner S]
                     [--harness K] [--provider P] [--model M] [--candidate T]
                     [--outcome O] [--since D] [--until D] [--archived|--live]
                     [--limit N] [--json] [-q "<query>"] [--by <axis>] [--rows]
       relevo history --tab [--owner <label|id|all>] [--since D] [--by binding|model|provider|owner] [--json] [--state <dir>]
       relevo history --stats [--since D|all] [--json]`

// historyOutcomeValues lists the round.Outcome enum in the order
// docs/specs/2026-09-20-persistence-design.md §3 decision 8 states it, for
// the --outcome usage error.
var historyOutcomeValues = []string{
	db.OutcomeReported, db.OutcomeHalted, db.OutcomeExited,
	db.OutcomeSwitched, db.OutcomeDoneNoReport, db.OutcomeOpen,
}

// validateHistoryFlags checks the two combinations `relevo history` rejects
// with a usage error: --archived with --live, and an --outcome outside the
// enum. It is a pure function, factored out so a cmd/relevo test can pin the
// rule without executing the subcommand (CI launches no harness).
func validateHistoryFlags(archived, live bool, outcome string) error {
	if archived && live {
		return fmt.Errorf("--archived and --live are mutually exclusive")
	}
	if outcome != "" && !db.ValidOutcome(outcome) {
		return fmt.Errorf("--outcome %q: want one of %s", outcome, strings.Join(historyOutcomeValues, ", "))
	}
	return nil
}

// validateHistoryBy checks that --by names one of histq's ten axes. It is a
// pure function, so a cmd/relevo test can pin the rule without executing the
// subcommand (CI launches no harness).
func validateHistoryBy(by string) error {
	if by == "" {
		return nil
	}
	if _, ok := histq.ParseAxis(by); !ok {
		return fmt.Errorf("--by %q: want one of %s", by, strings.Join(historyAxisNames(), ", "))
	}
	return nil
}

// historyTabAllowed and historyStatsAllowed are the flags `relevo history
// --tab` and `relevo history --stats` may be combined with. Nothing else on
// history's flag set is: a tab total is not a round filter (P3d §4.6).
var (
	historyTabAllowed   = map[string]bool{"tab": true, "stats": true, "since": true, "json": true, "by": true, "owner": true, "state": true}
	historyStatsAllowed = map[string]bool{"tab": true, "stats": true, "since": true, "json": true}
)

// validateHistoryTabStats rejects --tab and --stats together, and either of
// them beside any history flag the form does not document, naming the flag. It
// is a pure function, so a cmd/relevo test can pin the rule without executing
// the subcommand (CI launches no harness).
func validateHistoryTabStats(fs *flag.FlagSet, tab, stats bool) error {
	if !tab && !stats {
		return nil
	}
	if tab && stats {
		return fmt.Errorf("--tab and --stats are mutually exclusive")
	}
	allowed, form := historyTabAllowed, "--tab"
	if stats {
		allowed, form = historyStatsAllowed, "--stats"
	}
	bad := ""
	fs.Visit(func(f *flag.Flag) {
		if bad == "" && !allowed[f.Name] {
			bad = f.Name
		}
	})
	if bad != "" {
		return fmt.Errorf("--%s cannot be combined with %s", bad, form)
	}
	return nil
}

// historyAxisNames is histq's axes in their listed order, for the --by usage
// error and the flag's help text.
func historyAxisNames() []string {
	axes := histq.Axes()
	names := make([]string, len(axes))
	for i, a := range axes {
		names[i] = string(a)
	}
	return names
}

// historyAxis resolves the axis a result regroups by: a valid --by wins
// (validateHistoryBy has already rejected a bad one, so the fallback below is
// unreachable from cmdHistory), and a parsed query that names no axis reads
// as AxisNone. It is pure and defensive: Filter always names an axis now, but
// a path that bypasses Filter must not resurrect the zero value "" that
// cmdHistory once read as a regroup axis and printed "no rounds" for.
func historyAxis(parsed histq.Query, by string) histq.Axis {
	if by != "" {
		if a, ok := histq.ParseAxis(by); ok {
			return a
		}
	}
	if parsed.By == "" {
		return histq.AxisNone
	}
	return parsed.By
}

// groupJSON shapes the groups `--json --by` prints: their Rows are blanked
// unless --rows asked for them. A nil list encodes as [], not null.
func groupJSON(groups []histq.GroupRow, withRows bool) []histq.GroupRow {
	if groups == nil {
		groups = []histq.GroupRow{}
	}
	if withRows {
		return groups
	}
	out := make([]histq.GroupRow, len(groups))
	for i, g := range groups {
		g.Rows = nil
		out[i] = g
	}
	return out
}

// cmdHistory prints one line per round across every binding relevo has ever
// recorded, live or archived, newest first
// (docs/specs/2026-09-20-persistence-design.md §5.7).
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	here := fs.Bool("here", false, "filter to the repo the current directory belongs to")
	repo := fs.String("repo", "", "filter to this repo: an origin url or a git common dir")
	feature := fs.String("feature", "", "filter to this --feature label")
	binding := fs.String("binding", "", "filter to this binding name")
	planner := fs.String("planner", "", "filter to this planner session id")
	harness := fs.String("harness", "", "filter to this builder harness")
	provider := fs.String("provider", "", "filter to this builder provider")
	model := fs.String("model", "", "filter to this builder model")
	candidateTok := fs.String("candidate", "", "filter to this candidate name or harness/provider/model token")
	outcome := fs.String("outcome", "", "filter to this round outcome: "+strings.Join(historyOutcomeValues, ", "))
	since := fs.String("since", "", "only rounds started after this: 24h, 7d, or YYYY-MM-DD")
	until := fs.String("until", "", "only rounds started before this: 24h, 7d, or YYYY-MM-DD")
	archived := fs.Bool("archived", false, "archived bindings only")
	live := fs.Bool("live", false, "live bindings only")
	limit := fs.Int("limit", 200, "max rows to print; 0 = all")
	asJSON := fs.Bool("json", false, "machine-readable output: a JSON array of RoundRow")
	query := fs.String("q", "", "a query: harness:agy outcome:halted since:30d cost>1 by:builder")
	by := fs.String("by", "", "regroup the result by one of: "+strings.Join(historyAxisNames(), ", "))
	withRows := fs.Bool("rows", false, "with --json --by, include each group's rows")
	tab := fs.Bool("tab", false, "tokens and cost across bindings, archived ones included")
	stats := fs.Bool("stats", false, "per-candidate scorecard, spend per day, reliability, repos and outcomes (default window 30d; --since all for everything)")
	owner := fs.String("owner", "", "with --tab: the server's owner, a client label or id (all = every owner)")
	state := fs.String("state", "", "with --owner: the serve state directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), historyUsage)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}
	// --owner reads the server's owners, and the tab form is the only one
	// that can (§4.2); --state names that server's root, so it needs --owner.
	if *owner != "" && !*tab {
		fmt.Fprintln(os.Stderr, "relevo history: --owner works with --tab")
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}
	if *state != "" && *owner == "" {
		fmt.Fprintln(os.Stderr, "relevo history: --state only applies with --owner")
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}
	// --tab and --stats are the two old verbs: their output is exactly what
	// `relevo tab` and `relevo stats` printed (P3d §4.6).
	if *tab || *stats {
		if err := validateHistoryTabStats(fs, *tab, *stats); err != nil {
			fmt.Fprintln(os.Stderr, "relevo history: "+err.Error())
			fmt.Fprintln(os.Stderr, historyUsage)
			return exitCodeErr{code: 2}
		}
		if *stats {
			return historyStats(*since, *asJSON)
		}
		by := *by
		if by == "" {
			by = "binding"
		}
		switch by {
		case "binding", "model", "provider", "owner":
		default:
			fmt.Fprintf(os.Stderr, "relevo history: --by %q: want one of binding, model, provider, owner\n", by)
			fmt.Fprintln(os.Stderr, historyUsage)
			return exitCodeErr{code: 2}
		}
		if *owner != "" {
			// `--owner all` is today's `serve tab` with no --owner: every
			// owner, grouped as before (§4.2).
			ownerArg := *owner
			if ownerArg == "all" {
				ownerArg = ""
			}
			return serveTab(ownerArg, *state, *since, by, *asJSON)
		}
		if by == "owner" {
			// Grouping by owner is the server's view (AdminTabEntries sets
			// Owner); a local run has no owner to group by.
			fmt.Fprintln(os.Stderr, "relevo history: --by owner works with --owner")
			fmt.Fprintln(os.Stderr, historyUsage)
			return exitCodeErr{code: 2}
		}
		return historyTab(*since, by, *asJSON)
	}
	if err := validateHistoryFlags(*archived, *live, *outcome); err != nil {
		fmt.Fprintln(os.Stderr, "relevo history: "+err.Error())
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}
	if err := validateHistoryBy(*by); err != nil {
		fmt.Fprintln(os.Stderr, "relevo history: "+err.Error())
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	d, err := openDB(rt.Store.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo history: %v\n", err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()
	rt.DB = d

	opts := relevo.HistoryOptions{
		Repo:      *repo,
		Feature:   *feature,
		Binding:   *binding,
		Planner:   *planner,
		Harness:   *harness,
		Provider:  *provider,
		Model:     *model,
		Candidate: *candidateTok,
		Names:     rt.Candidates,
		Outcome:   *outcome,
		Since:     *since,
		Until:     *until,
		Limit:     *limit,
		Query:     *query,
		By:        *by,
	}
	if *here {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return cerr
		}
		opts.Here = cwd
	}
	switch {
	case *archived:
		t := true
		opts.Archived = &t
	case *live:
		f := false
		opts.Archived = &f
	}

	f, notes, ferr := opts.Filter(context.Background(), rt, time.Now())
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "relevo history: %v\n", ferr)
		var eq histq.ErrQuery
		if errors.As(ferr, &eq) {
			return exitCodeErr{code: 2}
		}
		return exitCodeErr{code: 1}
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	rows, qerr := rt.DB.Query(f)
	if qerr != nil {
		return qerr
	}

	parsed := opts.ParsedQuery()
	rows = parsed.Apply(rows)

	axis := historyAxis(parsed, *by)

	if axis != histq.AxisNone {
		groups := histq.Group(rows, axis, time.Local)
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(groupJSON(groups, *withRows))
		}
		fmt.Print(relevo.FormatGroups(groups, axis, time.Local, rt.Candidates.NameOf))
		return nil
	}

	if *asJSON {
		if rows == nil {
			rows = []db.RoundRow{}
		}
		return json.NewEncoder(os.Stdout).Encode(historyJSONRows(rows, rt.Candidates))
	}
	fmt.Print(relevo.FormatHistory(rows, time.Local, rt.Candidates.NameOf))
	return nil
}

// historyTab is `relevo history --tab`: exactly what `relevo tab` printed
// (P3d §4.6). The body is cmdTab's, moved here because the `tab` verb is gone.
// The server's per-owner form is serveTab, which cmdHistory routes to before
// this.
func historyTab(since, by string, asJSON bool) error {
	now := time.Now().UTC()
	cut, err := relevo.ParseSince(since, now)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	entries, err := relevo.TabEntries(rt, cut, func(msg string) {
		fmt.Fprintf(os.Stderr, "relevo tab: skip %s\n", msg)
	})
	if err != nil {
		return err
	}
	return renderTabReport(entries, by, cut, asJSON)
}

// historyStats is `relevo history --stats`: the stats report computed from
// the inputs relevo.StatsInputs assembles -- relevo.db's rounds, the
// availability history and the active gates (cockpit C2b §4.1). The window is
// --since, which defaults to 30 days; the literal `all` means every recorded
// round. No harness is spawned and no network is reached.
func historyStats(since string, asJSON bool) error {
	now := time.Now().UTC()

	var cut time.Time
	if since != "all" {
		if since == "" {
			since = "30d"
		}
		c, err := relevo.ParseSince(since, now)
		if err != nil {
			return err
		}
		cut = c
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	d, err := openDB(rt.Store.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v: %v\n", relevo.ErrNoDatabase, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()
	rt.DB = d

	in, warnings, err := relevo.StatsInputs(rt, cut)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "relevo: %s\n", w)
	}

	rep := stats.Build(in)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(stats.Render(rep, rt.Candidates.NameOf))
	return nil
}

// renderTabReport is cmdTab's tail: it sums the gathered entries through
// relevo.TabRows and prints the report, as JSON when asJSON is set. The
// server form `relevo history --tab --owner` (serveTab) renders through the
// same tail.
func renderTabReport(entries []relevo.TabEntry, by string, cut time.Time, asJSON bool) error {
	rows, total, err := relevo.TabRows(entries, by, cut)
	if err != nil {
		return err
	}
	rep := relevo.TabReport{By: by, Rows: rows, Total: total}
	if !cut.IsZero() {
		rep.Since = &cut
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(relevo.RenderTab(rep))
	return nil
}
