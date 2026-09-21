package harness

import (
	"errors"
	"fmt"
	"strings"
)

// ErrResumeUnsupported reports a resume request for a harness relay has no
// verified resume form for: codex, and any kind relay was not taught. The
// error names the kind.
var ErrResumeUnsupported = errors.New("resume is not supported for this harness")

// Resume renders the argv that continues an existing harness session with one
// more prompt, for the headless consult that asks a closed round's builder
// (#147 part 2). The returned slice is the full argv AFTER the binary -- the
// caller prepends h.Binary, exactly as it does for Launch's print form.
//
// claude and agy continue the session in place: the new turn is appended to
// the session the round recorded, which is why relay reaches for this only
// once the round is closed. opencode forks: --fork puts the new turn in a
// copy, so the original session is untouched.
//
// sessionID must be non-empty, free of whitespace, and not start with '-': it
// becomes one argv element, and a leading '-' would be read as a flag.
func (h Harness) Resume(sessionID, prompt string, tier Tier) ([]string, error) {
	if sessionID == "" {
		return nil, errors.New("resume needs a session id")
	}
	if strings.ContainsAny(sessionID, " \t\n\r\v\f") || strings.HasPrefix(sessionID, "-") {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
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
		base = []string{"run", prompt, "--session", sessionID, "--fork", "--format", "json", "--standalone"}
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
