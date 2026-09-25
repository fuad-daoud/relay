package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/usage"
)

type Severity int

const (
	SevOK   Severity = iota // nothing to do; also used for "not checked"
	SevWarn                 // wrong, but relevo can still run
	SevFail                 // relevo cannot run
	// SevInfo is an observation the human may want to act on: neither a
	// warning nor a failure, and counted as neither (§4.8 row 5).
	SevInfo
)

func (s Severity) String() string {
	switch s {
	case SevOK:
		return "ok"
	case SevWarn:
		return "warn"
	case SevFail:
		return "FAIL"
	case SevInfo:
		return "info"
	default:
		return "unknown"
	}
}

// Check is one probe's result. Fix is a literal command the user can paste,
// never prose, and is empty when Severity is SevOK.
type Check struct {
	Group    string // "" for global rows, else the harness kind
	Name     string // "daemon", "binary", "plugin", "plan-executor"
	Severity Severity
	Detail   string // what was actually found
	Fix      string // the command that fixes it
	// ProbeFailed marks a row where relevo could not establish the fact at all
	// (the probe errored, timed out, or returned nothing for this target) as
	// opposed to establishing that something is wrong. The bind-time preflight
	// skips these, because there is nothing the user can act on.
	ProbeFailed bool
	// Unsafe is how many running processes sit outside their own scope, set
	// only on the restart row (#370 §4.8). It exists so `relevo status` can
	// print the count without parsing Detail.
	Unsafe int
}

// Report is every check, in render order, plus the derived verdict.
type Report struct {
	Checks []Check
	// UsableBuilder is true when at least one checked kind has its binary on
	// PATH: the kind came from a candidate that parsed, or from a binding's
	// recorded builder. It is meaningless in adopted mode, where the binary is
	// deliberately not probed, so nothing reads it there.
	UsableBuilder bool

	// NoCandidates and BuilderRefusal are verdict inputs the caller sets
	// from configuration Run does not see (candidates.json, policy.json,
	// the ledger). Run leaves them zero. The footer reads them in the
	// order failures, NoCandidates, !UsableBuilder, BuilderRefusal.
	NoCandidates   bool
	BuilderRefusal string // RoleRefusal.Text for builder, "" when bind would pick
}

// Failures counts checks with SevFail.
func (r Report) Failures() int {
	count := 0
	for _, c := range r.Checks {
		if c.Severity == SevFail {
			count++
		}
	}
	return count
}

// Warnings counts checks with SevWarn.
func (r Report) Warnings() int {
	count := 0
	for _, c := range r.Checks {
		if c.Severity == SevWarn {
			count++
		}
	}
	return count
}

func parseSemver(s string) (major, minor, patch int, err error) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0, 0, 0, fmt.Errorf("invalid semver: %s", s)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, err
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, err
	}
	if len(parts) >= 3 {
		patchStr := parts[2]
		if idx := strings.IndexAny(patchStr, "-+"); idx != -1 {
			patchStr = patchStr[:idx]
		}
		patch, err = strconv.Atoi(patchStr)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	return major, minor, patch, nil
}

func semverAtLeast(v, floor string) (bool, error) {
	maj1, min1, pat1, err := parseSemver(v)
	if err != nil {
		return false, err
	}
	maj2, min2, pat2, err := parseSemver(floor)
	if err != nil {
		return false, err
	}
	if maj1 != maj2 {
		return maj1 > maj2, nil
	}
	if min1 != min2 {
		return min1 > min2, nil
	}
	return pat1 >= pat2, nil
}

// RunOption configures doctor execution.
type RunOption func(*runConfig)

type runConfig struct {
	adopted        bool
	definitions    map[string][]string
	usagePrices    []byte
	usageOn        bool
	usageOpencode  bool
	extra          []Check
	stateRoot      string
	configWarnings []string
}

// WithAdopted scopes the per-kind checks to an adopted builder: the user
// launched that agent themselves, so its binary and role are none of relevo's
// business. Global rows are unaffected.
func WithAdopted(adopted bool) RunOption {
	return func(cfg *runConfig) {
		cfg.adopted = adopted
	}
}

