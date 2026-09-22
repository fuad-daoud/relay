package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/release"
	"github.com/fuad-daoud/relay/internal/usage"
)

type Severity int

const (
	SevOK   Severity = iota // nothing to do; also used for "not checked"
	SevWarn                 // wrong, but relay can still run
	SevFail                 // relay cannot run
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
	Name     string // "herdr", "daemon", "binary", "integration", "plan-executor"
	Severity Severity
	Detail   string // what was actually found
	Fix      string // the command that fixes it
	// ProbeFailed marks a row where relay could not establish the fact at all
	// (the probe errored, timed out, or returned nothing for this target) as
	// opposed to establishing that something is wrong. The bind-time preflight
	// skips these, because there is nothing the user can act on.
	ProbeFailed bool
}

// Report is every check, in render order, plus the derived verdict.
type Report struct {
	Checks []Check
	// UsableBuilder is true when at least one checked kind has both its binary
	// on PATH and its integration installed. It is meaningless in adopted mode,
	// where the binary is deliberately not probed, so nothing reads it there.
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
	adopted       bool
	definitions   map[string][]string
	usagePrices   string
	usageOpencode bool
	extra         []Check
	stateRoot     string
}

// WithAdopted scopes the per-kind checks to an adopted pane: the user launched
// that agent themselves, so its binary and role are none of relay's business,
// but its integration still decides whether a round can be observed to finish.
// Global rows are unaffected.
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
// opencode candidate is configured (its pane rounds read opencode.db
// through it), and prices.json parsing and age.
func WithUsage(pricesPath string, opencodeConfigured bool) RunOption {
	return func(cfg *runConfig) {
		cfg.usagePrices = pricesPath
		cfg.usageOpencode = opencodeConfigured
	}
}

// WithExtraChecks appends checks verbatim at the end of the report, after
// every check Run itself builds (including the usage checks WithUsage
// enables). It exists so a caller can fold in checks built from data Run
// never sees -- remote server probes, assembled in cmd/relay from
// servers.json and the network -- without Run knowing anything about
// either. It never changes the verdict except through the severities the
// checks themselves carry.
func WithExtraChecks(checks []Check) RunOption {
	return func(cfg *runConfig) {
		cfg.extra = append(cfg.extra, checks...)
	}
}

// WithStateRoot lets the opencode branch check opencode's own
// permission.external_directory allowlist against relay's state root, where
// plans and reports are staged (#236). Empty disables the check, which is
// what a caller without a store wants.
func WithStateRoot(root string) RunOption {
	return func(cfg *runConfig) {
		cfg.stateRoot = root
	}
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
		// (#166 §1); relay agent install creates the directory.
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix:    fmt.Sprintf("relay agent install --kind %s --role %s", kind, r.Name),
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
						Detail: fmt.Sprintf("%s -- pins %s; relay ships %s", detail, model, r.ExpectModel),
						Fix:    fmt.Sprintf("relay agent install --kind %s --role %s --force", kind, r.Name),
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
		// definition relay owns outright (ExpectModel set: the pin must be
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
						Detail: fmt.Sprintf("%s -- differs from the definition this relay ships", detail),
						Fix:    fmt.Sprintf("relay agent install --kind %s --role %s --force", kind, r.Name),
					}
				}
			}
		}
	}
	return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
}

// releaseCheck is the one row about relay itself (#293): which install this
// is, and whether the daemon's cached check has seen a newer release.
//
// It is never SevFail -- a stale relay runs fine -- and SevOK whenever relay
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
		Fix:      releaseFix(kind),
	}
}

// releaseFix names the update path of the variant that is actually
// installed. KindUnknown and KindLocalBuild have no path, but neither
// reaches a Fix: both are SevOK.
func releaseFix(kind release.Kind) string {
	switch kind {
	case release.KindPluginRelease:
		return "sh scripts/plugin-fetch.sh"
	case release.KindPluginSource:
		return "sh scripts/plugin-build.sh"
	case release.KindGoInstall:
		return "go install github.com/fuad-daoud/relay/cmd/relay@latest"
	}
	return ""
}

