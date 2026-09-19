package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrAgentBlocked is returned when herdr refuses a prompt because the target
// agent is sitting at an approval or question dialog. Answering one requires
// SendKeys, not Prompt.
var ErrAgentBlocked = errors.New("herdr rejected prompt: agent blocked")

// ErrPromptStalled is returned when herdr observed no lifecycle change within
// its five second window after a prompt.
var ErrPromptStalled = errors.New("herdr prompt stalled")

// ErrWaitTimeout is returned when a herdr command that waits for a state gave
// up before observing it. It is distinct from ErrPromptStalled: a timeout says
// the state never arrived, not that herdr judged the prompt inert.
var ErrWaitTimeout = errors.New("herdr wait timed out")

// Sound is herdr's notification sound class (`notification show --sound`).
type Sound string

const (
	SoundNone    Sound = "none"
	SoundDone    Sound = "done"
	SoundRequest Sound = "request"
)

// MetadataSource is the one `--source` relay ever reports under: herdr caps
// a pane at 32 distinct sources, so relay is one source, never one per
// binding (#129).
const MetadataSource = "relay"

// maxTokenValue is herdr's cap on a token value (80 chars, verified 0.9.1).
const maxTokenValue = 80

// PaneMetadata is one `herdr pane report-metadata` call: display-only.
type PaneMetadata struct {
	Tokens      map[string]string // --token NAME=VALUE, emitted sorted by NAME; values truncated to maxTokenValue
	ClearTokens []string          // --clear-token NAME, emitted in slice order
	Seq         int64             // --seq; a later write with a lower seq is ignored by herdr
}

// Client runs the herdr CLI. Every external call is bounded by timeout.
type Client struct {
	bin     string
	timeout time.Duration
}

// NewClient returns a Client that invokes bin, defaulting to a 30s timeout.
func NewClient(bin string, timeout time.Duration) *Client {
	if bin == "" {
		bin = "herdr"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{bin: bin, timeout: timeout}
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stdoutBytes := stdout.Bytes()

	// herdr writes its structured error envelope to STDERR, not stdout --
	// verified against herdr 0.8.2:
	//   $ herdr agent get nope 2>/dev/null        # (nothing)
	//   $ herdr agent get nope 2>&1 1>/dev/null
	//   {"error":{"code":"agent_not_found",...},"id":"cli:agent:get"}
	// Checking stdout alone left ErrAgentBlocked and ErrPromptStalled
	// permanently unreachable. Check stdout first anyway: `agent read`
	// returns raw transcript text there and must never be misread as an
	// envelope, and a raw transcript will not decode as one.
	if code, msg, ok := decodeErrorEnvelope(stdoutBytes, stderr.Bytes()); ok {
		switch code {
		case "agent_blocked":
			return nil, ErrAgentBlocked
		case "agent_prompt_stalled":
			return nil, ErrPromptStalled
		case "timeout":
			return nil, ErrWaitTimeout
		default:
			if err != nil {
				return nil, fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), msg, err)
			}
			return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), msg)
		}
	}

	// If no error envelope and command failed, return exec error.
	if err != nil {
		combined := stdout.String() + stderr.String()
		return nil, fmt.Errorf("herdr %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(combined), err)
	}

	return stdoutBytes, nil
}

// decodeErrorEnvelope looks for herdr's `{"error":{"code","message"}}` envelope
// in the given streams, in order, returning the first one that carries a code.
func decodeErrorEnvelope(streams ...[]byte) (code, message string, ok bool) {
	type envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	for _, raw := range streams {
		if len(raw) == 0 {
			continue
		}

		var env envelope
		if json.Unmarshal(raw, &env) != nil || env.Error.Code == "" {
			continue
		}

		msg := env.Error.Message
		if msg == "" {
			msg = env.Error.Code
		}

		return env.Error.Code, msg, true
	}

	return "", "", false
}

// ListAgents returns every agent-occupied pane in the live herdr session.
func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	raw, err := c.run(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	return ParseAgentList(raw)
}

