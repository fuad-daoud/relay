package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/planner"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote/client"
	"github.com/fuad-daoud/relay/internal/store"
)

// bindPreflightTimeout bounds the bind-time preflight. The hot path must not be
// slowed by a hung probe: it reads the filesystem and PATH only.
const bindPreflightTimeout = 2 * time.Second

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
			fmt.Fprintf(w, "%s, %s -- could not establish a usable builder: no checked harness has its binary on PATH.\n", warnPart, failPart)
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
	env := doctor.NewEnv(rt.Store, releaseInputs())

	opencodeConfigured := false
	for _, k := range kinds {
		if k == "opencode" {
			opencodeConfigured = true
		}
	}
	configDir, err := userConfigRoot() // the same userConfigRoot() result newRuntime uses
	if err != nil {
		return err
	}
	pricesPath := filepath.Join(configDir, "relay", "prices.json")

	// One doctor row per configured server (remote-builders spec §5.5):
	// reachable and enrolled, not enrolled (with the line to give the
	// admin), unreachable, or a changed certificate. No servers.json ->
	// no rows.
	hasServers := false
	var extraChecks []doctor.Check
	if servers, serversErr := client.LoadServers(client.ServersPath(configDir)); serversErr == nil && len(servers) > 0 {
		hasServers = true
		_, pubPath := client.KeyPaths(configDir)
		enrollLine := ""
		if raw, rerr := os.ReadFile(pubPath); rerr == nil {
			enrollLine = string(raw)
		}
		extraChecks = serverChecks(relay.ProbeServers(context.Background(), rt, servers, enrollLine))
	}

	// #236: the opencode branch checks opencode's own external_directory
	// allowlist against the state root relay stages plans and reports under.
	// store has no root accessor on rt.Store, so resolve it the way
	// newRuntime did (main.go). A failure here just leaves the check off.
	stateRoot, _ := store.DefaultRoot()

	rep := doctor.Run(context.Background(), env, kinds,
		doctor.WithDefinitions(assembleDefinitions(rt.Candidates, kinds)),
		doctor.WithUsage(pricesPath, opencodeConfigured),
		doctor.WithExtraChecks(extraChecks),
		doctor.WithStateRoot(stateRoot))
	// #370: whether a restart right now would kill anything, read from the
	// cgroup every running local process actually sits in. One call, placed
	// directly after the daemon row.
	rep.Checks = insertRestartRow(rep.Checks, restartCheck(rt))
	if storeErr != nil {
		rep.Checks = insertGlobalCheck(rep.Checks, doctor.Check{
			Name:        "bindings",
			Severity:    doctor.SevWarn,
			Detail:      storeErr.Error(),
			ProbeFailed: true,
		})
	}
	// #335: a remote builder commits as the client, so a repo whose effective
	// user.name/user.email is unset makes `relay add --server` refuse. The row
	// exists only where a server is configured and the cwd is inside a repo; a
	// git failure (no repo, no git) is not established, so it is no row.
	identity := doctor.GitIdentityInput{HasServers: hasServers}
	if rt.Git != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if name, email, identErr := rt.Git.Identity(context.Background(), wd); identErr == nil {
				identity.InRepo, identity.Name, identity.Email = true, name, email
			}
		}
	}
	if c, ok := doctor.GitIdentityCheck(identity); ok {
		rep.Checks = insertGlobalCheck(rep.Checks, c)
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

	if stateRoot, err := store.DefaultRoot(); err == nil {
		serveRoot := filepath.Join(stateRoot, "serve")
		rep.Checks = append(rep.Checks, doctor.ServeChecks(env, serveRoot, time.Now())...)
	}

	// #314: when a scope block asks for cpu pinning, doctor confirms the user
	// manager has cpuset delegated (a silently ignored AllowedCPUs shows no
	// exit code anywhere else) and that the pool names only this host's cores.
	// A block whose scope is off is skipped, the same rule scopeFromPolicy
	// applies.
	var scopeBlocks []doctor.ScopeBlock
	if sc := enabledScope(rt.Policy.Scope); sc != nil {
		scopeBlocks = append(scopeBlocks, scopeBlock("scope.allowed_cpus", sc))
	}
	if rt.Policy.Serve != nil {
		if sc := enabledScope(rt.Policy.Serve.Scope); sc != nil {
			scopeBlocks = append(scopeBlocks, scopeBlock("serve.scope.allowed_cpus", sc))
		}
	}
	rep.Checks = append(rep.Checks, doctor.ScopeChecks(env, scopeBlocks, doctor.UserManagerControllersPath(os.Getuid()), runtime.NumCPU())...)

	rep.Checks = append(rep.Checks, ledgerChecks(relay.Gates(rt))...)
	rep.Checks = append(rep.Checks, policyChecks(relay.PolicyWarnings(rt.Candidates, rt.Policy))...)
	_, st := classify.Resolve(rt.Policy.Classify, configDir, os.Getenv)
	rep.Checks = append(rep.Checks, doctor.ClassifyCheck(st))
	refusals := relay.RoleRefusals(rt.Candidates, rt.Policy, relay.Gates(rt))
	rep.Checks = append(rep.Checks, refusalChecks(refusals)...)
	for _, r := range refusals {
		if r.Role == "builder" {
			rep.BuilderRefusal = r.Text
			break
		}
	}

	rep.Checks = append(rep.Checks, doctor.PlannerChecks(plannerCheckInput(rt, kinds))...)

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

		fix := "wait until " + relay.GateTimeText(g.Until)
		if g.Kind == ledger.RateLimited {
			fix = "relay available " + provider
		}

		checks = append(checks, doctor.Check{
			Group:    group,
			Name:     "ledger",
			Severity: doctor.SevWarn,
			Detail: fmt.Sprintf("%s: %s since %s (%s)",
				g.Token, relay.GateKindText(g.Kind), relay.GateTimeText(g.Since), relay.GateUntilText(g.Until)),
			Fix: fix,
		})
	}
	return checks
}

