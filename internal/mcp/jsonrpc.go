// Package mcp implements relay mcp: a JSON-RPC 2.0 server over stdio that
// Claude Code spawns as an MCP server (docs/specs/2026-09-21-planner-channel-design.md).
// The wire protocol is hand-written on the standard library: the surface is
// five methods and one notification, not worth a dependency and its
// go.mod/supply-chain cost.
package mcp

import "encoding/json"

// Request is one JSON-RPC 2.0 request or notification (ID absent on a
// notification), one per line on the stdio transport.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC 2.0 response, written for every Request that
// carried an ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a server-to-client message with no ID and no reply, used
// here for notifications/claude/channel pushes.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface so an *RPCError can be returned and
// compared like any other Go error.
func (e *RPCError) Error() string { return e.Message }

// JSON-RPC 2.0 reserved error codes, plus this server's own use of
// CodeInvalidParams for tool argument decode and validation failures.
const (
	CodeParse         = -32700
	CodeInvalidReq    = -32600
	CodeMethodMissing = -32601
	CodeInvalidParams = -32602
	CodeInternal      = -32603
)
