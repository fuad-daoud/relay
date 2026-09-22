package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// fakeSqliteExec is the fake usage.Exec deliver_opencode_test.go controls
// directly: it records every query and answers seen()'s "select count(*)"
// with a count that flips from 0 to 1 once callNumber reaches seenFrom (0
// means never). err, when set, is returned as a Run failure instead.
type fakeSqliteExec struct {
	queries  []string
	calls    int
	seenFrom int
	err      error
}

func (f *fakeSqliteExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	f.calls++
	if len(args) > 0 {
		f.queries = append(f.queries, args[len(args)-1])
	}
	if f.err != nil {
		return nil, f.err
	}
	n := 0
	if f.seenFrom > 0 && f.calls >= f.seenFrom {
		n = 1
	}
	return []byte(strconv.Itoa(n)), nil
}

func writeOpencodeServiceFile(t *testing.T, dir, url, password string, pid int) string {
	t.Helper()
	p := filepath.Join(dir, "service.json")
	data, err := json.Marshal(opencodeService{URL: url, Password: password, PID: pid, Version: "2.0.12"})
	if err != nil {
		t.Fatalf("marshal service.json: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write service.json: %v", err)
	}
	return p
}

func opencodePlanner(sessionID string) store.Endpoint {
	return store.Endpoint{Kind: "opencode", SessionID: sessionID, PaneID: "w2:p3"}
}

// aliveAlways and aliveNever stand in for the pid-liveness check so tests
// never depend on a real process's pid.
func aliveAlways(int) bool { return true }
func aliveNever(int) bool  { return false }

