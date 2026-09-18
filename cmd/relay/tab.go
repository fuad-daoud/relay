package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// cmdTab sums recorded usage across bindings, archived ones included
// (#142). It is freeze exception #2 under #114; see the surfaces spec.
func cmdTab(args []string) error {
	fs := flag.NewFlagSet("tab", flag.ContinueOnError)
	since := fs.String("since", "", "only rounds closed after this: 24h, 7d, or YYYY-MM-DD (default: all)")
	by := fs.String("by", "binding", "group rows by binding, model or provider")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relay tab [--since 7d] [--by binding|model|provider] [--json]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("usage: relay tab [--since 7d] [--by binding|model|provider] [--json]")
	}
	now := time.Now().UTC()
	cut, err := relay.ParseSince(*since, now)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	var entries []relay.TabEntry
	live, err := rt.Store.List()
	if err != nil {
		return err
	}
	for _, b := range live {
		log, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", b.Name, err)
		}
		for _, e := range log {
			entries = append(entries, relay.TabEntry{Binding: b.Name, Entry: e})
		}
	}
	archives, err := rt.Store.ListArchives()
	if err != nil {
		return err
	}
	for _, a := range archives {
		if !cut.IsZero() && a.At.Before(cut) {
			continue // every entry in it predates the archive itself
		}
		log, err := rt.Store.ReadArchivedLog(a.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relay tab: skip %s: %v\n", a.Path, err)
			continue
		}
		for _, e := range log {
			entries = append(entries, relay.TabEntry{Binding: a.Name, Entry: e})
		}
	}

	rows, total, err := relay.TabRows(entries, *by, cut)
	if err != nil {
		return err
	}
	rep := relay.TabReport{By: *by, Rows: rows, Total: total}
	if !cut.IsZero() {
		rep.Since = &cut
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(relay.RenderTab(rep))
	return nil
}
