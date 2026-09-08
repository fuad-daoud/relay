package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shellQuote returns a single-quoted shell string with interior quotes escaped.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stubHerdr writes an executable that echoes body on stdout and exits with code.
func stubHerdr(t *testing.T, body string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdr")
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(body) + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestClientListAgents(t *testing.T) {
	c := NewClient(stubHerdr(t, agentListFixture, 0), 5*time.Second)

	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
}

func TestClientPromptDetectsBlocked(t *testing.T) {
	c := NewClient(stubHerdr(t, `{"id":"x","error":{"code":"agent_blocked","message":"agent is blocked"}}`, 1), 5*time.Second)

	err := c.Prompt(context.Background(), "builder", "hello")
	if !errors.Is(err, ErrAgentBlocked) {
		t.Fatalf("got %v, want ErrAgentBlocked", err)
	}
}

func TestClientSplitPaneReturnsPaneID(t *testing.T) {
	c := NewClient(stubHerdr(t, `{"result":{"pane":{"pane_id":"w2:p9"}}}`, 0), 5*time.Second)

	id, err := c.SplitPane(context.Background(), "w2:p3", "right", "/tmp")
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if id != "w2:p9" {
		t.Fatalf("pane id = %q, want w2:p9", id)
	}
}

func TestClientReadAgentReturnsTextWithoutFalsePositive(t *testing.T) {
	// Stub returns exit 0 with body text containing "agent_blocked" but not
	// in error envelope format. ReadAgent should return this text successfully,
	// not error with ErrAgentBlocked.
	body := "some terminal text mentioning agent_blocked in output"
	c := NewClient(stubHerdr(t, body, 0), 5*time.Second)

	text, err := c.ReadAgent(context.Background(), "w2:p7", 10)
	if err != nil {
		t.Fatalf("ReadAgent: %v", err)
	}
	if text != body {
		t.Fatalf("got %q, want %q", text, body)
	}
}

func TestClientUnknownErrorCodeWithExitZero(t *testing.T) {
	// Stub returns exit 0 with an error envelope carrying an unknown code.
	// This tests the default branch when err == nil. The error message should
	// contain the error message text and not contain %!w or <nil>.
	body := `{"id":"x","error":{"code":"some_other_code","message":"something went wrong"}}`
	c := NewClient(stubHerdr(t, body, 0), 5*time.Second)

	err := c.Prompt(context.Background(), "target", "text")
	if err == nil {
		t.Fatal("want error, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "something went wrong") {
		t.Fatalf("error message missing expected text: %q", errMsg)
	}
	if strings.Contains(errMsg, "%!w") || strings.Contains(errMsg, "<nil>") {
		t.Fatalf("error message contains nil wrapping artifact: %q", errMsg)
	}
}

// stubHerdrStderr writes an executable that emits body on STDERR and exits
// with code -- the shape herdr 0.8.2 actually uses for its error envelope.
func stubHerdrStderr(t *testing.T, body string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdr")
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(body) + " >&2\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestClientDetectsBlockedOnStderr(t *testing.T) {
	body := `{"id":"x","error":{"code":"agent_blocked","message":"agent is blocked"}}`
	c := NewClient(stubHerdrStderr(t, body, 1), 5*time.Second)

	if err := c.Prompt(context.Background(), "builder", "hi"); !errors.Is(err, ErrAgentBlocked) {
		t.Fatalf("got %v, want ErrAgentBlocked -- herdr writes the envelope to stderr", err)
	}
}

func TestClientDetectsStallOnStderr(t *testing.T) {
	body := `{"id":"x","error":{"code":"agent_prompt_stalled","message":"no lifecycle change"}}`
	c := NewClient(stubHerdrStderr(t, body, 1), 5*time.Second)

	if err := c.Prompt(context.Background(), "builder", "hi"); !errors.Is(err, ErrPromptStalled) {
		t.Fatalf("got %v, want ErrPromptStalled", err)
	}
}

func TestClientStderrEnvelopeUnknownCodeKeepsMessage(t *testing.T) {
	body := `{"id":"x","error":{"code":"agent_not_found","message":"agent target nope not found"}}`
	c := NewClient(stubHerdrStderr(t, body, 1), 5*time.Second)

	_, err := c.ListAgents(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "agent target nope not found") {
		t.Errorf("error must carry herdr's message, got %v", err)
	}
}

func TestClientStdoutTranscriptIsNotMisreadAsEnvelope(t *testing.T) {
	// A real `agent read` transcript that merely mentions the code.
	c := NewClient(stubHerdr(t, "the agent said agent_blocked in passing", 0), 5*time.Second)

	out, err := c.ReadAgent(context.Background(), "builder", 20)
	if err != nil {
		t.Fatalf("a transcript must not be misread as an error: %v", err)
	}
	if !strings.Contains(out, "agent_blocked in passing") {
		t.Errorf("out = %q", out)
	}
}

// stubHerdrRecordingArgs records the argv it was invoked with, one argument per
// line, so a test can assert on the flags the client passes to herdr.
func stubHerdrRecordingArgs(t *testing.T, argsPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\nprintf '%s' '{\"result\":{}}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// Prompt must ask herdr to wait, because herdr only runs its five second
// "the prompt produced no lifecycle change" check on the wait path. Without
// --wait, a prompt typed into a harness that has not finished taking over the
// terminal is lost and herdr still reports success, so ErrPromptStalled is
// unreachable and promptWithRetry never fires. See #31.
func TestClientPromptWaitsSoAStalledPromptIsDetectable(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args")
	c := NewClient(stubHerdrRecordingArgs(t, argsPath), 5*time.Second)

	if err := c.Prompt(context.Background(), "builder", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")

	has := func(want string) bool {
		for _, a := range args {
			if a == want {
				return true
			}
		}
		return false
	}

	for _, want := range []string{"--wait", "--timeout"} {
		if !has(want) {
			t.Fatalf("Prompt argv %v is missing %s", args, want)
		}
	}

	// Waiting for a settled state would block for the builder's whole turn.
	// We only want to know the prompt landed, which is the transition out of
	// idle -- or straight to a dialog.
	if !has("working") || !has("blocked") {
		t.Fatalf("Prompt argv %v must wait --until working and blocked, not a settled state", args)
	}
}

// A wait that times out means herdr never saw the agent leave idle, which is
// the same evidence as a stall: nothing landed. Prompt must report it as
// ErrPromptStalled so promptWithRetry gets its one retry, rather than failing
// the round and making a human re-send by hand. See #31.
func TestClientPromptMapsWaitTimeoutToStalled(t *testing.T) {
	body := `{"error":{"code":"timeout","message":"timed out waiting for agent status"},"id":"cli:agent:prompt"}`
	c := NewClient(stubHerdrStderr(t, body, 1), 5*time.Second)

	if err := c.Prompt(context.Background(), "builder", "hi"); !errors.Is(err, ErrPromptStalled) {
		t.Fatalf("got %v, want ErrPromptStalled", err)
	}
}

// Other commands must not be told a timeout is a prompt stall.
func TestClientWaitTimeoutIsDistinctOutsidePrompt(t *testing.T) {
	body := `{"error":{"code":"timeout","message":"timed out waiting for agent status"},"id":"cli:agent:list"}`
	c := NewClient(stubHerdrStderr(t, body, 1), 5*time.Second)

	_, err := c.ListAgents(context.Background())
	if errors.Is(err, ErrPromptStalled) {
		t.Fatal("a timeout outside Prompt must not be reported as a prompt stall")
	}
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("got %v, want ErrWaitTimeout", err)
	}
}
