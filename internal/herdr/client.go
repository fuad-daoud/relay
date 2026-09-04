package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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
	combined := stdout.String() + stderr.String()

	switch {
	case strings.Contains(combined, "agent_blocked"):
		return nil, ErrAgentBlocked
	case strings.Contains(combined, "agent_prompt_stalled"):
		return nil, ErrPromptStalled
	case err != nil:
		return nil, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(combined))
	}

	return stdout.Bytes(), nil
}

// ListAgents returns every agent-occupied pane in the live herdr session.
func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	raw, err := c.run(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	return ParseAgentList(raw)
}

// Prompt submits text to an agent and presses enter. It returns
// ErrAgentBlocked without sending anything if the agent is at a dialog.
func (c *Client) Prompt(ctx context.Context, target, text string) error {
	_, err := c.run(ctx, "agent", "prompt", target, text)
	return err
}

// SendKeys writes logical key presses, the only way to answer a blocked agent.
func (c *Client) SendKeys(ctx context.Context, target, keys string) error {
	_, err := c.run(ctx, "agent", "send-keys", target, keys)
	return err
}

// ReadAgent snapshots recent terminal output with soft wraps joined. Alternate
// screen rows are unrecoverable, so callers must treat this as best effort.
func (c *Client) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	raw, err := c.run(ctx, "agent", "read", target,
		"--source", "recent-unwrapped", "--lines", strconv.Itoa(lines), "--format", "text")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

type paneEnvelope struct {
	Result struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	} `json:"result"`
}

// SplitPane creates a sibling pane without moving the user's focus and returns
// its pane id.
func (c *Client) SplitPane(ctx context.Context, paneID, direction, cwd string) (string, error) {
	raw, err := c.run(ctx, "pane", "split", "--pane", paneID,
		"--direction", direction, "--cwd", cwd, "--no-focus")
	if err != nil {
		return "", err
	}

	var env paneEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode pane split: %w", err)
	}
	if env.Result.Pane.PaneID == "" {
		return "", errors.New("pane split returned no pane id")
	}

	return env.Result.Pane.PaneID, nil
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

// Notify surfaces a non-invasive nudge in the herdr UI.
func (c *Client) Notify(ctx context.Context, message string) error {
	_, err := c.run(ctx, "notification", "show", message)
	return err
}
