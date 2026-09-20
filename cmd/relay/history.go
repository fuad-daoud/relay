package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/relay"
)

const historyUsage = `usage: relay history [--here|--repo <url|dir>] [--feature L] [--binding N] [--planner S]
                     [--harness K] [--provider P] [--model M] [--candidate T]
                     [--outcome O] [--since D] [--until D] [--archived|--live]
                     [--limit N] [--json]`

// historyOutcomeValues lists the round.Outcome enum in the order
// docs/specs/2026-09-20-persistence-design.md §3 decision 8 states it, for
// the --outcome usage error.
var historyOutcomeValues = []string{
	db.OutcomeReported, db.OutcomeHalted, db.OutcomeExited,
	db.OutcomeSwitched, db.OutcomeDoneNoReport, db.OutcomeOpen,
}

// validateHistoryFlags checks the two combinations `relay history` rejects
// with a usage error: --archived with --live, and an --outcome outside the
// enum. It is a pure function, factored out so a cmd/relay test can pin the
// rule without executing the subcommand (CI has no herdr to reach).
func validateHistoryFlags(archived, live bool, outcome string) error {
	if archived && live {
		return fmt.Errorf("--archived and --live are mutually exclusive")
	}
	if outcome != "" && !db.ValidOutcome(outcome) {
		return fmt.Errorf("--outcome %q: want one of %s", outcome, strings.Join(historyOutcomeValues, ", "))
	}
	return nil
}

// cmdHistory prints one line per round across every binding relay has ever
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
	candidateTok := fs.String("candidate", "", "filter to this harness/provider/model candidate token")
	outcome := fs.String("outcome", "", "filter to this round outcome: "+strings.Join(historyOutcomeValues, ", "))
	since := fs.String("since", "", "only rounds started after this: 24h, 7d, or YYYY-MM-DD")
	until := fs.String("until", "", "only rounds started before this: 24h, 7d, or YYYY-MM-DD")
	archived := fs.Bool("archived", false, "archived bindings only")
	live := fs.Bool("live", false, "live bindings only")
	limit := fs.Int("limit", 200, "max rows to print; 0 = all")
	asJSON := fs.Bool("json", false, "machine-readable output: a JSON array of RoundRow")
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
	if err := validateHistoryFlags(*archived, *live, *outcome); err != nil {
		fmt.Fprintln(os.Stderr, "relay history: "+err.Error())
		fmt.Fprintln(os.Stderr, historyUsage)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	d, err := openDB(rt.Store.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay history: %v\n", err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()
	rt.DB = d

	opts := relay.HistoryOptions{
		Repo:      *repo,
		Feature:   *feature,
		Binding:   *binding,
		Planner:   *planner,
		Harness:   *harness,
		Provider:  *provider,
		Model:     *model,
		Candidate: *candidateTok,
		Outcome:   *outcome,
		Since:     *since,
		Until:     *until,
		Limit:     *limit,
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

	f, ferr := opts.Filter(context.Background(), rt, time.Now())
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "relay history: %v\n", ferr)
		return exitCodeErr{code: 1}
	}
	rows, qerr := rt.DB.Query(f)
	if qerr != nil {
		return qerr
	}

	if *asJSON {
		if rows == nil {
			rows = []db.RoundRow{}
		}
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	fmt.Print(relay.FormatHistory(rows, time.Local))
	return nil
}