// promptLandedTimeout bounds the wait for evidence that a prompt landed. It is
// herdr's own stall window: herdr reports agent_prompt_stalled when a prompt
// sent from a non-working state produces no lifecycle change within five
// seconds.
const promptLandedTimeout = 5 * time.Second

// Prompt submits text to an agent and presses enter. It returns
// ErrAgentBlocked without sending anything if the agent is at a dialog, and
// ErrPromptStalled if the prompt produced no lifecycle change at all.
//
// --wait is not optional here, and not an optimisation. herdr only runs its
// "this prompt changed nothing" check on the wait path, so without it a prompt
// typed into a harness that has not finished taking over the terminal is lost
// and herdr still reports success. relay then logs a delivery that never
// happened, and thirty seconds later the reconciler nudges a builder that was
// never given a plan. That is #31, and it made ErrPromptStalled -- and the
// retry in promptWithRetry that consumes it -- unreachable code.
//
// --until names working and blocked rather than a settled state on purpose.
// The question is whether the prompt landed, not whether the turn finished;
// waiting for idle or done would block a send for the builder's whole round.
// The cost is that a prompt which lands but is never observed as working
// within the window surfaces as an error instead of silence. That is the right
// trade: a loud false alarm is recoverable, a silent lost round is not.
func (c *Client) Prompt(ctx context.Context, target, text string) error {
	_, err := c.run(ctx, "agent", "prompt", target, text,
		"--wait", "--until", "working", "--until", "blocked",
		"--timeout", strconv.FormatInt(promptLandedTimeout.Milliseconds(), 10))

	// herdr distinguishes "I judged this prompt inert" (agent_prompt_stalled)
	// from "the state never arrived" (timeout). For a caller deciding whether
	// to re-send, they are the same evidence: nothing landed. Collapse them so
	// promptWithRetry gets its one retry either way, which is what makes a lost
	// prompt self-heal instead of failing the round.
	if errors.Is(err, ErrWaitTimeout) {
		return ErrPromptStalled
	}
	return err
}

// SendKeys writes logical key presses, the only way to answer a blocked agent.
func (c *Client) SendKeys(ctx context.Context, target, keys string) error {
	_, err := c.run(ctx, "agent", "send-keys", target, keys)
	return err
}

// ReadAgent snapshots recent terminal output with soft wraps joined. Alternate
// screen rows are unrecoverable through this source, so callers must treat it
// as best effort -- a dialog drawn there needs ReadAgentSource("detection").
func (c *Client) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	return c.ReadAgentSource(ctx, target, "recent-unwrapped", lines)
}

// ReadAgentSource snapshots terminal output from a named herdr read source.
// The source decides what is visible: recent-unwrapped is scrollback, while
// detection is what herdr itself matched against, which is the only place a
// full-screen approval dialog can be read from.
func (c *Client) ReadAgentSource(ctx context.Context, target, source string, lines int) (string, error) {
	raw, err := c.run(ctx, "agent", "read", target,
		"--source", source, "--lines", strconv.Itoa(lines), "--format", "text")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ClosePane closes a pane. relay calls this from exactly one place, `relay
// reap`, and only for a consult pane relay spawned itself.
func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	_, err := c.run(ctx, "pane", "close", paneID)
	return err
}

type tabEnvelope struct {
	Result struct {
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	} `json:"result"`
}

// CreateTab opens a tab and returns its root pane, leaving focus where it is.
// Every agent relay spawns lives in its own tab (#79).
func (c *Client) CreateTab(ctx context.Context, workspaceID, cwd, label string) (string, error) {
	args := []string{"tab", "create", "--cwd", cwd, "--no-focus"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	if label != "" {
		args = append(args, "--label", label)
	}

	raw, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}

	var env tabEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode tab create: %w", err)
	}
	if env.Result.RootPane.PaneID == "" {
		return "", errors.New("tab create returned no root pane id")
	}

	return env.Result.RootPane.PaneID, nil
}