// WithDefinitions limits the role rows for each kind to the named
// definitions: the ones some candidate on that harness would actually
// load (#166). A kind absent from the map keeps every shipped
// definition, which is what a caller with no candidate knowledge wants.
func WithDefinitions(defs map[string][]string) RunOption {
	return func(cfg *runConfig) {
		cfg.definitions = defs
	}
}

// WithUsage enables the round-usage checks (#142): sqlite3 on PATH when an
// opencode candidate is configured (relevo confirms a push to an opencode
// planner through it), and the stored prices body's as_of age. prices is the
// config section body, nil when the section is absent (#4.9).
func WithUsage(prices []byte, opencodeConfigured bool) RunOption {
	return func(cfg *runConfig) {
		cfg.usagePrices = prices
		cfg.usageOn = true
		cfg.usageOpencode = opencodeConfigured
	}
}

// WithExtraChecks appends checks verbatim at the end of the report, after
// every check Run itself builds (including the usage checks WithUsage
// enables). It exists so a caller can fold in checks built from data Run
// never sees -- remote server probes, assembled in cmd/relevo from
// servers.json and the network -- without Run knowing anything about
// either. It never changes the verdict except through the severities the
// checks themselves carry.
func WithExtraChecks(checks []Check) RunOption {
	return func(cfg *runConfig) {
		cfg.extra = append(cfg.extra, checks...)
	}
}

// WithStateRoot lets the opencode branch check opencode's own
// permission.external_directory allowlist against relevo's state root, where
// plans and reports are staged (#236). Empty disables the check, which is
// what a caller without a store wants.
func WithStateRoot(root string) RunOption {
	return func(cfg *runConfig) {
		cfg.stateRoot = root
	}
}

// WithConfigWarnings supplies the unknown-key and skipped-candidate warnings
// the config readers produced (#372 §4.4), rendered as the one global `config`
// row. No warnings renders it OK.
func WithConfigWarnings(warnings []string) RunOption {
	return func(cfg *runConfig) {
		cfg.configWarnings = warnings
	}
}

// ConfigCheck is the one global doctor row for config the readers could not
// fully use: unknown keys in policy.json, and candidates skipped for an
// unknown harness or role (#372 §4.4). No warnings -> OK; otherwise a Warn
// listing them all.
func ConfigCheck(warnings []string) Check {
	c := Check{Name: "config", Group: ""}
	if len(warnings) == 0 {
		c.Severity = SevOK
		c.Detail = "the policy and candidates sections have no unknown keys"
		return c
	}
	c.Severity = SevWarn
	c.Detail = strings.Join(warnings, "; ")
	c.Fix = "remove the unknown keys, or upgrade relevo to the version that reads them"
	return c
}

// wants reports whether the role row for definition name on kind is in
// scope under cfg.definitions.
func (cfg runConfig) wants(kind, name string) bool {
	defs, limited := cfg.definitions[kind]
	if !limited {
		return true
	}
	for _, d := range defs {
		if d == name {
			return true
		}
	}
	return false
}

// frontmatterModel returns the value of a `model:` key in the leading `---`
// fenced block, or "" when there is none.
//
// Deliberately shallow: this reports a fact about an installed file for a
// human to read, so a malformed file yields "" rather than an error. An
// absent model pin is not a fault, and a parse failure must never mask the
// fact that the file exists.
func frontmatterModel(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	// Locate the closing fence first: a `model:` line inside frontmatter
	// that never terminates is not a pin.
	end := -1
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "" // unterminated frontmatter
	}
	for _, line := range lines[1 : end+1] {
		rest, ok := strings.CutPrefix(line, "model:")
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return "" // frontmatter ended with no model key
}

// pinnedModel returns the model pin recorded in an installed role
// definition's raw bytes, dispatching on the kind's shipped format: a TOML
// profile's top-level `model` key, or a Markdown definition's frontmatter
// `model:` key.
func pinnedModel(kind string, raw []byte) string {
	if h, ok := harness.Lookup(kind); ok && h.DocExt == "toml" {
		return tomlTopLevelModel(raw)
	}
	return frontmatterModel(raw)
}