func TestOpencodeDeliverHappyPath(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody []byte
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "the-password", 1)
	exec := &fakeSqliteExec{seenFrom: 2} // not seen pre-POST, seen on the first confirm poll

	d := &OpencodeDeliverer{
		StateFile: stateFile,
		DBPath:    filepath.Join(dir, "opencode.db"),
		Exec:      exec,
		Alive:     aliveAlways,
	}

	payload := "relay: round 1 · to planner · about builder \"w\" (not the human)\n\nBuilder finished round 1. Report: /x/001-report.md"
	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), payload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered {
		t.Fatalf("out = %v, reason = %q, want OutcomeDelivered", out, reason)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if gotPath != "/api/session/ses_abc123/prompt" {
		t.Errorf("path = %q, want /api/session/ses_abc123/prompt", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	wantAuth := "Basic " + basicAuth("opencode", "the-password")
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}

	var body struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.Text != payload {
		t.Errorf("body.Text = %q, want the payload verbatim", body.Text)
	}
	if body.Delivery != "queue" {
		t.Errorf("body.Delivery = %q, want %q", body.Delivery, "queue")
	}
	if !body.Resume {
		t.Error("body.Resume = false, want true")
	}
}

// TestOpencodeDeliverSilentTwoHundred is the test the design exists for
// (§3.4): a 2xx from the wrong server process must never be treated as
// delivery. The fake Exec always answers 0, so the origin is never seen.
func TestOpencodeDeliverSilentTwoHundred(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{} // seenFrom 0: never seen

	d := &OpencodeDeliverer{
		StateFile: stateFile,
		DBPath:    filepath.Join(dir, "opencode.db"),
		Exec:      exec,
		Alive:     aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relay: round 1\n\nbody", "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeUnavailable {
		t.Fatalf("out = %v, reason = %q, want OutcomeUnavailable", out, reason)
	}
	if reason != "posted but not seen in the session" {
		t.Errorf("reason = %q", reason)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly one POST", requests)
	}
}

// TestOpencodeDeliverIdempotentSkipsPost proves a retry never double-posts:
// when the origin is already in the db, Deliver confirms without touching
// the network at all.
func TestOpencodeDeliverIdempotentSkipsPost(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &fakeSqliteExec{seenFrom: 1} // already seen on the very first query

	d := &OpencodeDeliverer{
		StateFile: stateFile,
		DBPath:    filepath.Join(dir, "opencode.db"),
		Exec:      exec,
		Alive:     aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relay: round 1\n\nbody", "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("out = %v, reason = %q, want OutcomeDelivered/\"already present\"", out, reason)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want zero: idempotence must skip the POST entirely", requests)
	}
}

func TestOpencodeDeliverNotMineCases(t *testing.T) {
	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, "http://127.0.0.1:1", "pw", 1)

	cases := []struct {
		name    string
		planner store.Endpoint
		noExec  bool
		reason  string
	}{
		{name: "claude planner", planner: store.Endpoint{Kind: "claude", SessionID: "ses_abc123"}, reason: ""},
		{name: "empty session id", planner: store.Endpoint{Kind: "opencode", SessionID: ""}, reason: "no opencode session id"},
		{name: "malformed session id", planner: store.Endpoint{Kind: "opencode", SessionID: "ses_bad!id"}, reason: "no opencode session id"},
		{name: "nil Exec", planner: store.Endpoint{Kind: "opencode", SessionID: "ses_abc123"}, noExec: true, reason: "no sqlite3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFile: stateFile, Alive: aliveAlways}
			if !tc.noExec {
				d.Exec = &fakeSqliteExec{}
			}

			out, reason, err := d.Deliver(context.Background(), tc.planner, "relay: round 1\n\nbody", "/x/r.md", time.Time{})
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("out = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

func TestOpencodeDeliverUnavailableCases(t *testing.T) {
	goodDir := t.TempDir()
	deadPidFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:49999", "pw", 999999)
	nonLoopbackFile := writeOpencodeServiceFile(t, goodDir, "http://example.com:8080", "pw", 1)

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()
	errFile := writeOpencodeServiceFile(t, t.TempDir(), errSrv.URL, "pw", 1)

	transportErrFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)

	cases := []struct {
		name             string
		stateFile        string
		alive            func(int) bool
		exec             *fakeSqliteExec
		wantReasonPrefix string
	}{
		{name: "missing state file", stateFile: filepath.Join(t.TempDir(), "nope.json"), alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "dead pid", stateFile: deadPidFile, alive: aliveNever, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "non-loopback url", stateFile: nonLoopbackFile, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service url is not loopback"},
		{name: "500 from server", stateFile: errFile, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post: 500"},
		{name: "transport error", stateFile: transportErrFile, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFile: tc.stateFile, DBPath: filepath.Join(t.TempDir(), "opencode.db"), Exec: tc.exec, Alive: tc.alive}
			out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relay: round 1\n\nbody", "/x/r.md", time.Time{})
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if out != OutcomeUnavailable {
				t.Fatalf("out = %v, reason = %q, want OutcomeUnavailable", out, reason)
			}
			if !strings.HasPrefix(reason, tc.wantReasonPrefix) {
				t.Errorf("reason = %q, want prefix %q", reason, tc.wantReasonPrefix)
			}
		})
	}
}

// TestOpencodeDeliverFallsBackAfterFallbackAfter proves every OutcomeUnavailable
// condition becomes OutcomeNotMine once FallbackAfter has passed, so the pane path
// takes over rather than retrying forever.
func TestOpencodeDeliverFallsBackAfterFallbackAfter(t *testing.T) {
	stateFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)
	queuedAt := time.Unix(1000, 0)
	now := queuedAt.Add(31 * time.Second) // past the 30s default

	d := &OpencodeDeliverer{
		StateFile: stateFile, // deliberately unusable (dead pid) -- would be OutcomeUnavailable before the FallbackAfter check
		DBPath:    filepath.Join(t.TempDir(), "opencode.db"),
		Exec:      &fakeSqliteExec{},
		Alive:     aliveNever,
		Now:       func() time.Time { return now },
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relay: round 1\n\nbody", "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine", out, reason)
	}
	if !strings.Contains(reason, "opencode push gave up after") {
		t.Errorf("reason = %q, want it to name the fallback", reason)
	}
}

// TestOpencodeDeliverPasswordNeverLeaks runs several scenarios with a
// distinctive password and asserts it never appears in a returned reason
// or error.
func TestOpencodeDeliverPasswordNeverLeaks(t *testing.T) {
	const password = "sekrit-do-not-log-me"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, password, 1)

	d := &OpencodeDeliverer{
		StateFile: stateFile,
		DBPath:    filepath.Join(dir, "opencode.db"),
		Exec:      &fakeSqliteExec{},
		Alive:     aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relay: round 1\n\nbody", "/x/r.md", time.Time{})
	if out != OutcomeUnavailable {
		t.Fatalf("out = %v, want OutcomeUnavailable (500 from server)", out)
	}
	if strings.Contains(reason, password) {
		t.Errorf("reason leaks the password: %q", reason)
	}
	if err != nil && strings.Contains(err.Error(), password) {
		t.Errorf("error leaks the password: %v", err)
	}
}

func basicAuth(user, pass string) string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}