// Run executes every check for the given kinds against env.
// kinds is the caller's choice of scope; Run does not discover it.
// Run never returns an error -- a failed probe becomes a Check saying so.
func Run(ctx context.Context, env Env, kinds []string, opts ...RunOption) Report {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var checks []Check

	// 1. herdr probe
	herdrVer, err := env.HerdrVersion(ctx)
	if err != nil {
		checks = append(checks, Check{
			Group:       "",
			Name:        "herdr",
			Severity:    SevFail,
			Detail:      err.Error(),
			Fix:         "",
			ProbeFailed: true,
		})
	} else {
		atLeast, semErr := semverAtLeast(herdrVer, herdr.MinVersion)
		if semErr != nil {
			checks = append(checks, Check{
				Group:    "",
				Name:     "herdr",
				Severity: SevFail,
				Detail:   fmt.Sprintf("unparseable version %q", herdrVer),
				Fix:      "",
			})
		} else if !atLeast {
			checks = append(checks, Check{
				Group:    "",
				Name:     "herdr",
				Severity: SevFail,
				Detail:   fmt.Sprintf("%s (below floor %s)", herdrVer, herdr.MinVersion),
				Fix:      "",
			})
		} else {
			checks = append(checks, Check{
				Group:    "",
				Name:     "herdr",
				Severity: SevOK,
				Detail:   fmt.Sprintf("%s (floor %s)", herdrVer, herdr.MinVersion),
				Fix:      "",
			})
		}
	}

	// Relay's own install and release state (#293), right beside the herdr
	// probe and for the same reason: it is unconditional. There is nothing to
	// opt into -- it reads one small file and never touches the network.
	checks = append(checks, releaseCheck(env))

	// 2. daemon probe
	daemonRunning, dErr := env.DaemonRunning(ctx)
	if dErr != nil {
		checks = append(checks, Check{
			Group:       "",
			Name:        "daemon",
			Severity:    SevWarn,
			Detail:      fmt.Sprintf("probe error: %v", dErr),
			Fix:         "relay daemon",
			ProbeFailed: true,
		})
	} else if !daemonRunning {
		checks = append(checks, Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevWarn,
			Detail:   "not running",
			Fix:      "relay daemon",
		})
	} else {
		checks = append(checks, Check{
			Group:    "",
			Name:     "daemon",
			Severity: SevOK,
			Detail:   "running",
			Fix:      "",
		})
	}

	// Query integration status
	intStatusMap, intStatusErr := env.IntegrationStatus(ctx)

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

		// Integration check
		target := kind
		if known && h.Integration != "" {
			target = h.Integration
		}

		kindHasInstalledIntegration := false

		if intStatusErr != nil {
			checks = append(checks, Check{
				Group:       kind,
				Name:        "integration",
				Severity:    SevWarn,
				Detail:      fmt.Sprintf("integration status unavailable: %v", intStatusErr),
				Fix:         "",
				ProbeFailed: true,
			})
		} else {
			state, found := intStatusMap[target]
			if !found {
				if !known {
					checks = append(checks, Check{
						Group:    kind,
						Name:     "integration",
						Severity: SevOK,
						Detail:   fmt.Sprintf("not checked -- herdr has no integration for kind %q", kind),
						Fix:      "",
					})
				} else {
					checks = append(checks, Check{
						Group:       kind,
						Name:        "integration",
						Severity:    SevWarn,
						Detail:      fmt.Sprintf("could not read herdr integration status for %s", target),
						Fix:         "",
						ProbeFailed: true,
					})
				}
			} else if !state.Installed {
				checks = append(checks, Check{
					Group:    kind,
					Name:     "integration",
					Severity: SevFail,
					Detail:   "not installed -- this binding will report `unknown` forever and never finish a round",
					Fix:      fmt.Sprintf("herdr integration install %s", target),
				})
			} else if state.Outdated {
				kindHasInstalledIntegration = true
				checks = append(checks, Check{
					Group:    kind,
					Name:     "integration",
					Severity: SevWarn,
					Detail:   state.Detail,
					Fix:      fmt.Sprintf("herdr integration install %s", target),
				})
			} else {
				kindHasInstalledIntegration = true
				detail := state.Detail
				if detail == "" {
					detail = "current"
				}
				checks = append(checks, Check{
					Group:    kind,
					Name:     "integration",
					Severity: SevOK,
					Detail:   detail,
					Fix:      "",
				})
			}
		}

		if kindHasInstalledIntegration {
			usableBuilder = true
		}

		// #256: opencode 2.x runs a shared background service; note it even
		// for an adopted pane, since the service is per-user, not per-binding.
		if kind == "opencode" {
			if c := opencodeServiceCheck(ctx, env); c.Name != "" {
				checks = append(checks, c)
			}
		}

		if !cfg.adopted {
			// Role checks: one row per shipped role in scope (see WithDefinitions). Every known kind has rows (#85).
			switch {
			case !known:
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   fmt.Sprintf("not checked -- relay has no role path for kind %q", kind),
					Fix:      "",
				})
			default:
				for _, r := range h.Roles {
					if !cfg.wants(kind, r.Name) {
						continue
					}
					checks = append(checks, roleCheck(env, kind, r))
				}
			}

			// #236: opencode's own config decides whether a headless builder
			// can read its plan under relay's state root. Adopted panes are
			// the user's own agent, so that path stays quiet.
			if kind == "opencode" && cfg.stateRoot != "" {
				checks = append(checks, opencodeAllowlistCheck(env, cfg.stateRoot))
			}
		}
	}

	// Second pass: if UsableBuilder is true, demote every integration: not installed row
	// from SevFail to SevWarn.
	if usableBuilder {
		for i := range checks {
			if checks[i].Name == "integration" && checks[i].Severity == SevFail {
				checks[i].Severity = SevWarn
			}
		}
	}

	if cfg.usagePrices != "" {
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
				Detail: "not on PATH; opencode pane rounds record usage as unknown",
				Fix:    "install sqlite3 (the CLI), e.g. pacman -S sqlite / apt install sqlite3",
			})
		} else {
			out = append(out, Check{Name: "sqlite3", Severity: SevOK, Detail: "on PATH; opencode pane usage readable"})
		}
	}
	if err := env.Stat(cfg.usagePrices); err != nil {
		d := usage.DefaultPrices()
		out = append(out, Check{Name: "prices", Severity: SevOK,
			Detail: fmt.Sprintf("no %s; using the embedded default (as_of %s, %d models)", cfg.usagePrices, d.AsOf, len(d.Models))})
		return out
	}
	raw, err := env.ReadFile(cfg.usagePrices)
	if err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn, Detail: cfg.usagePrices + ": " + err.Error(), ProbeFailed: true})
		return out
	}
	var p usage.Prices
	if err := json.Unmarshal(raw, &p); err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s does not validate: %v; rounds estimate from the embedded default", cfg.usagePrices, err),
			Fix:    "fix the JSON or delete the file"})
		return out
	}
	asOf, err := time.Parse("2006-01-02", p.AsOf)
	switch {
	case err != nil:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s: as_of %q is not YYYY-MM-DD", cfg.usagePrices, p.AsOf), Fix: "set as_of to the date the prices were checked"})
	case time.Since(asOf) > pricesMaxAge:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s: as_of %s is older than %d days; estimates may be stale", cfg.usagePrices, p.AsOf, int(pricesMaxAge.Hours()/24)),
			Fix:    "check the providers' pricing pages and update as_of"})
	default:
		out = append(out, Check{Name: "prices", Severity: SevOK, Detail: fmt.Sprintf("%s: as_of %s, %d models", cfg.usagePrices, p.AsOf, len(p.Models))})
	}
	return out
}