// serverChecks turns each configured server's probe (relay.ProbeServers)
// into a doctor row (remote-builders spec §5.5): reachable and enrolled is
// ok; not enrolled and unreachable warn (an unreachable probe is
// ProbeFailed -- relay could not establish the fact, not that anything is
// wrong); a changed certificate fails, since the client hard-refuses it.
func serverChecks(probes []relay.ServerProbe) []doctor.Check {
	checks := make([]doctor.Check, 0, len(probes))
	for _, p := range probes {
		c := doctor.Check{Group: "", Name: "servers"}
		switch p.State {
		case "enrolled":
			c.Severity = doctor.SevOK
			c.Detail = fmt.Sprintf("%s: enrolled as %s", p.Name, p.Label)
			if p.QueueAware && p.Builders != nil {
				c.Detail += fmt.Sprintf(", %s", buildersText(p))
			}
			checks = append(checks, c)
			if warning := relay.ServerTierWarning(p); warning != "" {
				checks = append(checks, doctor.Check{
					Group:    "",
					Name:     "servers",
					Severity: doctor.SevWarn,
					Detail:   fmt.Sprintf("%s: %s", p.Name, warning),
					Fix:      "set tier.builder in the server's policy.json",
				})
			} else if !p.TierAware {
				checks = append(checks, doctor.Check{
					Group:       "",
					Name:        "servers",
					Severity:    doctor.SevWarn,
					Detail:      fmt.Sprintf("%s: builder tier unknown (pre-tier server)", p.Name),
					ProbeFailed: true,
				})
			}
			if p.QueueAware && p.Builders != nil && !p.Builders.Scopes {
				checks = append(checks, doctor.Check{
					Group:    "",
					Name:     "servers",
					Severity: doctor.SevWarn,
					Detail:   fmt.Sprintf("scopes unavailable on %s: a daemon restart kills its builders", p.Name),
				})
			}
			continue
		case "not enrolled":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: not enrolled", p.Name)
			c.Fix = "give the admin: " + p.Detail
		case "unreachable":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: unreachable: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		case "cert changed":
			c.Severity = doctor.SevFail
			c.Detail = fmt.Sprintf("%s: certificate changed", p.Name)
			c.Fix = fmt.Sprintf("relay client add-server %s <url> --fingerprint <new>", p.Name)
		case "no key":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		default:
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		}
		checks = append(checks, c)
	}
	return checks
}