// tomlTopLevelModel returns the value of a top-level `model = "..."` key,
// or "" when there is none. The first `[table]` header ends the top level,
// so a model key inside a table (e.g. [agents.researcher]) is not a pin
// for the profile itself. `model_reasoning_effort` must not match: after
// trimming the "model" prefix, the remainder starts with "_reasoning_effort",
// which does not begin with "=" once trimmed, so the line is skipped.
func tomlTopLevelModel(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		rest, ok := strings.CutPrefix(line, "model")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		rest, ok = strings.CutPrefix(rest, "=")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return v[1 : len(v)-1]
		}
	}
	return ""
}

// roleCheck probes one shipped role definition on disk.
func roleCheck(env Env, kind string, r harness.Role) Check {
	homeRel := "~/" + r.Path
	fullPath, err := env.HomePath(r.Path)
	if err != nil {
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}
	}
	if env.Stat(fullPath) != nil {
		// The fix must work on a machine that has never run this harness
		// as a sub-agent host: none of the agents/ directories exist yet
		// (#166 §1); relevo config agents creates the directory.
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix:    fmt.Sprintf("relevo config agents --kind %s --role %s", kind, r.Name),
		}
	}

	detail := homeRel
	// A read error is deliberately swallowed: the file exists, which is
	// what this row reports, and the model pin is a courtesy on top of
	// that -- except on a kind whose pin would override the launch line.
	if raw, err := env.ReadFile(fullPath); err == nil {
		if model := pinnedModel(kind, raw); model != "" {
			detail = fmt.Sprintf("%s (model: %s)", homeRel, model)
			if r.ExpectModel != "" && model != r.ExpectModel {
				if h, ok := harness.Lookup(kind); ok && h.DocExt == "toml" {
					return Check{
						Group: kind, Name: r.Name, Severity: SevWarn,
						Detail: fmt.Sprintf("%s -- pins %s; relevo ships %s", detail, model, r.ExpectModel),
						Fix:    fmt.Sprintf("relevo config agents --kind %s --role %s --force", kind, r.Name),
					}
				}
				return Check{
					Group: kind, Name: r.Name, Severity: SevWarn,
					Detail: fmt.Sprintf("%s -- pins a tier; the candidate's --model is ignored", detail),
					Fix:    fmt.Sprintf("set model: %s in %s", r.ExpectModel, homeRel),
				}
			}
		}
		// Drift from the shipped bytes is only a finding on a kind whose
		// definition relevo owns outright (ExpectModel set: the pin must be
		// inherit, so any edit is already wrong). Elsewhere the README invites
		// the user to repin model:, and a warning whose fix overwrites that
		// edit would be worse than silence. #91 is the case this catches:
		// an agy copy that predates a tools: fix starts a builder that
		// cannot build, and nothing else on this machine notices.
		if r.ExpectModel != "" {
			if shipped, shipErr := harness.AgentDoc(r.Name, kind); shipErr == nil {
				if !harness.DocEqual(shipped, raw) {
					return Check{
						Group: kind, Name: r.Name, Severity: SevWarn,
						Detail: fmt.Sprintf("%s -- differs from the definition this relevo ships", detail),
						Fix:    fmt.Sprintf("relevo config agents --kind %s --role %s --force", kind, r.Name),
					}
				}
			}
		}
	}
	return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
}

// customRoleCheck probes one custom role definition on disk (#374 §3.6). A
// custom definition is the user's own file: relevo never installs it, so
// there is no model-pin check, no drift check and no manifest entry, and a
// missing file's fix is by hand rather than `relevo config agents`.
func customRoleCheck(env Env, kind, name string) Check {
	path, _ := harness.DefinitionPath(kind, name)
	homeRel := "~/" + path
	fullPath, err := env.HomePath(path)
	if err != nil {
		return Check{
			Group: kind, Name: name, Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}
	}
	if env.Stat(fullPath) != nil {
		return Check{
			Group: kind, Name: name, Severity: SevWarn,
			Detail: "missing: " + homeRel + " (custom)",
			Fix:    "install your agent definition at " + homeRel + "; relevo never installs a custom definition",
		}
	}
	return Check{Group: kind, Name: name, Severity: SevOK, Detail: homeRel + " (custom)"}
}

