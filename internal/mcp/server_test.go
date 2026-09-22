package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// fakeVerbs is a Verbs whose four methods are swappable per test; a nil
// field returns (nil, nil).
type fakeVerbs struct {
	statusFn func(ctx context.Context, a StatusArgs) (any, error)
	sendFn   func(ctx context.Context, a SendArgs) (any, error)
	answerFn func(ctx context.Context, a AnswerArgs) (any, error)
	doneFn   func(ctx context.Context, a DoneArgs) (any, error)
}

func (f *fakeVerbs) Status(ctx context.Context, a StatusArgs) (any, error) {
	if f.statusFn == nil {
		return nil, nil
	}
	return f.statusFn(ctx, a)
}

func (f *fakeVerbs) Send(ctx context.Context, a SendArgs) (any, error) {
	if f.sendFn == nil {
		return nil, nil
	}
	return f.sendFn(ctx, a)
}

func (f *fakeVerbs) Answer(ctx context.Context, a AnswerArgs) (any, error) {
	if f.answerFn == nil {
		return nil, nil
	}
	return f.answerFn(ctx, a)
}

func (f *fakeVerbs) Done(ctx context.Context, a DoneArgs) (any, error) {
	if f.doneFn == nil {
		return nil, nil
	}
	return f.doneFn(ctx, a)
}

// runServer drives Serve over an in-memory pipe with one line per request,
// closing the input after writing them (EOF), and returns everything Serve
// wrote to out.
func runServer(t *testing.T, verbs Verbs, requests []string) []byte {
	t.Helper()
	pr, pw := io.Pipe()
	var out bytes.Buffer
	srv := &Server{Verbs: verbs, Version: "0.6.0-test"}

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

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func decodeResponse(t *testing.T, line []byte) Response {
	t.Helper()
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response %s: %v", line, err)
	}
	return resp
}

func TestServerInitializePinsProtocolVersionAndAdvertisesChannel(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1: %s", len(lines), out)
	}
	resp := decodeResponse(t, lines[0])
	if resp.Error != nil {
		t.Fatalf("initialize error = %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", resp.Result)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v (the client offered 2026-07-28)", result["protocolVersion"], ProtocolVersion)
	}

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", result["capabilities"])
	}
	exp, ok := caps["experimental"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.experimental = %#v", caps["experimental"])
	}
	if _, ok := exp["claude/channel"]; !ok {
		t.Errorf("capabilities.experimental must contain claude/channel, got %#v", exp)
	}
}

func TestServerInitializeSameVersionAnswersSame(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	result := resp.Result.(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v", result["protocolVersion"], ProtocolVersion)
	}
}

func TestServerToolsListHasFourToolsInOrderNoAdditionalProperties(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	result := resp.Result.(map[string]any)
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools = %#v, want exactly 4", result["tools"])
	}

	wantOrder := []string{"status", "send", "answer", "done"}
	for i, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tools[%d] = %#v, want an object", i, raw)
		}
		if tool["name"] != wantOrder[i] {
			t.Errorf("tools[%d].name = %v, want %v", i, tool["name"], wantOrder[i])
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("tools[%d].inputSchema = %#v", i, tool["inputSchema"])
		}
		if ap, ok := schema["additionalProperties"].(bool); !ok || ap {
			t.Errorf("tools[%d].inputSchema.additionalProperties = %#v, want false", i, schema["additionalProperties"])
		}
	}
}

func TestServerToolsCallAnswerRejectsTwoOfThree(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"answer","arguments":{"name":"judge","text":"ok","keys":"enter"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeInvalidParams)
	}
}

func TestServerToolsCallAnswerRejectsMissingName(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"answer","arguments":{"text":"ok"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeInvalidParams)
	}
}

func TestServerToolsCallDoneErrorBecomesIsError(t *testing.T) {
	verbs := &fakeVerbs{
		doneFn: func(ctx context.Context, a DoneArgs) (any, error) {
			return nil, errors.New("boom")
		},
	}
	out := runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"done","arguments":{"name":"judge"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("want no JSON-RPC error for a verb failure, got %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("result = %#v, want isError true", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one block", result["content"])
	}
	block := content[0].(map[string]any)
	if block["text"] != "boom" {
		t.Errorf("text = %v, want %q (the error verbatim)", block["text"], "boom")
	}
}

func TestServerToolsCallDoneSuccess(t *testing.T) {
	verbs := &fakeVerbs{
		doneFn: func(ctx context.Context, a DoneArgs) (any, error) {
			return map[string]any{"ok": true, "name": a.Name}, nil
		},
	}
	out := runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"done","arguments":{"name":"judge"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("result = %#v, want isError false/absent", result)
	}
}

func TestServerToolsCallUnknownTool(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeInvalidParams)
	}
}

func TestServerUnknownMethodWithIDIsMethodMissing(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","id":1,"method":"bogus"}`})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeMethodMissing {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeMethodMissing)
	}
}

func TestServerUnknownNotificationIsIgnored(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","method":"bogus"}`})
	if lines := splitLines(out); len(lines) != 0 {
		t.Fatalf("an unknown notification must produce no response, got %q", lines)
	}
}

func TestServerUnparseableLineIsParseError(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`not json`})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1", len(lines))
	}
	resp := decodeResponse(t, lines[0])
	if resp.Error == nil || resp.Error.Code != CodeParse {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeParse)
	}
	if string(resp.ID) != "null" {
		t.Errorf("id = %s, want null", resp.ID)
	}
}

func TestServerPingRepliesEmptyObject(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","id":1,"method":"ping"}`})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("ping error = %+v", resp.Error)
	}
}

func TestServerOnInitializedFiresAfterNotification(t *testing.T) {
	fired := false
	srv := &Server{Verbs: &fakeVerbs{}, OnInitialized: func() { fired = true }}

	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, &out) }()

	if _, err := pw.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	if !fired {
		t.Error("OnInitialized must fire after notifications/initialized")
	}
	if len(splitLines(out.Bytes())) != 0 {
		t.Error("notifications/initialized must produce no reply")
	}
}

func TestServerPushEmitsNotificationAndDropsBadKey(t *testing.T) {
	var out bytes.Buffer
	srv := &Server{Verbs: &fakeVerbs{}}
	srv.out = &out

	if err := srv.Push(context.Background(), "hello", map[string]string{"binding": "judge", "bad-key": "x"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var note struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &note); err != nil {
		t.Fatalf("unmarshal push: %v", err)
	}
	if note.Method != "notifications/claude/channel" {
		t.Errorf("method = %q, want notifications/claude/channel", note.Method)
	}
	if note.Params.Content != "hello" {
		t.Errorf("content = %q, want %q", note.Params.Content, "hello")
	}
	if _, ok := note.Params.Meta["bad-key"]; ok {
		t.Errorf("meta must drop the non-identifier key, got %+v", note.Params.Meta)
	}
	if note.Params.Meta["binding"] != "judge" {
		t.Errorf("meta must keep the identifier key, got %+v", note.Params.Meta)
	}
}
