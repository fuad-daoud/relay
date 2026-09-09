package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func assembleKinds(aliases *alias.Table, st *store.Store) []string {
	seen := make(map[string]bool)
	if aliases != nil {
		for _, name := range aliases.Names() {
			if spec, err := aliases.Lookup(name); err == nil && spec.Kind != "" {
				seen[spec.Kind] = true
			}
		}
	}
	if st != nil {
		if bindings, err := st.List(); err == nil {
			for _, b := range bindings {
				if b.Builder.Kind != "" {
					seen[b.Builder.Kind] = true
				}
			}
		}
	}
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

func renderReport(w io.Writer, rep doctor.Report) {
	// 1. Global rows
	for _, c := range rep.Checks {
		if c.Group == "" {
			fmt.Fprintf(w, "%-26s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(w, "    fix: %s\n", c.Fix)
			}
		}
	}

	// 2. Kind blocks
	var groups []string
	seen := make(map[string]bool)
	for _, c := range rep.Checks {
		if c.Group != "" && !seen[c.Group] {
			seen[c.Group] = true
			groups = append(groups, c.Group)
		}
	}

	for _, g := range groups {
		fmt.Fprintln(w)
		fmt.Fprintln(w, g)
		for _, c := range rep.Checks {
			if c.Group == g {
				fmt.Fprintf(w, "  %-24s%-9s%s\n", c.Name, c.Severity.String(), c.Detail)
				if c.Fix != "" {
					fmt.Fprintf(w, "    fix: %s\n", c.Fix)
				}
			}
		}
	}

	// 3. Footer
	fmt.Fprintln(w)
	failCount := rep.Failures()
	warnCount := rep.Warnings()

	failPart := fmt.Sprintf("%d failures", failCount)
	if failCount == 1 {
		failPart = "1 failure"
	}
	warnPart := fmt.Sprintf("%d warnings", warnCount)
	if warnCount == 1 {
		warnPart = "1 warning"
	}

	if failCount == 0 {
		fmt.Fprintf(w, "%s, %s -- relay can run.\n", warnPart, failPart)
	} else {
		fixPart := "Fix the failures above."
		if failCount == 1 {
			fixPart = "Fix the failure above."
		}
		fmt.Fprintf(w, "%s, %s -- no usable builder. %s\n", failPart, warnPart, fixPart)
	}
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("relay doctor", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	kinds := assembleKinds(rt.Aliases, rt.Store)
	var herdrClient doctor.HerdrClient
	if hc, ok := rt.Herdr.(doctor.HerdrClient); ok {
		herdrClient = hc
	} else {
		herdrClient = herdr.NewClient("herdr", 30*time.Second)
	}
	env := doctor.NewEnv(herdrClient, rt.Store)
	rep := doctor.Run(context.Background(), env, kinds)

	renderReport(os.Stdout, rep)

	if rep.Failures() > 0 {
		return exitCodeErr{code: 1}
	}
	return nil
}

func bindWarningLines(rep doctor.Report, adopted bool) []string {
	var warnings []string
	for _, c := range rep.Checks {
		if c.Group == "" || c.Severity == doctor.SevOK {
			continue
		}
		if adopted && c.Name != "integration" {
			continue
		}
		// Probe errors are swallowed
		if strings.Contains(c.Detail, "unavailable") || strings.Contains(c.Detail, "probe error") {
			continue
		}

		msg := fmt.Sprintf("%s %s %s", c.Group, c.Name, c.Detail)
		if c.Fix != "" {
			msg += fmt.Sprintf(". Fix: %s", c.Fix)
		}
		warnings = append(warnings, msg)
	}

	if len(warnings) == 0 {
		return nil
	}

	var lines []string
	for _, w := range warnings {
		wrapped := wrapText(w, 70)
		for _, l := range wrapped {
			lines = append(lines, "relay: "+l)
		}
	}
	lines = append(lines, "relay: run `relay doctor` for the full check")
	return lines
}

func wrapText(text string, maxLen int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	curr := words[0]
	for _, w := range words[1:] {
		if len(curr)+1+len(w) > maxLen {
			lines = append(lines, curr)
			curr = "  " + w
		} else {
			curr += " " + w
		}
	}
	lines = append(lines, curr)
	return lines
}
