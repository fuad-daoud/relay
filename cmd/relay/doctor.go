package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/store"
)

// bindPreflightTimeout bounds the bind-time preflight. The hot path must not be
// slowed by a hung herdr: the herdr client's own per-call timeout is 30s, and
// two calls would add a minute to `relay bind`.
const bindPreflightTimeout = 2 * time.Second

// Compile-time proof that the concrete herdr client satisfies the interface
// doctor needs, so the assertion in newDoctorEnv can never panic at runtime.
var _ doctor.HerdrClient = (*herdr.Client)(nil)

// assembleKinds is the scope: every kind named by a configured candidate,
// plus every existing binding's builder kind. storeErr is returned rather than
// aborting -- a diagnostic that refuses to diagnose because one of its own
// inputs is unreadable is worse than one that reports the gap, so the caller
// renders it as a row and checks the kinds it did find.
func assembleKinds(set *candidate.Set, st *store.Store) (kinds []string, storeErr error) {
	seen := make(map[string]bool)
	if set != nil {
		for _, ref := range set.Refs() {
			// Refs() is the set's own canonical keys, so ParseRef cannot fail.
			parsed, _ := candidate.ParseRef(ref)
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			if c.Harness != "" {
				seen[c.Harness] = true
			}
		}
	}
	if st != nil {
		bindings, err := st.List()
		if err != nil {
			storeErr = fmt.Errorf("could not list bindings: %w", err)
		}
		for _, b := range bindings {
			if b.Builder.Kind != "" {
				seen[b.Builder.Kind] = true
			}
		}
	}
	kinds = make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds, storeErr
}

// builderDefinitions is what a builder needs installed on its harness:
// the plan-executor and the researcher it dispatches to (#166 §3).
func builderDefinitions() []string {
	spec, _ := harness.RoleByName("builder")
	return append([]string(nil), spec.Definitions...)
}

// assembleDefinitions is doctor's per-kind role scope: the definitions
// some candidate on that harness would load, given its roles. A kind in
// kinds that no candidate names reached doctor through a binding, and a
// binding is always a builder.
func assembleDefinitions(set *candidate.Set, kinds []string) map[string][]string {
	seen := make(map[string]map[string]bool)
	add := func(kind string, defs []string) {
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		for _, d := range defs {
			seen[kind][d] = true
		}
	}
	if set != nil {
		for _, ref := range set.Refs() {
			parsed, _ := candidate.ParseRef(ref)
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			for _, role := range c.Roles {
				if spec, ok := harness.RoleByName(role); ok {
					add(c.Harness, spec.Definitions)
				}
			}
		}
	}
	for _, kind := range kinds {
		if seen[kind] == nil {
			add(kind, builderDefinitions())
		}
	}
	out := make(map[string][]string, len(seen))
	for kind, defs := range seen {
		list := make([]string, 0, len(defs))
		for d := range defs {
			list = append(list, d)
		}
		sort.Strings(list)
		out[kind] = list
	}
	return out
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
		if rep.NoCandidates {
			fmt.Fprintf(w, "%s, %s -- no candidates configured; write ~/.config/relay/candidates.json first.\n", warnPart, failPart)
		} else if !rep.UsableBuilder {
			// Say why. Every row can be `ok` and still leave no usable builder --
			// a machine whose only alias names a kind relay was not taught reads
			// as entirely healthy, so a bare verdict would point at nothing.
			fmt.Fprintf(w, "%s, %s -- could not establish a usable builder: no checked harness has both its binary on PATH and its integration installed.\n", warnPart, failPart)
		} else if rep.BuilderRefusal != "" {
			fmt.Fprintf(w, "%s, %s -- relay cannot pick a builder: %s.\n", warnPart, failPart, rep.BuilderRefusal)
		} else {
			fmt.Fprintf(w, "%s, %s -- relay can run.\n", warnPart, failPart)
		}
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

	kinds, storeErr := assembleKinds(rt.Candidates, rt.Store)
	hc, ok := rt.Herdr.(doctor.HerdrClient)
	if !ok {
		return fmt.Errorf("herdr client does not support the probes doctor needs")
	}
	env := doctor.NewEnv(hc, rt.Store)
	rep := doctor.Run(context.Background(), env, kinds, doctor.WithDefinitions(assembleDefinitions(rt.Candidates, kinds)))
	if storeErr != nil {
		rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
			Name:        "bindings",
			Severity:    doctor.SevWarn,
			Detail:      storeErr.Error(),
			ProbeFailed: true,
		})
	}
	if rt.Candidates.Len() == 0 {
		rep.NoCandidates = true
		rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
			Name:     "candidates",
			Severity: doctor.SevWarn,
			Detail:   "none configured",
			Fix:      `write ~/.config/relay/candidates.json, e.g. [{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`,
		})
	}

	rep.Checks = append(rep.Checks, ledgerChecks(relay.Gates(rt))...)
	rep.Checks = append(rep.Checks, policyChecks(relay.PolicyWarnings(rt.Candidates, rt.Policy))...)
	refusals := relay.RoleRefusals(rt.Candidates, rt.Policy, relay.Gates(rt))
	rep.Checks = append(rep.Checks, refusalChecks(refusals)...)
	for _, r := range refusals {
		if r.Role == "builder" {
			rep.BuilderRefusal = r.Text
			break
		}
	}

	renderReport(os.Stdout, rep)

	if rep.Failures() > 0 {
		return exitCodeErr{code: 1}
	}
	return nil
}

