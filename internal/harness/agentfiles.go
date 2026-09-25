package harness

import "fmt"

// FileState is the state of one agent's definition file on one harness kind,
// as the cockpit's agents view shows it. It is the dry-run InstallOutcome
// with the outcomes that name a user's choice collapsed to one word.
type FileState string

const (
	// FileUpToDate is InstallResult's "kept (identical)": the file on disk
	// is byte-identical to the definition relevo ships.
	FileUpToDate FileState = "up to date"
	// FileStale is InstallResult's "would update": the file on disk is an
	// older copy relevo itself wrote, so the next install refreshes it.
	FileStale FileState = "stale"
	// FileEdited is InstallResult's "kept (differs; --force to overwrite)":
	// the file on disk is the user's own edit.
	FileEdited FileState = "your edit"
	// FileMissing is InstallResult's "would write": no file on disk.
	FileMissing FileState = "missing"
)

// AgentFile is one (agent, harness kind) definition file, as the cockpit's
// agents view reads it.
type AgentFile struct {
	Kind  string    // harness kind
	Path  string    // absolute path, resolved through the InstallEnv home
	State FileState // the dry-run install's verdict
	Model string    // PinnedModel of the file on disk; "" when missing or unpinned
}

// AgentFiles returns agent's definition state on every known kind whose
// binary is on PATH, in All() order. A name relevo does not ship returns
// nil, nil: relevo never writes a custom agent, so it has no file state.
//
// It is a dry run: nothing is written, because Install's DryRun holds.
func AgentFiles(env InstallEnv, agent string) ([]AgentFile, error) {
	if !shippedAnywhere(agent) {
		return nil, nil
	}

	var out []AgentFile
	for _, h := range All() {
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		results, err := Install(env, InstallOptions{Kind: h.Kind, Role: agent, DryRun: true})
		if err != nil {
			return nil, err
		}
		for _, res := range results {
			var state FileState
			switch res.Outcome {
			case OutcomeWouldWrite:
				state = FileMissing
			case OutcomeKeptIdentical:
				state = FileUpToDate
			case OutcomeWouldUpdate:
				state = FileStale
			case OutcomeKeptDiffers:
				state = FileEdited
			case OutcomeError:
				return nil, fmt.Errorf("%s %s: %s", res.Kind, res.Role, res.Err)
			default:
				return nil, fmt.Errorf("%s %s: unexpected install outcome %q", res.Kind, res.Role, res.Outcome)
			}

			full, err := env.HomePath(res.Path)
			if err != nil {
				return nil, err
			}
			af := AgentFile{Kind: res.Kind, Path: full, State: state}
			if state != FileMissing {
				raw, err := env.ReadFile(full)
				if err != nil {
					return nil, err
				}
				af.Model = PinnedModel(res.Kind, raw)
			}
			out = append(out, af)
		}
	}
	return out, nil
}

// ResetAgentFile overwrites agent's definition file on kind with the shipped
// copy and returns Install's single result. Zero results is an error naming
// the pair.
func ResetAgentFile(env InstallEnv, kind, agent string) (InstallResult, error) {
	results, err := Install(env, InstallOptions{Kind: kind, Role: agent, Force: true})
	if err != nil {
		return InstallResult{}, err
	}
	if len(results) == 0 {
		return InstallResult{}, fmt.Errorf("%s has no %s definition", kind, agent)
	}
	return results[0], nil
}

// shippedAnywhere reports whether any known kind ships a definition named
// agent.
func shippedAnywhere(agent string) bool {
	for _, h := range All() {
		if _, ok := h.Role(agent); ok {
			return true
		}
	}
	return false
}
