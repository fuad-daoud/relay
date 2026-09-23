package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"sync"
)

// ProtocolVersion is what initialize always answers, regardless of what the
// client offered: Claude Code refuses to register a channel that negotiates
// a newer version, and an older client accepts the one it knows (spec §6).
const ProtocolVersion = "2025-06-18"

// ServerName is this MCP server's name, and the "source" attribute Claude
// Code stamps on every pushed channel event.
const ServerName = "relay"

// Mode is whether this process claims the pane and pushes events, or only
// serves tools (spec §7: the delivery hole and its guard).
type Mode int

const (
	// ModeTools serves tools/list and tools/call; it writes no claim and
	// pushes nothing.
	ModeTools Mode = iota
	// ModeChannel additionally claims its pane and drains its mailbox.
	ModeChannel
)

// maxLineBytes bounds one JSON-RPC line: a report payload can be large.
const maxLineBytes = 16 << 20

// metaKeyPattern is what Claude Code accepts as a meta key: an identifier.
var metaKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Server is relay mcp's JSON-RPC 2.0 loop over stdio. It implements
// relay.Pusher via Push, so cmd/relay can hand it straight to relay.Drain.
type Server struct {
	Verbs   Verbs
	Version string // serverInfo.version
	// Mode picks the instructions text when Instructions is empty (#303
	// §4.5): channel mode hears events, tools mode gets reports from the
	// background wait's `relay pull`.
	Mode Mode
	// Instructions overrides the mode's text when non-empty. Tests use it;
	// cmd/relay leaves it empty so the mode decides.
	Instructions string
	Log          io.Writer // stderr; nil -> discard
	// OnInitialized is called once, after notifications/initialized. The
	// command wires the poll loop start here so nothing is pushed before
	// the client has acknowledged initialize.
	OnInitialized func()

	logger     *log.Logger
	loggerOnce sync.Once

	// out is the transport's outbound side, set once at the top of Serve.
	// The request loop (via writeResponse) and the poll goroutine (via
	// Push) both write to it, so every write is serialised by writeMu.
	out     io.Writer
	writeMu sync.Mutex
}

func (s *Server) log() *log.Logger {
	s.loggerOnce.Do(func() {
		w := s.Log
		if w == nil {
			w = io.Discard
		}
		s.logger = log.New(w, "relay mcp: ", log.LstdFlags)
	})
	return s.logger
}

// Serve reads one JSON object per line from in until EOF or ctx is done,
// dispatching each per the method table (spec §6), and writes responses to
// out. Once Serve has started, Push may be called concurrently from another
// goroutine (the poll loop) and writes safely to the same out.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out

	type lineResult struct {
		line []byte
		err  error
	}
	lines := make(chan lineResult)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- lineResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- lineResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case lr, ok := <-lines:
			if !ok {
				return nil
			}
			if lr.err != nil {
				return lr.err
			}
			if len(bytesTrimSpace(lr.line)) == 0 {
				continue
			}
			s.handleLine(ctx, lr.line)
		}
	}
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: nil, Error: &RPCError{Code: CodeParse, Message: err.Error()}})
		return
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		if len(req.ID) > 0 {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidReq, Message: "invalid request"}})
		}
		return
	}

	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: s.initializeResult()})
	case "notifications/initialized":
		if s.OnInitialized != nil {
			s.OnInitialized()
		}
	case "ping":
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		}
	case "tools/list":
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": Tools()}})
		}
	case "tools/call":
		s.handleToolsCall(ctx, req)
	default:
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeMethodMissing, Message: fmt.Sprintf("unknown method %q", req.Method)}})
		}
		// Any other notification: ignored.
	}
}

func (s *Server) initializeResult() map[string]any {
	instructions := s.Instructions
	if instructions == "" {
		instructions = InstructionsFor(s.Mode)
	}
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools":        map[string]any{},
			"experimental": map[string]any{"claude/channel": map[string]any{}},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": s.Version,
		},
		"instructions": instructions,
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, req Request) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}

	result, rpcErr := s.callTool(ctx, params.Name, params.Arguments)
	if rpcErr != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
		return
	}
	s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (ToolResult, *RPCError) {
	switch name {
	case "status":
		var a StatusArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Status(ctx, a)
		return toolResultFrom(res, err)

	case "send":
		var a SendArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateSendArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Send(ctx, a)
		out, rpcErr := toolResultFrom(res, err)
		if rpcErr != nil || err != nil || a.DryRun {
			return out, rpcErr
		}
		// #303 §4.5: in tools mode nothing is pushed, so the send result
		// ends with the background wait the model must start and end its
		// turn on. In channel mode the event arrives by itself and the
		// result carries no such line.
		if s.Mode == ModeTools {
			return appendWaitCommand(out, a.Name, budgetOf(res)), nil
		}
		return out, nil

	case "done":
		var a DoneArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateDoneArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Done(ctx, a)
		return toolResultFrom(res, err)

	default:
		return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// toolResultFrom turns a verb's (result, error) into a ToolResult: a verb
// error becomes isError:true with the message verbatim, never a JSON-RPC
// error, so the model reads it as data and can act (spec §6).
func toolResultFrom(res any, err error) (ToolResult, *RPCError) {
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	r, jerr := jsonResult(res)
	if jerr != nil {
		return ToolResult{}, &RPCError{Code: CodeInternal, Message: jerr.Error()}
	}
	return r, nil
}

func (s *Server) writeResponse(resp Response) {
	if resp.ID == nil {
		resp.ID = json.RawMessage("null")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		s.log().Printf("marshal response: %v", err)
		return
	}
	s.writeLine(raw)
}

func (s *Server) writeLine(raw []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(raw, '\n')); err != nil {
		s.log().Printf("write: %v", err)
	}
}

// Push implements relay.Pusher: it writes a notifications/claude/channel
// line to the transport Serve is using, dropping any meta key that is not a
// Claude-Code-legal identifier (with a log line -- Claude Code would drop it
// silently otherwise). It is safe to call concurrently with Serve's own
// request loop.
func (s *Server) Push(ctx context.Context, content string, meta map[string]string) error {
	clean := make(map[string]string, len(meta))
	for k, v := range meta {
		if metaKeyPattern.MatchString(k) {
			clean[k] = v
		} else {
			s.log().Printf("dropping non-identifier meta key %q", k)
		}
	}

	note := Notification{
		JSONRPC: "2.0",
		Method:  "notifications/claude/channel",
		Params:  map[string]any{"content": content, "meta": clean},
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}