// roleInstallEnv adapts Env to harness.InstallEnv for the dry-run install the
// role-staleness row runs (#371 §4.10). A dry run never writes, so MkdirAll,
// WriteFile and SaveManifest are unreachable; they are no-ops rather than
// silent fallbacks because doctor must never touch a user's files.
type roleInstallEnv struct {
	env      Env
	manifest map[string]string
}

func (e roleInstallEnv) LookPath(binary string) (string, error) { return e.env.LookPath(binary) }
func (e roleInstallEnv) HomePath(rel string) (string, error)    { return e.env.HomePath(rel) }
func (e roleInstallEnv) MkdirAll(string) error                  { return nil }
func (e roleInstallEnv) WriteFile(string, []byte) error         { return nil }

// ReadFile reports fs.ErrNotExist for a path Stat says is absent, so the
// dry-run decision table reads a missing definition as "would write" rather
// than as an empty file that differs.
func (e roleInstallEnv) ReadFile(path string) ([]byte, error) {
	if err := e.env.Stat(path); err != nil {
		return nil, fs.ErrNotExist
	}
	return e.env.ReadFile(path)
}

func (e roleInstallEnv) LoadManifest() (map[string]string, error) { return e.manifest, nil }
func (e roleInstallEnv) SaveManifest(map[string]string) error     { return nil }

// rolesCheck is §4.10's role-staleness row, one per harness whose binary is on
// PATH: a dry-run install with the manifest says whether the daemon will
// refresh anything on its next start, whether the user's own edits are being
// kept, or whether every definition is already current.
func rolesCheck(env Env, kind string) Check {
	manifest, err := env.LoadManifest()
	if err != nil {
		// A manifest relevo cannot read records nothing, which is exactly
		// what the dry run decides from: an empty map.
		manifest = nil
	}

	results, err := harness.Install(roleInstallEnv{env: env, manifest: manifest}, harness.InstallOptions{
		Kind:   kind,
		DryRun: true,
	})
	if err != nil {
		return Check{
			Group: kind, Name: "roles", Severity: SevOK,
			Detail:      fmt.Sprintf("not checked -- %v", err),
			ProbeFailed: true,
		}
	}

	stale, edited := false, false
	for _, r := range results {
		switch r.Outcome {
		case harness.OutcomeWouldWrite, harness.OutcomeWouldUpdate:
			stale = true
		case harness.OutcomeKeptDiffers:
			edited = true
		case harness.OutcomeError:
			return Check{
				Group: kind, Name: "roles", Severity: SevWarn,
				Detail:      fmt.Sprintf("could not check the role definitions: %s", r.Err),
				ProbeFailed: true,
			}
		}
	}

	switch {
	case stale:
		return Check{
			Group: kind, Name: "roles", Severity: SevWarn,
			Detail: "role definitions are stale; the daemon refreshes them on its next start, or run relevo config agents",
			Fix:    "relevo config agents",
		}
	case edited:
		return Check{Group: kind, Name: "roles", Severity: SevOK, Detail: "differs from every copy relevo has shipped (kept as your edit)"}
	default:
		return Check{Group: kind, Name: "roles", Severity: SevOK, Detail: "up to date"}
	}
}

