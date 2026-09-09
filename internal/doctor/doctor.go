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
	Group       string // "" for global rows, else the harness kind
	Name        string // "herdr", "daemon", "binary", "integration", "plan-executor"
	Severity    Severity
	Detail      string // what was actually found
	Fix         string // the command that fixes it
	ProbeFailed bool   // true when external probe errored/timed out
}

// Report is every check, in render order, plus the derived verdict.
type Report struct {
	Checks        []Check
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

// WithAdopted sets whether checks are scoped to an adopted pane (integration row only).
func WithAdopted(adopted bool) RunOption {
	return func(cfg *runConfig) {
		cfg.adopted = adopted
	}
}

// RunAdopted is shorthand for Run(ctx, env, kinds, WithAdopted(true)).
func RunAdopted(ctx context.Context, env Env, kinds []string) Report {
	return Run(ctx, env, kinds, WithAdopted(true))
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
						Group:    kind,
						Name:     "integration",
						Severity: SevWarn,
						Detail:   fmt.Sprintf("could not read herdr integration status for %s", target),
						Fix:      "",
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
			// Role (plan-executor) check
			if !known {
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   fmt.Sprintf("not checked -- relay has no role path for kind %q", kind),
					Fix:      "",
				})
			} else if h.RolePath == "" {
				checks = append(checks, Check{
					Group:    kind,
					Name:     "plan-executor",
					Severity: SevOK,
					Detail:   "selected by preamble, not a file",
					Fix:      "",
				})
			} else {
				homeRel := "~/" + h.RolePath
				fullPath, hErr := env.HomePath(h.RolePath)
				if hErr != nil {
					checks = append(checks, Check{
						Group:       kind,
						Name:        "plan-executor",
						Severity:    SevWarn,
						Detail:      fmt.Sprintf("could not resolve home directory: %v", hErr),
						Fix:         "",
						ProbeFailed: true,
					})
				} else if env.Stat(fullPath) != nil {
					checks = append(checks, Check{
						Group:    kind,
						Name:     "plan-executor",
						Severity: SevWarn,
						Detail:   fmt.Sprintf("missing: %s", homeRel),
						Fix:      fmt.Sprintf("relay agent print --kind %s > %s", kind, homeRel),
					})
				} else {
					checks = append(checks, Check{
						Group:    kind,
						Name:     "plan-executor",
						Severity: SevOK,
						Detail:   homeRel,
						Fix:      "",
					})
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
