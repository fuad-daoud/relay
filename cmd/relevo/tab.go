package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// cmdTab sums recorded usage across bindings, archived ones included
// (#142). It is freeze exception #2 under #114; see the surfaces spec.
func cmdTab(args []string) error {
	fs := flag.NewFlagSet("tab", flag.ContinueOnError)
	since := fs.String("since", "", "only rounds closed after this: 24h, 7d, or YYYY-MM-DD (default: all)")
	by := fs.String("by", "binding", "group rows by binding, model or provider")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo tab [--since 7d] [--by binding|model|provider] [--json]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("usage: relevo tab [--since 7d] [--by binding|model|provider] [--json]")
	}
	// --by owner is the server-side grouping (`relevo serve tab --by owner`):
	// a client's entries have no owner to group by (#216).
	if *by == "owner" {
		fmt.Fprintln(os.Stderr, `"owner": --by owner is for relevo serve tab`)
		return exitCodeErr{code: 1}
	}
	now := time.Now().UTC()
	cut, err := relevo.ParseSince(*since, now)
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
	return renderTabReport(entries, *by, cut, *asJSON)
}

// renderTabReport is cmdTab's tail: it sums the gathered entries through
// relevo.TabRows and prints the report, as JSON when asJSON is set. The server
// verb `relevo serve tab` renders through the same tail.
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
