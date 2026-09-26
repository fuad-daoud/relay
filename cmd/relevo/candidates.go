package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// noteConsultRolesTooLong prints, after a successful bind/add, the one
// advisory line naming configured consult roles the binding's name is too long
// for -- so a later `relevo ask` failing on the derived name is not a surprise
// a day later. It is a note, not an error: a binding that can build is still
// useful, and refusing would let the alias table dictate binding names.
func noteConsultRolesTooLong(reg *roles.Registry, name string) {
	roles := relevo.ConsultRolesTooLongFor(reg, name)
	if len(roles) == 0 {
		return
	}
	// The tightest limit is set by the longest role: relevo ask needs the
	// binding name at most store.MaxAgentNameLen - 10 - len(role) characters.
	longest := roles[0]
	for _, r := range roles[1:] {
		if len(r) > len(longest) {
			longest = r
		}
	}
	fmt.Printf("note: %s is too long for the %s consult role(s); relevo ask needs a binding name of at most %d characters for %s\n",
		name, strings.Join(roles, ", "), store.MaxAgentNameLen-10-len(longest), longest)
}

// notePick prints why relevo chose the candidate it spawned. Silent for
// an explicit token (the planner already knows) and for adoption
// (nothing was chosen); the gated note, if any, is printed separately.
// A1 §4.4: the line names the candidate by its short name.
func notePick(rt relevo.Runtime, role string, res relevo.Resolution) {
	if res.How == "" || res.How == relevo.HowExplicit {
		return
	}
	fmt.Fprintln(os.Stderr, relevo.PickText(role, res, rt.Candidates))
}

// roleOrBuilder is the role name a flag value means: "builder" for "", else
// the value. The CLI's --actor and the registry both spell the default builder
// as "".
func roleOrBuilder(r string) string {
	if r == "" {
		return "builder"
	}
	return r
}

// builderWhere is how the bound/added lines name the builder's place: a local
// builder is always headless (a process relevo runs itself, #99, #303).
func builderWhere(ep store.Endpoint) string {
	if ep.Headless() {
		return "headless"
	}
	return ep.PaneID
}

func cmdCandidates(args []string) error {
	fs := flag.NewFlagSet("relevo config", flag.ContinueOnError)
	probe := fs.Bool("probe", false, "run each candidate once with a one-line prompt from this machine and record its time to first output")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*probe && len(fs.Args()) > 0 {
		return fmt.Errorf("usage: relevo config --probe [token...]")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *probe {
		host, _ := os.Hostname()
		tokens := fs.Args()
		n := len(tokens)
		if n == 0 {
			n = rt.Candidates.Len()
		}
		fmt.Fprintf(os.Stderr, "probing %d candidate(s) from %s, one at a time\n", n, host)

		// The column is sized from the names FormatProbe prints, not from
		// argv: Probe resolves each argument and FormatProbe prints the
		// resolved candidate's short name (A1 §4.4, round 3 F2).
		var names []string
		if len(tokens) > 0 {
			for _, tok := range tokens {
				c, err := rt.Candidates.Resolve(tok)
				if err != nil {
					names = append(names, tok)
					continue
				}
				names = append(names, rt.Candidates.NameOf(c.Ref().String()))
			}
		} else {
			for _, ref := range rt.Candidates.Refs() {
				names = append(names, rt.Candidates.NameOf(ref))
			}
		}
		width := availability.ProbeNameWidth(names)

		_, err := availability.Probe(context.Background(), relevo.AvailabilityDeps(rt), lineExec{}, tokens, host, func(r availability.ProbeResult) {
			fmt.Println(availability.FormatProbe(r, width))
		})
		return err
	}

	fmt.Print(formatCandidates(rt))
	return nil
}

// formatCandidates renders the candidate table cmdCandidates' non-probe form
// prints -- the candidates block `relevo config` shows.
func formatCandidates(rt relevo.Runtime) string {
	h := availability.LatencyHistory{}
	if rt.Latency != nil {
		loaded, err := availability.LoadLatency(rt.Latency, legacyGatesPath(rt.GatesDir, "latency.json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo: could not read latency: %v\n", err)
		} else {
			h = loaded
		}
	}
	h = h.Prune(rt.Now())

	lat := make(map[string]availability.Summary)
	for _, ref := range rt.Candidates.Refs() {
		lat[ref] = h.Summary(ref)
	}

	return view.FormatCandidatesLatencyFor(rt.RoleRegistry(), rt.Candidates, availability.Gates(relevo.AvailabilityDeps(rt)), lat)
}

// legacyGatesPath is <dir>/<name> for the pre-kv gate documents, moved to
// internal/relevo (cockpit C2b §4.1) so the stats input assembly shares it.
func legacyGatesPath(dir, name string) string {
	return availability.LegacyGatesPath(dir, name)
}

// loadHistory reads the availability history for display, treating an
// unreadable record as empty after one stderr line -- the same rule Gates
// applies to the ledger. The body moved to relevo.LoadHistory (cockpit C2b
// §4.1); this keeps the stderr line for its other callers.
func loadHistory(rt relevo.Runtime) availability.History {
	h, err := availability.LoadHistory(relevo.AvailabilityDeps(rt))
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read history: %v\n", err)
		return availability.History{}
	}
	return h
}

// formatPolicy renders the per-role pick explanation the `pick` block of
// `relevo config` shows.
func formatPolicy(rt relevo.Runtime) string {
	return relevo.FormatPolicyFor(rt.RoleRegistry(), rt.Candidates, rt.Policy, availability.Gates(relevo.AvailabilityDeps(rt)), loadHistory(rt), rt.Now(), time.Local)
}
