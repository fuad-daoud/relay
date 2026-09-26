package release

import "fmt"

// UpdateAction is what `relevo update` should do, decided by DecideUpdate
// before anything touches the network or the disk.
type UpdateAction int

const (
	// UpdateCurrent means the running version is already the target.
	UpdateCurrent UpdateAction = iota
	// UpdateReplace means download the target and replace the running binary.
	UpdateReplace
	// UpdatePrintGoInstall means a `go install` binary: print the command.
	UpdatePrintGoInstall
	// UpdateRefuse means a local or unknown build without --release.
	UpdateRefuse
	// UpdateInvalid means a bad --to value: a usage error.
	UpdateInvalid
)

// String names the action for `--check` output.
func (a UpdateAction) String() string {
	switch a {
	case UpdateCurrent:
		return "current"
	case UpdateReplace:
		return "replace"
	case UpdatePrintGoInstall:
		return "go-install"
	case UpdateRefuse:
		return "refuse"
	case UpdateInvalid:
		return "invalid"
	}
	return "unknown"
}

// UpdateRequest is every fact DecideUpdate reads. The caller gathers them:
// Detect supplies Kind, buildVersion the Running version, the Fetcher the
// Latest tag, and the flags the rest.
type UpdateRequest struct {
	// Kind is release.Detect's classification of the running binary.
	Kind Kind
	// Running is buildVersion(): a clean tag, a describe string or "(devel)".
	Running string
	// Latest is the latest published tag, or "" when the caller skipped the
	// fetch because the decision does not need it.
	Latest string
	// To is the --to value, "" when the flag was not given. A leading "v" is
	// optional.
	To string
	// ForceRelease is --release: replace a local or unknown build anyway.
	ForceRelease bool
}

// UpdateDecision is the pure outcome: what to do, the normalised tag to
// install or print, and the one sentence the CLI prints as is.
type UpdateDecision struct {
	Action  UpdateAction
	Target  string
	Message string
}

// DecideUpdate answers, without touching the network or the disk, what
// `relevo update` should do for req. The rules are ordered and the first
// match wins. Pure, so the table in update_test.go is the
// whole truth about this decision.
func DecideUpdate(req UpdateRequest) UpdateDecision {
	target := req.Latest
	if req.To != "" {
		target = req.To
		if target[0] != 'v' {
			target = "v" + target
		}
		if !IsReleaseTag(target) {
			return UpdateDecision{
				Action:  UpdateInvalid,
				Message: fmt.Sprintf(`--to must be a release tag like v0.13.0, got %q`, req.To),
			}
		}
	} else if req.Latest != "" && !IsReleaseTag(req.Latest) {
		// The latest tag becomes a path segment in the download URL, so a
		// malformed one must be refused before the switch, for every kind:
		// it is never downloaded, and never printed into a go install
		// command either.
		return UpdateDecision{
			Action:  UpdateRefuse,
			Message: fmt.Sprintf("the latest release tag %q is not a release tag like v0.13.0; nothing was downloaded", req.Latest),
		}
	}

	switch req.Kind {
	case KindGoInstall:
		display := target
		if display == "" {
			display = "latest"
		}
		return UpdateDecision{
			Action:  UpdatePrintGoInstall,
			Target:  display,
			Message: "go install github.com/fuad-daoud/relevo/cmd/relevo@" + display,
		}
	case KindLocalBuild, KindUnknown:
		if !req.ForceRelease {
			return UpdateDecision{Action: UpdateRefuse, Message: localBuildRefusal(req.Running)}
		}
		if target == "" {
			return UpdateDecision{Action: UpdateRefuse, Message: "no release tag to update to"}
		}
		return replaceTo(req.Running, target)
	case KindRelease:
		if target == "" {
			return UpdateDecision{Action: UpdateRefuse, Message: "no release tag to update to"}
		}
		if req.To != "" {
			// An explicit --to allows a downgrade, so equal-versions is the
			// only thing that makes it a no-op; the numbers and the suffix
			// both decide.
			rv, rok := ParseVersion(req.Running)
			tv, tok := ParseVersion(target)
			if rok && tok && rv == tv {
				return currentAt(req.Running, target)
			}
			return replaceTo(req.Running, target)
		}
		if NewerStrings(req.Running, target) {
			return replaceTo(req.Running, target)
		}
		return currentAt(req.Running, target)
	}

	// Any other Kind value: refuse exactly like a local build.
	return UpdateDecision{Action: UpdateRefuse, Message: localBuildRefusal(req.Running)}
}

// localBuildRefusal is the one sentence rule 3a and rule 5 share.
func localBuildRefusal(running string) string {
	return fmt.Sprintf("relevo %s is a local build; relevo update replaces only release binaries. relevo update --release replaces it with the latest release binary.", running)
}

func replaceTo(running, target string) UpdateDecision {
	return UpdateDecision{
		Action:  UpdateReplace,
		Target:  target,
		Message: fmt.Sprintf("relevo %s -> %s", running, target),
	}
}

func currentAt(running, target string) UpdateDecision {
	return UpdateDecision{
		Action:  UpdateCurrent,
		Target:  target,
		Message: fmt.Sprintf("relevo %s is current (latest %s)", running, target),
	}
}
