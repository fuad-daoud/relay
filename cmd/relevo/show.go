package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

const showUsage = `usage: relevo show <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript|--gate|--findings ID] [--json]
       relevo show <name> --diff|--drift [--stat] [--anchors]
       relevo show <name> --log [--follow] [--after N]
       relevo show <name> --owner <label|id> [--log] [--state DIR]`

// flagGiven reports whether the named flag was set on the command line. It is
// how `--after 0` is told from the flag's default 0.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// showSectionFlags counts how many section flags are set and resolves the
// one section they name, defaulting to plan when none is given. It is a
// pure function so a cmd/relevo test can pin "more than one is a usage
// error" without executing the subcommand. findingsID is `--findings`'s
// value: a non-empty id names the findings section.
func showSectionFlags(plan, report, diff, drift, log, transcript, gate bool, findingsID string) (relevo.ShowSection, error) {
	sections := []struct {
		on      bool
		section relevo.ShowSection
	}{
		{plan, relevo.ShowPlan},
		{report, relevo.ShowReport},
		{diff, relevo.ShowDiff},
		{drift, relevo.ShowDrift},
		{log, relevo.ShowLog},
		{transcript, relevo.ShowTranscript},
		{gate, relevo.ShowGate},
		{findingsID != "", relevo.ShowFindings},
	}
	var chosen relevo.ShowSection
	n := 0
	for _, s := range sections {
		if s.on {
			n++
			chosen = s.section
		}
	}
	switch n {
	case 0:
		return relevo.ShowPlan, nil
	case 1:
		return chosen, nil
	default:
		return "", fmt.Errorf("only one of --plan, --report, --diff, --drift, --log, --transcript, --gate, --findings may be given")
	}
}

// cmdShow prints one round's plan, report, diff, drift, log or transcript,
// read from a live binding's files or, for anything not live, from the
// database (docs/specs/2026-09-20-persistence-design.md §5.7). Its --diff,
// --drift and whole-log forms are today's diff and log verbs, byte for byte
// (§4.2).
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	round := fs.Int("round", 0, "the round to read; 0 = the newest completed round")
	plan := fs.Bool("plan", false, "show the plan (default)")
	report := fs.Bool("report", false, "show the report")
	diff := fs.Bool("diff", false, "show the round's captured diff")
	drift := fs.Bool("drift", false, "show the round's drift patch")
	logSection := fs.Bool("log", false, "show the round's log entries")
	transcript := fs.Bool("transcript", false, "show the round's builder transcript")
	gateSection := fs.Bool("gate", false, "show the round's gate log")
	findings := fs.String("findings", "", "show a consult's findings: --findings <id>")
	stat := fs.Bool("stat", false, "with --diff/--drift: print the summary line instead of the patch body")
	anchors := fs.Bool("anchors", false, "with --diff/--drift: prefix each hunk and line with its path:line")
	follow := fs.Bool("follow", false, "with --log: keep printing new entries until the binding is DONE or removed")
	after := fs.Int("after", 0, "with --log: show only entries with a Seq greater than this (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output: the ShowResult, Events included for --log")
	owner := fs.String("owner", "", "on the server host: read this owner's binding, a client label or id")
	state := fs.String("state", "", "with --owner: the serve state directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), showUsage)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}
	name := fs.Args()[0]

	// --state names the serve root, so it means nothing without an owner to
	// read there (§4.1).
	if *state != "" && *owner == "" {
		fmt.Fprintln(os.Stderr, "relevo show: --state only applies with --owner")
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}

	section, serr := showSectionFlags(*plan, *report, *diff, *drift, *logSection, *transcript, *gateSection, *findings)
	if serr != nil {
		// An --owner invocation is the moved serve show body, so its section
		// conflict keeps that route's prefix and usage line (§4.1).
		if *owner != "" {
			fmt.Fprintln(os.Stderr, "relevo serve show: "+serr.Error())
			fmt.Fprintln(os.Stderr, serveShowUsage)
			return exitCodeErr{code: 2}
		}
		fmt.Fprintln(os.Stderr, "relevo show: "+serr.Error())
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}

	// The absorbed flags are valid only with the section they came from
	// (§4.2): --stat/--anchors are diff's, --follow/--after are log's.
	if *stat || *anchors {
		if section != relevo.ShowDiff && section != relevo.ShowDrift {
			bad := "--stat"
			if *anchors {
				bad = "--anchors"
			}
			fmt.Fprintf(os.Stderr, "relevo show: %s requires --diff or --drift\n", bad)
			fmt.Fprintln(os.Stderr, showUsage)
			return exitCodeErr{code: 2}
		}
	}
	if *follow || flagGiven(fs, "after") {
		if section != relevo.ShowLog {
			bad := "--follow"
			if !*follow {
				bad = "--after"
			}
			fmt.Fprintf(os.Stderr, "relevo show: %s requires --log\n", bad)
			fmt.Fprintln(os.Stderr, showUsage)
			return exitCodeErr{code: 2}
		}
	}
	if *after < 0 {
		fmt.Fprintln(os.Stderr, "relevo: --after must be >= 0")
		return exitCodeErr{code: 2}
	}

	if *owner != "" {
		// --log without --round is the whole-log read the removed `serve
		// log` did; every other form is the removed `serve show` (§4.1).
		if section == relevo.ShowLog && *round == 0 {
			return serveLog(*owner, *state, name, *round, *after, *asJSON, *follow)
		}
		return serveShow(*owner, *state, name, *round, section, *asJSON)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	switch {
	case section == relevo.ShowDiff, section == relevo.ShowDrift:
		// Byte-identical to the removed diff verb, including its default
		// round and its #143 .viewed stamp.
		return printDiff(rt, name, *round, *stat, section == relevo.ShowDrift, *anchors)
	case section == relevo.ShowLog && (*round == 0 || *follow):
		// The whole log is the removed log verb, byte for byte, --json's
		// NDJSON included.
		return printLog(rt, name, *round, *after, *asJSON, *follow, true)
	}

	opts := relevo.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON, FindingsID: *findings}
	return printShow(rt, opts, true, true, "")
}

