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
	c := NewClient(stubHerdr(t, `{"error":{"code":"agent_blocked"}}`, 1), 5*time.Second)

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
