package doctor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
)

type Severity int

const (
	SevOK   Severity = iota // nothing to do; also used for "not checked"
	SevWarn                 // wrong, but relay can still run
	SevFail                 // relay cannot run
)

func (s Severity) String() string {
	switch s {
	case SevOK:
		return "ok"
	case SevWarn:
		return "warn"
	case SevFail:
		return "FAIL"
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
	adopted bool
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
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix: fmt.Sprintf("relay agent print --kind %s --role %s > %s",
				kind, r.Name, homeRel),
		}
	}

	detail := homeRel
	// A read error is deliberately swallowed: the file exists, which is
	// what this row reports, and the model pin is a courtesy on top of
	// that -- except on a kind whose pin would override the launch line.
	if raw, err := env.ReadFile(fullPath); err == nil {
		if model := frontmatterModel(raw); model != "" {
			detail = fmt.Sprintf("%s (model: %s)", homeRel, model)
			if r.ExpectModel != "" && model != r.ExpectModel {
				return Check{
					Group: kind, Name: r.Name, Severity: SevWarn,
					Detail: fmt.Sprintf("%s -- pins a tier; the candidate's --model is ignored", detail),
					Fix:    fmt.Sprintf("set model: %s in %s", r.ExpectModel, homeRel),
				}
			}
		}
	}
	return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
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

		if !cfg.adopted {
			// Role checks: one row per shipped role. Every known kind has rows (#85).
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
					checks = append(checks, roleCheck(env, kind, r))
				}
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

	return Report{
		Checks:        checks,
		UsableBuilder: usableBuilder,
	}
}
