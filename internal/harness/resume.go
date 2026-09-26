package harness

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrResumeUnsupported reports a resume request for a harness relevo has no
// verified resume form for: codex, and any kind relevo was not taught. The
// error names the kind.
var ErrResumeUnsupported = errors.New("resume is not supported for this harness")

// checkResumeSessionID validates a session id exactly as both resume forms
// must: it becomes one argv element, so it must be non-empty, free of
// whitespace, and not start with '-' -- a leading '-' would be read as a
// flag.
func checkResumeSessionID(sessionID string) error {
	if sessionID == "" {
		return errors.New("resume needs a session id")
	}
	if strings.ContainsAny(sessionID, " \t\n\r\v\f") || strings.HasPrefix(sessionID, "-") {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	return nil
}

// Resume renders the argv that continues an existing harness session with one
// more prompt, for the headless consult that asks a closed round's builder
// (#147 part 2). The returned slice is the full argv AFTER the binary -- the
// caller prepends h.Binary, exactly as it does for Launch's print form.
//
// claude and agy continue the session in place: the new turn is appended to
// the session the round recorded, which is why relevo reaches for this only
// once the round is closed. opencode forks: --fork puts the new turn in a
// copy, so the original session is untouched.
//
// sessionID must be non-empty, free of whitespace, and not start with '-': it
// becomes one argv element, and a leading '-' would be read as a flag.
func (h Harness) Resume(sessionID, prompt string, tier Tier) ([]string, error) {
	if err := checkResumeSessionID(sessionID); err != nil {
		return nil, err
	}

	var base []string
	switch h.Kind {
	case "claude":
		base = []string{"-p", prompt, "--resume", sessionID,
			"--output-format", "stream-json", "--verbose"}
	case "agy":
		base = []string{"-p", prompt, "--conversation", sessionID,
			"--output-format", "stream-json"}
	case "opencode":
		base = []string{"run", prompt, "--session", sessionID, "--fork", "--format", "json", "--thinking", "--standalone"}
	case "codex":
		return nil, fmt.Errorf("%w: codex", ErrResumeUnsupported)
	default:
		return nil, fmt.Errorf("%w: harness %q", ErrResumeUnsupported, h.Kind)
	}

	perm, err := h.PermissionArgs(tier)
	if err != nil {
		return nil, err
	}
	return append(append([]string(nil), base...), perm...), nil
}

// ResumeBuild renders the argv that continues a lost headless builder's own
// session (#370, spec §4.10): the builder-grade print form l with the prompt,
// budget, directory and state filled in, followed by this kind's resume
// selector. l must be the Launch for the same candidate and tier the round
// was started with, so a resumed round keeps its model, agent definition and
// permission flags. The returned slice is the full argv AFTER the binary --
// the caller prepends h.Binary, exactly as headlessLaunch does.
//
// A kind with no verified resume selector -- codex, and any kind relevo was
// not taught -- returns ErrResumeUnsupported, which the caller reads as "fall
// back to a fresh relaunch".
//
// sessionID is validated exactly as Resume validates it: it becomes one argv
// element.
func (h Harness) ResumeBuild(sessionID string, l Launch, prompt string, budget time.Duration, dir, state string) ([]string, error) {
	if err := checkResumeSessionID(sessionID); err != nil {
		return nil, err
	}

	var selector []string
	switch h.Kind {
	case "claude":
		selector = []string{"--resume", sessionID}
	case "agy":
		selector = []string{"--conversation", sessionID}
	case "opencode":
		selector = []string{"--session", sessionID, "--fork"}
	default:
		return nil, fmt.Errorf("%w: %s", ErrResumeUnsupported, h.Kind)
	}

	return append(l.PrintArgs(prompt, budget, dir, state), selector...), nil
}