// releaseCheck is the one row about relevo itself (#293): which install this
// is, and whether the daemon's cached check has seen a newer release.
//
// It is never SevFail -- a stale relevo runs fine -- and SevOK whenever relevo
// cannot prove anything, so an unrefreshed cache, an offline machine, an
// unclassifiable install and a (devel) build all read as "not checked".
func releaseCheck(env Env) Check {
	running, latest, ok, kind := env.ReleaseState()

	if !ok || kind == release.KindUnknown {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	// Neither side parseable means no honest claim is available; "(devel)"
	// lands here, which is why an untagged local build stays quiet.
	if _, rok := release.ParseVersion(running); !rok {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	if _, lok := release.ParseVersion(latest); !lok {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	if kind == release.KindLocalBuild {
		return Check{
			Name:     "release",
			Severity: SevOK,
			Detail:   fmt.Sprintf("local build %s; nothing to update to", running),
		}
	}
	if !release.NewerStrings(running, latest) {
		return Check{
			Name:     "release",
			Severity: SevOK,
			Detail:   fmt.Sprintf("%s is current", running),
		}
	}
	return Check{
		Name:     "release",
		Severity: SevWarn,
		Detail:   fmt.Sprintf("%s is behind %s", running, latest),
		Fix:      releaseFix(kind, latest, runtime.GOOS, runtime.GOARCH),
	}
}

// releaseFix names the update path of the variant that is actually
// installed. KindUnknown and KindLocalBuild have no path, but neither
// reaches a Fix: both are SevOK.
func releaseFix(kind release.Kind, latest, goos, goarch string) string {
	switch kind {
	case release.KindGoInstall:
		return "go install github.com/fuad-daoud/relevo/cmd/relevo@latest"
	case release.KindRelease:
		archive, checksums := release.AssetURLs(latest, goos, goarch)
		return fmt.Sprintf("download %s, check it against %s, and replace this relevo binary with the one inside", archive, checksums)
	}
	return ""
}

// Run executes every check for the given kinds against env.
// kinds is the caller's choice of scope; Run does not discover it.
// daemonCheck builds the daemon row while the daemon is running (#371 §4.8).
// Four states: no record (a daemon older than #371, which will not follow an
// upgrade until restarted), a binary the daemon refused, a version behind the
// CLI (transient during a re-exec), and equal.
func daemonCheck(env Env) Check {
	cli, _, _, _ := env.ReleaseState()
	info, ok, err := env.DaemonInfo()
	if err != nil {
		// An unreadable record is "no record": the daemon runs, and the safe
		// answer is the one that tells a human to restart once.
		ok = false
	}

	switch {
	case !ok:
		return Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevWarn,
			Detail:   "running, but started before relevo recorded its version: it will not follow upgrades until restarted once",
			Fix:      "systemctl --user restart relevo.service, or make service",
		}
	case info.ReexecFailed != nil:
		return Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevWarn,
			Detail: fmt.Sprintf("runs %s; the relevo binary at %s failed preflight (%s) and was not loaded",
				info.Version, info.Exe, info.ReexecFailed.Reason),
			Fix: "fix the error above; the daemon retries when the file changes",
		}
	case info.Version != cli:
		return Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevOK,
			Detail:   fmt.Sprintf("runs %s; switching to %s within seconds", info.Version, cli),
			Fix:      "",
		}
	default:
		return Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevOK,
			Detail:   fmt.Sprintf("running %s", info.Version),
			Fix:      "",
		}
	}
}

