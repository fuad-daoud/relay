package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir returns a fresh temp directory whose path is much shorter
// than t.TempDir()'s (which nests under the test name and can push a unix
// socket path past the 104-byte sun_path limit on macOS). It honours
// $TMPDIR like t.TempDir() does, and is cleaned up the same way.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// startFakeHerdrServer listens on a short-path unix socket and runs handle
// against the first connection it accepts, on its own goroutine. It returns
// the socket path.
func startFakeHerdrServer(t *testing.T, handle func(conn net.Conn)) string {
	t.Helper()
	sockPath := filepath.Join(shortTempDir(t), "s.sock")
	if len(sockPath) >= 100 {
		t.Fatalf("socket path %q is %d bytes, want < 100 (macOS sun_path is 104 bytes)", sockPath, len(sockPath))
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		handle(conn)
	}()
	return sockPath
}

func TestSubscribeSendsFiltersAndStreams(t *testing.T) {
	reqCh := make(chan subscribeRequest, 1)
	sockPath := startFakeHerdrServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)
		if !scanner.Scan() {
			t.Errorf("server: no request line")
			return
		}
		var req subscribeRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Errorf("server: decode request: %v", err)
			return
		}
		reqCh <- req

		conn.Write([]byte(`{"id":"` + req.ID + `","type":"success_response","result":{"type":"subscription_started"}}` + "\n"))
		conn.Write([]byte(`{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"blocked"}}` + "\n"))
		conn.Write([]byte(`{"event":"pane_agent_status_changed","data":{"pane_id":"w1:p2","agent_status":"blocked"}}` + "\n"))
		conn.Write([]byte(`{"event":"pane.closed","data":{"pane_id":"w1:p1"}}` + "\n"))
		conn.Write([]byte("not json\n"))
	})
	t.Setenv("HERDR_SOCKET_PATH", sockPath)

	c := NewClient("herdr", time.Second)
	ch, err := c.Subscribe(context.Background(), []string{"w1:p1", "w1:p2"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var req subscribeRequest
	select {
	case req = <-reqCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}

	if req.Method != "events.subscribe" {
		t.Errorf("method = %q, want events.subscribe", req.Method)
	}
	wantKinds := map[string]int{
		"pane.agent_status_changed": 0,
		"pane.exited":               0,
		"pane.closed":               0,
		"pane.agent_detected":       0,
	}
	panePresent := map[string]bool{}
	for _, s := range req.Params.Subscriptions {
		if _, ok := wantKinds[s.Type]; !ok {
			t.Errorf("unexpected subscription type %q", s.Type)
			continue
		}
		wantKinds[s.Type]++
		if s.Type == "pane.agent_status_changed" {
			panePresent[s.PaneID] = true
		}
	}
	if wantKinds["pane.agent_status_changed"] != 2 {
		t.Errorf("pane.agent_status_changed subscriptions = %d, want 2: %+v", wantKinds["pane.agent_status_changed"], req.Params.Subscriptions)
	}
	if !panePresent["w1:p1"] || !panePresent["w1:p2"] {
		t.Errorf("subscriptions missing a pane id, got %+v", req.Params.Subscriptions)
	}
	for _, kind := range []string{"pane.exited", "pane.closed", "pane.agent_detected"} {
		if wantKinds[kind] != 1 {
			t.Errorf("%s subscriptions = %d, want 1", kind, wantKinds[kind])
		}
	}

	var got []Event
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3 (garbage line skipped): %+v", len(got), got)
	}
	if got[0].Kind != "pane_agent_status_changed" || got[0].PaneID != "w1:p1" || got[0].AgentStatus != "blocked" {
		t.Errorf("event[0] = %+v, want dotted kind normalised to pane_agent_status_changed for w1:p1", got[0])
	}
	if got[1].Kind != "pane_agent_status_changed" || got[1].PaneID != "w1:p2" {
		t.Errorf("event[1] = %+v, want snake_case kind preserved for w1:p2", got[1])
	}
	if got[2].Kind != "pane_closed" || got[2].PaneID != "w1:p1" {
		t.Errorf("event[2] = %+v, want pane_closed for w1:p1", got[2])
	}
}

func TestSubscribeNoSocketIsErrNoSocket(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "missing.sock"))

	c := NewClient("herdr", time.Second)
	_, err := c.Subscribe(context.Background(), nil)
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want it to wrap ErrNoSocket", err)
	}
}

func TestSubscribeErrorReplyIsErrNoSocket(t *testing.T) {
	sockPath := startFakeHerdrServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)
		scanner.Scan() // consume the request
		conn.Write([]byte(`{"id":"relay-sub-1","type":"error_response","error":{"code":"unsupported","message":"no"}}` + "\n"))
	})
	t.Setenv("HERDR_SOCKET_PATH", sockPath)

	c := NewClient("herdr", time.Second)
	_, err := c.Subscribe(context.Background(), []string{"w1:p1"})
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want it to wrap ErrNoSocket", err)
	}
	if !strings.Contains(err.Error(), "no") {
		t.Errorf("err = %v, want herdr's own message included", err)
	}
}

func TestSocketPathPrecedence(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/custom-herdr.sock")
	if got := SocketPath(); got != "/tmp/custom-herdr.sock" {
		t.Errorf("SocketPath() = %q, want the env override", got)
	}

	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HOME", "/home/relay-test")
	want := filepath.Join("/home/relay-test", ".config", "herdr", "herdr.sock")
	if got := SocketPath(); got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}