// printShow is cmdShow's body after section resolution, moved verbatim.
// allowDB=false means a non-live binding returns store.ErrNotFound instead
// of opening (and so creating) the database: `relevo serve show` reads live
// bindings only. markViewed guards the #143 .viewed stamp the same way
// printLog's does. headerPrefix is prepended to the stderr header, which is
// how `relevo serve show` names the owner it read from.
func printShow(rt relevo.Runtime, opts relevo.ShowOptions, markViewed, allowDB bool, headerPrefix string) error {
	name := opts.Name

	// A live binding needs no database at all; only open one -- and only
	// fail on it -- once we know the binding is not live
	// (docs/specs/2026-09-20-persistence-design.md §4: "show on a live
	// binding still works ... and on a non-live one exits 1 with the
	// error").
	live := false
	if _, loadErr := rt.Store.Load(name); loadErr != nil {
		if !errors.Is(loadErr, store.ErrNotFound) {
			return loadErr
		}
		if !allowDB {
			return fmt.Errorf("%s: %w", name, store.ErrNotFound)
		}
		d, dbErr := openDB(rt.Store.DBPath())
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "relevo show: %v\n", dbErr)
			return exitCodeErr{code: 1}
		}
		defer d.Close()
		rt.DB = d
	} else {
		live = true
	}
	// #143: a successful print is what "viewed" means, for a live binding
	// only -- there is no .viewed sidecar for a database-only (archived)
	// binding to stamp. Best-effort: never fails the read.
	stampViewed := func() {
		if markViewed && live {
			_ = rt.Store.MarkViewed(name, time.Now())
		}
	}

	res, err := relevo.Show(context.Background(), rt, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo show: %v\n", err)
		return exitCodeErr{code: 1}
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
		stampViewed()
		return nil
	}

	header := fmt.Sprintf("%s%s round %d of %d · %s", headerPrefix, res.Name, res.Round, res.Rounds, res.Section)
	if res.Archived {
		header += " · archived " + res.ArchivedAt.Format("2006-01-02")
	}
	fmt.Fprintln(os.Stderr, header)

	if res.Missing {
		fmt.Printf("no %s for round %d\n", res.Section, res.Round)
		stampViewed()
		return nil
	}

	if res.Section == relevo.ShowLog {
		for _, e := range res.Events {
			fmt.Println(relevo.LogLine(e))
		}
		stampViewed()
		return nil
	}

	text := res.Text
	if text != "" && text[len(text)-1] != '\n' {
		text += "\n"
	}
	fmt.Print(text)
	stampViewed()
	return nil
}