// Run never returns an error -- a failed probe becomes a Check saying so.
func Run(ctx context.Context, env Env, kinds []string, opts ...RunOption) Report {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var checks []Check

	// 1. Relevo's own install and release state (#293): unconditional. There
	// is nothing to opt into -- it reads one small file and never touches the
	// network.
	checks = append(checks, releaseCheck(env))

	// 2. daemon probe
	daemonRunning, dErr := env.DaemonRunning(ctx)
	if dErr != nil {
		checks = append(checks, Check{
			Group:       "",
			Name:        "daemon",
			Severity:    SevWarn,
			Detail:      fmt.Sprintf("probe error: %v", dErr),
			Fix:         "relevo daemon",
			ProbeFailed: true,
		})
	} else if !daemonRunning {
		checks = append(checks, Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevWarn,
			Detail:   "not running",
			Fix:      "relevo daemon",
		})
	} else {
		checks = append(checks, daemonCheck(env))
	}

	// #372 §4.4: one global row for the config keys this relevo could not use,
	// from candidates.json and policy.json.
	checks = append(checks, ConfigCheck(cfg.configWarnings))

	usableBuilder := false

	// Per-kind checks
	for _, kind := range kinds {
		h, known := harness.Lookup(kind)
		binName := kind
		if known && h.Binary != "" {
			binName = h.Binary
		}

		if !cfg.adopted {
			binPath, binErr := env.LookPath(binName)
			if binErr != nil {
				checks = append(checks, Check{
					Group:    kind,
					Name:     "binary",
					Severity: SevWarn,
					Detail:   "not on PATH -- skipping the rest of this harness",
					Fix:      "",
				})
				continue
			}

			checks = append(checks, Check{
				Group:    kind,
				Name:     "binary",
				Severity: SevOK,
				Detail:   binPath,
				Fix:      "",
			})
			// A candidate for this kind parsed (it is why the kind is in
			// scope) and its binary runs here, which is all #303 §4.8 means
			// by a usable builder.
			usableBuilder = true

			if known && h.MinVersion != "" {
				// A harness with a floor is held to it before its roles are checked:
				// below the floor, --agent has nothing to select and every role row
				// would be reporting a file the binary cannot load (spec §7.6).
				ver, verr := env.BinaryVersion(ctx, binPath)
				switch {
				case verr != nil:
					checks = append(checks, Check{
						Group: kind, Name: "version", Severity: SevWarn,
						Detail: fmt.Sprintf("could not read version: %v", verr), ProbeFailed: true,
					})
				default:
					atLeast, semErr := semverAtLeast(ver, h.MinVersion)
					switch {
					case semErr != nil:
						checks = append(checks, Check{
							Group: kind, Name: "version", Severity: SevWarn,
							Detail: fmt.Sprintf("unparseable version %q", ver),
						})
					case !atLeast:
						checks = append(checks, Check{
							Group: kind, Name: "version", Severity: SevFail,
							Detail: fmt.Sprintf("%s (below floor %s)", ver, h.MinVersion),
							Fix:    fmt.Sprintf("upgrade %s to >= %s", h.Binary, h.MinVersion),
						})
					default:
						checks = append(checks, Check{
							Group: kind, Name: "version", Severity: SevOK,
							Detail: fmt.Sprintf("%s (floor %s)", ver, h.MinVersion),
						})
					}
				}
			}
		}

		// #256: opencode 2.x runs a shared background service; note it even
		// for an adopted pane, since the service is per-user, not per-binding.
		if kind == "opencode" {
			if c := opencodeServiceCheck(ctx, env); c.Name != "" {
				checks = append(checks, c)
			}
		}

		if !cfg.adopted {
			// §4.10: for every harness whose binary is on PATH, a dry run
			// with the manifest says whether the daemon will refresh
			// anything on its next start. An unknown kind has no shipped
			// definitions to dry-run, so it gets no row.
			if known {
				checks = append(checks, rolesCheck(env, kind))
			}

			// Role checks: one row per shipped role in scope (see WithDefinitions). Every known kind has rows (#85).
			switch {
			case !known:
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   fmt.Sprintf("not checked -- relevo has no role path for kind %q", kind),
					Fix:      "",
				})
			default:
				for _, r := range h.Roles {
					if !cfg.wants(kind, r.Name) {
						continue
					}
					checks = append(checks, roleCheck(env, kind, r))
				}
				// A definition in scope that relevo does not ship is the user's
				// own file: one row per custom name, after the shipped rows
				// (#374 §3.6). Legacy mode has no custom names, so its output
				// is unchanged.
				custom := append([]string(nil), cfg.definitions[kind]...)
				sort.Strings(custom)
				for _, name := range custom {
					if harness.IsShipped(kind, name) {
						continue
					}
					checks = append(checks, customRoleCheck(env, kind, name))
				}
			}

			// #236: opencode's own config decides whether a headless builder
			// can read its plan under relevo's state root. Adopted panes are
			// the user's own agent, so that path stays quiet.
			if kind == "opencode" && cfg.stateRoot != "" {
				checks = append(checks, opencodeAllowlistCheck(env, cfg.stateRoot))
			}

			// #393 §5.5: the shipped OpenCode plugin package, and whether
			// another command already binds a key it uses. The keys row is
			// silent unless the plugin is installed.
			if kind == "opencode" {
				checks = append(checks, opencodePluginCheck(env))
				if kc := opencodePluginKeysCheck(env); kc.Name != "" {
					checks = append(checks, kc)
				}
			}
		}
	}

	if cfg.usageOn {
		checks = append(checks, usageChecks(env, cfg)...)
	}

	checks = append(checks, cfg.extra...)

	return Report{
		Checks:        checks,
		UsableBuilder: usableBuilder,
	}
}

