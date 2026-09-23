package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// cmdStats reports rounds, outcomes, switches, gate results and consults
// across bindings, archived ones included, from the round logs relevo already
// writes, plus the provider blocks the last 30 days of availability history
// records (#322 stage 1). It reads the same gather `relevo tab` does and
// nothing leaves the machine.
func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	since := fs.String("since", "", "only rounds started after this: 24h, 7d, or YYYY-MM-DD (default: all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo stats [--since 7d] [--json]")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("usage: relevo stats [--since 7d] [--json]")
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
		fmt.Fprintf(os.Stderr, "relevo stats: skip %s\n", msg)
	})
	if err != nil {
		return err
	}
	hist := loadHistory(rt)
	rep := relevo.BuildStats(entries, hist, cut, rt.Now().UTC())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(relevo.RenderStats(rep))
	return nil
}