// ledgerChecks turns live gates into doctor rows under the candidate's
// harness. They warn, never fail: a gated provider is a fact about right
// now, not a broken install, and must not change doctor's exit code.
func ledgerChecks(gates []ledger.Gate) []doctor.Check {
	checks := make([]doctor.Check, 0, len(gates))
	for _, g := range gates {
		ref, err := candidate.ParseRef(g.Token)
		group := ""
		provider := ""
		if err == nil {
			group = ref.Harness
			provider = ref.Provider
		}

		fix := "wait until " + g.Until.Local().Format("15:04")
		if g.Kind == ledger.RateLimited {
			fix = "relay available " + provider
		}

		checks = append(checks, doctor.Check{
			Group:    group,
			Name:     "ledger",
			Severity: doctor.SevWarn,
			Detail: fmt.Sprintf("%s: %s since %s (%s)",
				g.Token, relay.GateKindText(g.Kind), g.Since.Local().Format("15:04"), relay.GateUntilText(g.Until)),
			Fix: fix,
		})
	}
	return checks
}

// policyChecks turns policy/candidates inconsistencies into doctor rows.
// Warnings, not failures: an unlisted candidate is a degraded order, not
// a broken machine (spec §4.8).
func policyChecks(warnings []relay.PolicyWarning) []doctor.Check {
	checks := make([]doctor.Check, 0, len(warnings))
	for _, w := range warnings {
		checks = append(checks, doctor.Check{
			Group:    "",
			Name:     "policy",
			Severity: doctor.SevWarn,
			Detail:   w.Text,
			Fix:      "edit ~/.config/relay/policy.json",
		})
	}
	return checks
}

// refusalChecks turns the roles an omitted candidate would be refused for
// into doctor rows. Warnings, not failures: the machine is fine, the
// configuration is not (#165). The fix is a literal policy.json built
// from the tokens that serve the role, so it can be pasted as is.
func refusalChecks(refusals []relay.RoleRefusal) []doctor.Check {
	checks := make([]doctor.Check, 0, len(refusals))
	for _, r := range refusals {
		detail := r.Text + " -- ask --role " + r.Role + " without --candidate would refuse"
		if r.Role == "builder" {
			detail = r.Text + " -- add/bind without --builder would refuse"
		}
		fix := "write ~/.config/relay/policy.json, e.g. " + policyExample(r.Role, r.Serving)
		if !r.NoOrder {
			provider := "<provider>"
			if len(r.Gated) > 0 {
				provider = r.Gated[0]
			}
			fix = "relay available " + provider
		}
		checks = append(checks, doctor.Check{
			Group:    "",
			Name:     "policy",
			Severity: doctor.SevWarn,
			Detail:   detail,
			Fix:      fix,
		})
	}
	return checks
}

func policyExample(role string, serving []string) string {
	// json.Marshal cannot fail on a map of string slices.
	b, _ := json.Marshal(map[string]map[string][]string{
		"order": {role: serving},
	})
	return string(b)
}

// insertGlobalCheck puts c after the last global row, so render order stays
// "globals first, then one block per kind".
func insertGlobalCheck(checks []doctor.Check, c doctor.Check) []doctor.Check {
	last := 0
	for i, existing := range checks {
		if existing.Group == "" {
			last = i + 1
		}
	}
	out := make([]doctor.Check, 0, len(checks)+1)
	out = append(out, checks[:last]...)
	out = append(out, c)
	return append(out, checks[last:]...)
}

// bindPreflight runs the bind-time preflight for one kind and renders its
// warning lines. The timeout lives here, not at the call site, so it cannot be
// dropped by accident; adopted is passed through to doctor.Run, which owns what
// an adopted pane is and is not checked for.
func bindPreflight(ctx context.Context, env doctor.Env, kind string, adopted bool) []string {
	ctx, cancel := context.WithTimeout(ctx, bindPreflightTimeout)
	defer cancel()
	return bindWarningLines(doctor.Run(ctx, env, []string{kind}, doctor.WithAdopted(adopted), doctor.WithDefinitions(map[string][]string{kind: builderDefinitions()})))
}

func bindWarningLines(rep doctor.Report) []string {
	var warnings []string
	for _, c := range rep.Checks {
		if c.Severity == doctor.SevOK {
			continue
		}
		// A row relay could not establish is not actionable, so it stays off the
		// hot path -- unless it is a SevFail, which means relay cannot run at
		// all and the user needs to hear it even when the cause was a bad probe.
		if c.ProbeFailed && c.Severity != doctor.SevFail {
			continue
		}

		var msg string
		if c.Group == "" {
			msg = fmt.Sprintf("%s %s", c.Name, c.Detail)
		} else {
			msg = fmt.Sprintf("%s %s %s", c.Group, c.Name, c.Detail)
		}
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