// buildersText is a queue-aware, enrolled probe's builder census, the same
// words RenderServers appends to its row (#285): "builders %d/%d, %d
// queued, scopes %s".
func buildersText(p relay.ServerProbe) string {
	scopes := "off"
	switch {
	case p.Builders.Scopes && p.Builders.Slice != "" && p.Builders.Quota != "":
		scopes = fmt.Sprintf("on (%s, %s)", p.Builders.Slice, p.Builders.Quota)
	case p.Builders.Scopes && p.Builders.Quota != "":
		scopes = fmt.Sprintf("on (%s)", p.Builders.Quota)
	case p.Builders.Scopes && p.Builders.Slice != "":
		scopes = fmt.Sprintf("on (%s)", p.Builders.Slice)
	case p.Builders.Scopes:
		scopes = "on"
	}
	return fmt.Sprintf("builders %d/%d, %d queued, scopes %s",
		p.Builders.Running, p.Builders.Cap, p.Builders.Queued, scopes)
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

// stalePlannerAge is how old a planner record's seen_at must be before the
// stale-record note names it (§4.8, row 5).
const stalePlannerAge = 7 * 24 * time.Hour

// plannerCheckInput gathers §4.8's planner-row facts: which planners exist,
// whether this process runs inside Claude Code, and whether the resolved
// planner has a live channel claim. Every read is best-effort -- a fact relay
// cannot establish reads as absent, and the checks say "not checked" rather
// than guessing. Home comes from $HOME so the row reads the same directory
// cmd/relay's TestMain isolated.
func plannerCheckInput(rt relay.Runtime, kinds []string) doctor.PlannerCheckInput {
	in := doctor.PlannerCheckInput{Home: os.Getenv("HOME"), Running: buildVersion()}
	if wd, err := os.Getwd(); err == nil {
		in.Repo = wd
	}
	for _, k := range kinds {
		if k == "claude" {
			in.Claude = true
		}
	}

	records := []planner.Record{}
	if rt.Planners != nil {
		if recs, err := rt.Planners.List(); err == nil {
			records = recs
		}
	}

	live := map[string]bool{}
	if rt.Store != nil {
		if bindings, err := rt.Store.List(); err == nil {
			for _, b := range bindings {
				if b.State != store.StateDone && b.PlannerID != "" {
					live[b.PlannerID] = true
				}
			}
		}
	}

	cutoff := rt.Now().Add(-stalePlannerAge)
	for _, rec := range records {
		if rec.HarnessKind == "claude" {
			in.Claude = true
		}
		if rec.SeenAt.Before(cutoff) && !live[rec.ID] {
			in.Stale = append(in.Stale, rec.Name)
		}
	}

	if ident, ok := planner.Detect(os.Getenv, os.Getppid()); ok && ident.Kind == "claude" {
		in.Detected = true
		in.MCPChild = HasMCPChild(hostChildProcesses(ident.HostPID))
		if rt.Planners != nil {
			rec, _, err := planner.Resolve(rt.Planners, planner.ResolveInput{
				Env:       os.Getenv,
				PPID:      os.Getppid(),
				ProcStart: rt.ProcStart,
				Now:       rt.Now(),
			})
			if err == nil {
				in.Resolved = &rec
				if rt.Channels != nil {
					if c, cerr := rt.Channels.Live(rec.ID, rt.Now()); cerr == nil && c != nil {
						in.ClaimLive = true
					}
				}
			}
		}
	}

	return in
}

// HasMCPChild is doctor.HasMCPChild, re-exported so the pure rule is visible
// at this call site without importing internal/doctor into a test's mind.
func HasMCPChild(children []doctor.ChildProcess) bool { return doctor.HasMCPChild(children) }

// hostChildProcesses reads the child processes of pid: /proc/<pid>/task/*/children
// names them, and each child's /proc/<pid>/cmdline its argv. On a host with no
// /proc (macOS) it falls back to `ps -o pid=,args= --ppid`. A pid that has gone
// or a read that fails yields nil, which reads as "no relay mcp child" -- the
// FAIL #4.8 asks for when a Claude session has no push route.
func hostChildProcesses(pid int) []doctor.ChildProcess {
	if pid <= 0 {
		return nil
	}
	paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	if err != nil || len(paths) == 0 {
		return psChildren(pid)
	}
	seen := make(map[int]bool)
	var out []doctor.ChildProcess
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(raw)) {
			cpid, err := strconv.Atoi(f)
			if err != nil || seen[cpid] {
				continue
			}
			seen[cpid] = true
			if args := processArgs(cpid); len(args) > 0 {
				out = append(out, doctor.ChildProcess{PID: cpid, Args: args})
			}
		}
	}
	if len(out) == 0 {
		return psChildren(pid)
	}
	return out
}

// processArgs reads one process's argv from /proc/<pid>/cmdline, which is
// NUL-separated. A process that has gone yields nil.
func processArgs(pid int) []string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return parts
}

// psChildren is hostChildProcesses' portable fallback: `ps -o pid=,args=
// --ppid <pid>`, which the tree's other process probes already use. An
// unsupported flag or a missing ps reads as no children.
func psChildren(pid int) []doctor.ChildProcess {
	out, err := exec.Command("ps", "-o", "pid=,args=", "--ppid", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return ParsePSChildren(out)
}

// ParsePSChildren parses `ps -o pid=,args=` output: one line per process, the
// pid first, the argv as the rest of the line. Pure, so the parsing is
// testable without a process tree.
func ParsePSChildren(out []byte) []doctor.ChildProcess {
	var procs []doctor.ChildProcess
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		var args []string
		if len(fields) > 1 {
			args = strings.Fields(fields[1])
		}
		procs = append(procs, doctor.ChildProcess{PID: pid, Args: args})
	}
	return procs
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

// enabledScope is a scope block that will actually run: nil for a nil block or
// one whose Enabled is explicitly false, the same rule scopeFromPolicy applies.
func enabledScope(sc *policy.ScopePolicy) *policy.ScopePolicy {
	if sc == nil || (sc.Enabled != nil && !*sc.Enabled) {
		return nil
	}
	return sc
}

// scopeBlock is one doctor.ScopeBlock for a policy scope that sets allowed_cpus
// (#314). MaxCPU is the highest core the pool names, computed with
// policy.ParseCPUList so internal/doctor never imports policy.
func scopeBlock(key string, sc *policy.ScopePolicy) doctor.ScopeBlock {
	b := doctor.ScopeBlock{Key: key, AllowedCPUs: sc.AllowedCPUs}
	if cpus, err := policy.ParseCPUList(sc.AllowedCPUs); err == nil && len(cpus) > 0 {
		b.MaxCPU = cpus[len(cpus)-1]
	}
	return b
}
