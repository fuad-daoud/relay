package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

const showUsage = `usage: relay show <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json]`

// showSectionFlags counts how many section flags are set and resolves the
// one section they name, defaulting to plan when none is given. It is a
// pure function so a cmd/relay test can pin "more than one is a usage
// error" without executing the subcommand.
func showSectionFlags(plan, report, diff, drift, log, transcript bool) (relay.ShowSection, error) {
	set := map[relay.ShowSection]bool{
		relay.ShowPlan:       plan,
		relay.ShowReport:     report,
		relay.ShowDiff:       diff,
		relay.ShowDrift:      drift,
		relay.ShowLog:        log,
		relay.ShowTranscript: transcript,
	}
	var chosen relay.ShowSection
	n := 0
	for section, on := range set {
		if on {
			n++
			chosen = section
		}
	}
	switch n {
	case 0:
		return relay.ShowPlan, nil
	case 1:
		return chosen, nil
	default:
		return "", fmt.Errorf("only one of --plan, --report, --diff, --drift, --log, --transcript may be given")
	}
}

// cmdShow prints one round's plan, report, diff, drift, log or transcript,
// read from a live binding's files or, for anything not live, from the
// database (docs/specs/2026-09-20-persistence-design.md §5.7).
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	round := fs.Int("round", 0, "the round to read; 0 = the newest completed round")
	plan := fs.Bool("plan", false, "show the plan (default)")
	report := fs.Bool("report", false, "show the report")
	diff := fs.Bool("diff", false, "show the round's captured diff")
	drift := fs.Bool("drift", false, "show the round's drift patch")
	logSection := fs.Bool("log", false, "show the round's log entries")
	transcript := fs.Bool("transcript", false, "show the round's builder transcript")
	asJSON := fs.Bool("json", false, "machine-readable output: the ShowResult, Events included for --log")
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

	section, serr := showSectionFlags(*plan, *report, *diff, *drift, *logSection, *transcript)
	if serr != nil {
		fmt.Fprintln(os.Stderr, "relay show: "+serr.Error())
		fmt.Fprintln(os.Stderr, showUsage)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// A live binding needs no database at all; only open one -- and only
	// fail on it -- once we know the binding is not live
	// (docs/specs/2026-09-20-persistence-design.md §4: "show on a live
	// binding still works ... and on a non-live one exits 1 with the
	// error").
	if _, loadErr := rt.Store.Load(name); loadErr != nil {
		if !errors.Is(loadErr, store.ErrNotFound) {
			return loadErr
		}
		d, dbErr := openDB(rt.Store.DBPath())
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "relay show: %v\n", dbErr)
			return exitCodeErr{code: 1}
		}
		defer d.Close()
		rt.DB = d
	}

	opts := relay.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON}
	res, err := relay.Show(context.Background(), rt, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay show: %v\n", err)
		return exitCodeErr{code: 1}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	header := fmt.Sprintf("%s round %d of %d · %s", res.Name, res.Round, res.Rounds, res.Section)
	if res.Archived {
		header += " · archived " + res.ArchivedAt.Format("2006-01-02")
	}
	fmt.Fprintln(os.Stderr, header)

	if res.Missing {
		fmt.Printf("no %s for round %d\n", res.Section, res.Round)
		return nil
	}

	if res.Section == relay.ShowLog {
		for _, e := range res.Events {
			fmt.Println(relay.LogLine(e))
		}
		return nil
	}

	text := res.Text
	if text != "" && text[len(text)-1] != '\n' {
		text += "\n"
	}
	fmt.Print(text)
	return nil
}