// ClassifyCheck is the one global doctor row for the classifier. It never
// fails doctor: regex runs regardless.
//
//	!st.Configured                  -> SevOK,   Detail "regex only (no classify block in policy.json)"
//	Configured, KeySource "env"     -> SevOK,   Detail "<model>; key from TYPESAFE_API_KEY; not passed to builders"
//	Configured, KeySource "file"    -> SevOK,   Detail "<model>; key from <KeyPath>"
//	Configured, KeyFileLoose        -> SevWarn, Detail "<model> configured but <KeyPath> is readable by others (mode 0644); ignored",
//	                                             Fix "chmod 600 <KeyPath>"
//	Configured, KeySource ""        -> SevWarn, Detail "<model> configured but no key found; the daemon falls back to regex",
//	                                             Fix "set TYPESAFE_API_KEY for the daemon, or write the key to <KeyPath> (chmod 600)"
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
	case "file":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s; key from %s", st.Model, st.KeyPath)
	default:
		c.Severity = SevWarn
		if st.KeyFileLoose {
			c.Detail = fmt.Sprintf("%s configured but %s is readable by others (mode 0%o); ignored", st.Model, st.KeyPath, st.KeyFileMode.Perm())
			c.Fix = fmt.Sprintf("chmod 600 %s", st.KeyPath)
		} else {
			c.Detail = fmt.Sprintf("%s configured but no key found; the daemon falls back to regex", st.Model)
			c.Fix = fmt.Sprintf("set TYPESAFE_API_KEY for the daemon, or write the key to %s (chmod 600)", st.KeyPath)
		}
	}
	return c
}
