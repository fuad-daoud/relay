package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/relay"
)

// serveOnce drives a fully-built Server over an in-memory pipe with one line
// per request, so a test can set Mode as well as Verbs.
func serveOnce(t *testing.T, srv *Server, requests []string) []byte {
	t.Helper()
	pr, pw := io.Pipe()
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, &out) }()

	for _, line := range requests {
		if _, err := pw.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out.Bytes()
}

// callSend drives one tools/call send and returns the tool result's text.
func callSend(t *testing.T, mode Mode, res any) string {
	t.Helper()
	verbs := &fakeVerbs{sendFn: func(context.Context, SendArgs) (any, error) { return res, nil }}
	srv := &Server{Verbs: verbs, Version: "test", Mode: mode}
	out := serveOnce(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"name":"webshop","file":"/tmp/plan.md"}}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("want one response line, got %d: %q", len(lines), string(out))
	}
	resp := decodeResponse(t, lines[0])
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	return result.Content[0].Text
}

// TestMCPToolsModeSendResultCarriesBackgroundWait is the plan's required case
// for #303 §4.5: in tools mode the send tool's result ends with the exact
// background-wait command, carrying this binding's name and its round budget.
func TestMCPToolsModeSendResultCarriesBackgroundWait(t *testing.T) {
	text := callSend(t, ModeTools, sendResult{SendResult: relay.SendResult{Round: 1}, WaitBudget: "24h0m0s"})

	want := "background wait (run with run_in_background, then end your turn):\n" +
		"  relay wait --name webshop --timeout 24h0m0s; relay pull --name webshop"
	if !strings.HasSuffix(text, want) {
		t.Fatalf("send result text = %q, want it to end with:\n%s", text, want)
	}
}

// TestMCPChannelModeSendResultHasNoWaitLine is the other half of §4.5: in
// channel mode the event arrives by itself, so the result carries no wait
// command -- a report must not arrive twice, once by channel and once by pull.
func TestMCPChannelModeSendResultHasNoWaitLine(t *testing.T) {
	text := callSend(t, ModeChannel, sendResult{SendResult: relay.SendResult{Round: 1}, WaitBudget: "24h0m0s"})

	if strings.Contains(text, "background wait") || strings.Contains(text, "relay wait") {
		t.Fatalf("channel-mode send result must carry no wait line, got %q", text)
	}
}

// TestMCPInstructionsDependOnMode is §4.5's mode-dependent instructions: the
// mode is known before initialize is answered, and the two texts say
// different things. The channel text keeps no broken/orphaned mention, and
// the tools text is where the background wait is explained.
func TestMCPInstructionsDependOnMode(t *testing.T) {
	channel := InstructionsFor(ModeChannel)
	tools := InstructionsFor(ModeTools)

	if channel == tools {
		t.Fatal("the two modes must be told different things")
	}
	for _, word := range []string{"broken", "orphaned"} {
		if strings.Contains(strings.ToLower(channel), word) {
			t.Errorf("channel instructions must not mention %q", word)
		}
	}
	if strings.Contains(channel, "background wait") {
		t.Error("channel instructions must not describe the background wait")
	}
	for _, want := range []string{"background wait", "relay pull", "WaitTimeout", "relay status --name"} {
		if !strings.Contains(tools, want) {
			t.Errorf("tools instructions must mention %q", want)
		}
	}
	if strings.Contains(tools, "this pane") || strings.Contains(channel, "this pane") {
		t.Error(`instructions must say "this planner", not "this pane"`)
	}

	// initialize serves the mode's text when no override is set.
	srv := &Server{Verbs: &fakeVerbs{}, Version: "test", Mode: ModeTools}
	res := srv.initializeResult()
	if res["instructions"] != tools {
		t.Error("initialize must serve the tools-mode text when Mode is ModeTools")
	}
}
