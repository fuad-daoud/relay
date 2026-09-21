package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// dialTimeout bounds connecting to herdr's socket.
const dialTimeout = 2 * time.Second

// ackTimeout bounds waiting for the subscribe acknowledgement.
const ackTimeout = 5 * time.Second

// maxEventLine bounds one pushed event line the socket reader will accept.
const maxEventLine = 1 << 20 // 1 MiB

// Event is one pushed herdr socket event, normalised from the wire shape
// documented in docs/plans/2026-09-21-w2j3-socket-events.md.
type Event struct {
	// Kind is the event's type with any '.' normalised to '_'
	// ("pane_agent_status_changed", "pane_exited", "pane_closed",
	// "pane_agent_detected", or another kind passed through unrecognised).
	Kind        string
	PaneID      string
	WorkspaceID string
	// AgentStatus is set for a status-change event; "" otherwise.
	AgentStatus string
	// Agent is the harness name, when herdr sends one.
	Agent string
}

// SocketPath returns where herdr's session socket lives: HERDR_SOCKET_PATH
// when set, else ~/.config/herdr/herdr.sock. It never fails: a home
// directory lookup error yields "", which Subscribe's dial then reports as
// ErrNoSocket like any other unreachable path.
func SocketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// subscribeSeq numbers every Subscribe call's request id ("relay-sub-<n>"),
// package-wide: uniqueness across Clients is all herdr needs, and a shared
// counter is simpler than threading one through Client's otherwise stateless
// value.
var subscribeSeq int64

type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id,omitempty"`
}

type subscribeParams struct {
	Subscriptions []subscription `json:"subscriptions"`
}

type subscribeRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params subscribeParams `json:"params"`
}

// buildSubscribeRequest composes the events.subscribe request: one
// pane.agent_status_changed filter per pane id, plus the three session-wide
// kinds every subscription always wants.
func buildSubscribeRequest(id string, paneIDs []string) subscribeRequest {
	subs := make([]subscription, 0, len(paneIDs)+3)
	for _, p := range paneIDs {
		subs = append(subs, subscription{Type: "pane.agent_status_changed", PaneID: p})
	}
	subs = append(subs,
		subscription{Type: "pane.exited"},
		subscription{Type: "pane.closed"},
		subscription{Type: "pane.agent_detected"},
	)
	return subscribeRequest{
		ID:     id,
		Method: "events.subscribe",
		Params: subscribeParams{Subscriptions: subs},
	}
}

// ackEnvelope is herdr's reply to events.subscribe: a success_response whose
// result.type is "subscription_started" or "ok", or an error_response
// carrying error.message.
type ackEnvelope struct {
	Type   string `json:"type"`
	Result struct {
		Type string `json:"type"`
	} `json:"result"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// checkAck decides whether a subscribe reply means the subscription is live.
func checkAck(line []byte) error {
	var ack ackEnvelope
	if err := json.Unmarshal(line, &ack); err != nil {
		return ErrNoSocket
	}
	switch ack.Type {
	case "success_response":
		if ack.Result.Type == "subscription_started" || ack.Result.Type == "ok" {
			return nil
		}
		return ErrNoSocket
	case "error_response":
		return fmt.Errorf("%w: subscribe: %s", ErrNoSocket, ack.Error.Message)
	default:
		return ErrNoSocket
	}
}

// wireEvent is a pushed line's shape: {"event": "<kind>", "data": {...}}.
type wireEvent struct {
	Event string `json:"event"`
	Data  struct {
		PaneID      string `json:"pane_id"`
		WorkspaceID string `json:"workspace_id"`
		AgentStatus string `json:"agent_status"`
		Agent       string `json:"agent"`
	} `json:"data"`
}

// parseEvent decodes one pushed line into an Event. A non-JSON line or one
// with no "event" field is not a protocol violation worth failing the
// stream over -- ok is false and the caller skips it.
func parseEvent(line []byte) (Event, bool) {
	var we wireEvent
	if err := json.Unmarshal(line, &we); err != nil || we.Event == "" {
		return Event{}, false
	}
	return Event{
		Kind:        strings.ReplaceAll(we.Event, ".", "_"),
		PaneID:      we.Data.PaneID,
		WorkspaceID: we.Data.WorkspaceID,
		AgentStatus: we.Data.AgentStatus,
		Agent:       we.Data.Agent,
	}, true
}

// Subscribe opens herdr's event stream, filtered to a pane.agent_status_changed
// subscription per pane in paneIDs plus the three session-wide kinds
// (pane.exited, pane.closed, pane.agent_detected). The returned channel is
// closed when ctx is done or the connection drops; a malformed line is
// skipped rather than failing the stream.
//
// The CLI client is otherwise untouched: ListAgents stays the snapshot
// source, and Subscribe never runs the herdr binary.
func (c *Client) Subscribe(ctx context.Context, paneIDs []string) (<-chan Event, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
	}

	id := fmt.Sprintf("relay-sub-%d", atomic.AddInt64(&subscribeSeq, 1))
	req := buildSubscribeRequest(id, paneIDs)
	body, err := json.Marshal(req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: encode subscribe request: %v", ErrNoSocket, err)
	}

	if err := conn.SetWriteDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: write subscribe request: %v", ErrNoSocket, err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)

	if err := conn.SetReadDeadline(time.Now().Add(ackTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
	}
	if !scanner.Scan() {
		conn.Close()
		if serr := scanner.Err(); serr != nil {
			return nil, fmt.Errorf("%w: read ack: %v", ErrNoSocket, serr)
		}
		return nil, fmt.Errorf("%w: connection closed before ack", ErrNoSocket)
	}
	if err := checkAck(scanner.Bytes()); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
	}

	ch := make(chan Event)
	go streamEvents(ctx, conn, scanner, ch)
	return ch, nil
}

// streamEvents pushes decoded events onto ch until ctx is done or the
// connection ends, then closes ch. A separate goroutine watches ctx so a
// blocked Scan() unblocks on cancellation: closing conn is what makes that
// happen, since bufio.Scanner has no context awareness of its own.
func streamEvents(ctx context.Context, conn net.Conn, scanner *bufio.Scanner, ch chan<- Event) {
	defer close(ch)
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	for scanner.Scan() {
		ev, ok := parseEvent(scanner.Bytes())
		if !ok {
			continue
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}
}