// StartAgent launches a supported agent kind in an existing shell pane. Native
// agent arguments are passed after a bare --, as herdr requires.
func (c *Client) StartAgent(ctx context.Context, name, kind, paneID string, args []string) error {
	argv := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if len(args) > 0 {
		argv = append(argv, "--")
		argv = append(argv, args...)
	}

	_, err := c.run(ctx, argv...)
	return err
}

// Notify shows a toast: `herdr notification show <title> [--body <body>] --sound <sound>`.
// --body is omitted when body == ""; an empty sound is sent as SoundNone.
func (c *Client) Notify(ctx context.Context, title, body string, sound Sound) error {
	if sound == "" {
		sound = SoundNone
	}
	args := []string{"notification", "show", title}
	if body != "" {
		args = append(args, "--body", body)
	}
	args = append(args, "--sound", string(sound))
	_, err := c.run(ctx, args...)
	return err
}

// ReportMetadata runs `herdr pane report-metadata <paneID> --source relay
// [--token k=v ...] [--clear-token k ...] --seq <n>`. Tokens are emitted in
// sorted key order so the argv is deterministic; a value longer than
// maxTokenValue is truncated (rune-safe). A call with no tokens and no
// clears is a no-op that returns nil without running herdr.
func (c *Client) ReportMetadata(ctx context.Context, paneID string, m PaneMetadata) error {
	if len(m.Tokens) == 0 && len(m.ClearTokens) == 0 {
		return nil
	}

	args := []string{"pane", "report-metadata", paneID, "--source", MetadataSource}

	names := make([]string, 0, len(m.Tokens))
	for name := range m.Tokens {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := m.Tokens[name]
		if r := []rune(value); len(r) > maxTokenValue {
			value = string(r[:maxTokenValue])
		}
		args = append(args, "--token", name+"="+value)
	}
	for _, name := range m.ClearTokens {
		args = append(args, "--clear-token", name)
	}
	args = append(args, "--seq", strconv.FormatInt(m.Seq, 10))

	_, err := c.run(ctx, args...)
	return err
}

// Version runs `herdr --version` and returns the bare semver.
func (c *Client) Version(ctx context.Context) (string, error) {
	raw, err := c.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	return parseVersion(string(raw))
}

func parseVersion(output string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	ver := fields[len(fields)-1]
	ver = strings.TrimPrefix(ver, "v")
	if ver == "" {
		return "", errors.New("empty version string")
	}
	return ver, nil
}

// IntegrationStatus runs `herdr integration status` and parses its lines.
func (c *Client) IntegrationStatus(ctx context.Context) (map[string]IntegrationState, error) {
	raw, err := c.run(ctx, "integration", "status")
	if err != nil {
		return nil, err
	}
	return ParseIntegrationStatus(raw), nil
}

// ParseIntegrationStatus parses the lines from `herdr integration status`.
// An unparseable line is skipped, not fatal.
func ParseIntegrationStatus(raw []byte) map[string]IntegrationState {
	res := make(map[string]IntegrationState)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) != 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		rest := strings.TrimSpace(parts[1])
		if target == "" || rest == "" {
			continue
		}

		lastOpen := strings.LastIndex(rest, "(")
		lastClose := strings.LastIndex(rest, ")")
		if lastOpen == -1 || lastClose == -1 || lastClose <= lastOpen {
			continue
		}

		path := strings.TrimSpace(rest[lastOpen+1 : lastClose])
		if path == "" {
			continue
		}

		state := strings.TrimSpace(rest[:lastOpen])
		if state == "" {
			continue
		}

		var st IntegrationState
		st.Detail = state
		if strings.HasPrefix(state, "not installed") {
			st.Installed = false
			st.Outdated = false
		} else if strings.HasPrefix(state, "outdated") {
			st.Installed = true
			st.Outdated = true
		} else {
			st.Installed = true
			st.Outdated = false
		}
		res[target] = st
	}
	return res
}