// pricesMaxAge is how old prices.json's as_of may be before doctor warns.
const pricesMaxAge = 90 * 24 * time.Hour

func usageChecks(env Env, cfg runConfig) []Check {
	var out []Check
	if cfg.usageOpencode {
		if _, err := env.LookPath("sqlite3"); err != nil {
			out = append(out, Check{
				Name: "sqlite3", Severity: SevWarn,
				Detail: "not on PATH; relevo cannot confirm a push to an opencode planner, so its reports wait for the background wait",
				Fix:    "install sqlite3 (the CLI), e.g. pacman -S sqlite / apt install sqlite3",
			})
		} else {
			out = append(out, Check{Name: "sqlite3", Severity: SevOK, Detail: "on PATH; pushes to an opencode planner can be confirmed"})
		}
	}
	if cfg.usagePrices == nil {
		d := usage.DefaultPrices()
		out = append(out, Check{Name: "prices", Severity: SevOK,
			Detail: fmt.Sprintf("no prices configured; using the embedded default (as_of %s, %d models)", d.AsOf, len(d.Models))})
		return out
	}
	var p usage.Prices
	if err := json.Unmarshal(cfg.usagePrices, &p); err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("the stored prices body does not validate: %v; rounds estimate from the embedded default", err),
			Fix:    "fix the prices section"})
		return out
	}
	asOf, err := time.Parse("2006-01-02", p.AsOf)
	switch {
	case err != nil:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("prices: as_of %q is not YYYY-MM-DD", p.AsOf), Fix: "set as_of to the date the prices were checked"})
	case time.Since(asOf) > pricesMaxAge:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("prices: as_of %s is older than %d days; estimates may be stale", p.AsOf, int(pricesMaxAge.Hours()/24)),
			Fix:    "check the providers' pricing pages and update as_of"})
	default:
		out = append(out, Check{Name: "prices", Severity: SevOK, Detail: fmt.Sprintf("prices: as_of %s, %d models", p.AsOf, len(p.Models))})
	}
	return out
}

// ClassifyCheck is the one global doctor row for the classifier. It never
// fails doctor: regex runs regardless.
//
//	!st.Configured                -> SevOK,   Detail "regex only (no classify block in policy.json)"
//	Configured, KeySource "env"   -> SevOK,   Detail "<model>; key from TYPESAFE_API_KEY; not passed to builders"
//	Configured, KeySource "db"    -> SevOK,   Detail "<model>; key from the database"
//	Configured, KeySource ""      -> SevWarn, Detail "<model> configured but no classifier key; the daemon falls back to regex"
//	                                           Fix "set TYPESAFE_API_KEY for the daemon, or store a key in the database"
//
// Name "classify", Group "". No network probe: a bad key is reported by the
// first round's entry note, not by doctor.
func ClassifyCheck(st classify.Status) Check {
	c := Check{
		Name:  "classify",
		Group: "",
	}
	if !st.Configured {
		c.Severity = SevOK
		c.Detail = "regex only (no classify block in policy.json)"
		return c
	}
	switch st.KeySource {
	case "env":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s; key from TYPESAFE_API_KEY; not passed to builders", st.Model)
	case "db":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s; key from the database", st.Model)
	default:
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("%s configured but no classifier key; the daemon falls back to regex", st.Model)
		c.Fix = "set TYPESAFE_API_KEY for the daemon, or store a key in the database"
	}
	return c
}
