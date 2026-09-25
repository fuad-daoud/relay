package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
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
	if f.err != nil {
		return nil, f.err
	}
	if len(args) > 0 && strings.Contains(args[len(args)-1], "sqlite_master") {
		return []byte(`[{"name":"session_inbox"}]`), nil
	}
	f.calls++
	if len(args) > 0 {
		f.queries = append(f.queries, args[len(args)-1])
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
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	payload := "relevo: round 1 · to planner · about builder \"w\" (not the human)\n\nBuilder finished round 1. Report: /x/001-report.md"
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
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/001-report.md", time.Time{})
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
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/001-report.md", time.Time{})
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
			d := &OpencodeDeliverer{StateFiles: []string{stateFile}, Alive: aliveAlways}
			if !tc.noExec {
				d.Exec = &fakeSqliteExec{}
			}

			out, reason, err := d.Deliver(context.Background(), tc.planner, "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
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
		stateFiles       []string
		alive            func(int) bool
		exec             *fakeSqliteExec
		wantReasonPrefix string
	}{
		{name: "missing state file", stateFiles: []string{filepath.Join(t.TempDir(), "nope.json")}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "dead pid", stateFiles: []string{deadPidFile}, alive: aliveNever, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service not running"},
		{name: "non-loopback url", stateFiles: []string{nonLoopbackFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "opencode service url is not loopback"},
		{name: "500 from server", stateFiles: []string{errFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post: 500"},
		{name: "transport error", stateFiles: []string{transportErrFile}, alive: aliveAlways, exec: &fakeSqliteExec{}, wantReasonPrefix: "post:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &OpencodeDeliverer{StateFiles: tc.stateFiles, DBPath: filepath.Join(t.TempDir(), "opencode.db"), Exec: tc.exec, Alive: tc.alive}
			out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
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
		StateFiles: []string{stateFile}, // deliberately unusable (dead pid) -- would be OutcomeUnavailable before the FallbackAfter check
		DBPath:     filepath.Join(t.TempDir(), "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveNever,
		Now:        func() time.Time { return now },
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", queuedAt)
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

func TestOpencodeDeliverLogsGiveUpOncePerPayload(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	stateFile := writeOpencodeServiceFile(t, t.TempDir(), "http://127.0.0.1:1", "pw", 1)
	queuedAt := time.Unix(1000, 0)
	now := queuedAt.Add(31 * time.Second)

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(t.TempDir(), "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveNever,
		Now:        func() time.Time { return now },
	}

	payload := "relevo: round 1\n\nbody"
	endpoint := opencodePlanner("ses_abc123")

	// 1. call Deliver three times with the same payload and queuedAt, with now = queuedAt+31s, +32s and +33s
	for _, sec := range []time.Duration{31 * time.Second, 32 * time.Second, 33 * time.Second} {
		now = queuedAt.Add(sec)
		out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		// 2. assert that every call returned OutcomeNotMine with the reason containing opencode push gave up after
		if out != OutcomeNotMine {
			t.Fatalf("out = %v, want OutcomeNotMine", out)
		}
		if !strings.Contains(reason, "opencode push gave up after") {
			t.Errorf("reason = %q, want it to contain 'opencode push gave up after'", reason)
		}
	}

	countLogs := func() int {
		return strings.Count(logged.String(), "push not confirmed")
	}

	// 3. assert that the buffer contains push not confirmed exactly once
	if got := countLogs(); got != 1 {
		t.Fatalf("got %d 'push not confirmed' log lines, want 1; logs:\n%s", got, logged.String())
	}

	// 4. call once more with a different queuedAt (queuedAt+1s) and a now past its fallback, and assert the count is 2
	queuedAt2 := queuedAt.Add(time.Second)
	now = queuedAt2.Add(31 * time.Second)
	out, reason, err := d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt2)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine || !strings.Contains(reason, "opencode push gave up after") {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine with give up", out, reason)
	}
	if got := countLogs(); got != 2 {
		t.Fatalf("got %d 'push not confirmed' log lines after second queuedAt, want 2; logs:\n%s", got, logged.String())
	}

	// 5. set now one hour past the first call and call with the first payload again, and assert the count is 3
	now = queuedAt.Add(31*time.Second + time.Hour)
	out, reason, err = d.Deliver(context.Background(), endpoint, payload, "/x/r.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine || !strings.Contains(reason, "opencode push gave up after") {
		t.Fatalf("out = %v, reason = %q, want OutcomeNotMine with give up", out, reason)
	}
	if got := countLogs(); got != 3 {
		t.Fatalf("got %d 'push not confirmed' log lines after 1 hour, want 3; logs:\n%s", got, logged.String())
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
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       &fakeSqliteExec{},
		Alive:      aliveAlways,
	}

	out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
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

func TestOpencodeDeliverStateFilesPreference(t *testing.T) {
	var stateRequests, configRequests int
	stateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stateRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer stateSrv.Close()

	configSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		configRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer configSrv.Close()

	dir := t.TempDir()
	stateWithURL := writeOpencodeServiceFile(t, dir, stateSrv.URL, "pw", 1)
	pwDir := filepath.Join(dir, "pw-only")
	if err := os.MkdirAll(pwDir, 0o755); err != nil {
		t.Fatalf("mkdir pw-only: %v", err)
	}
	configPasswordOnly := writeOpencodeServiceFile(t, pwDir, "", "pw", 1)
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	configWithURL := writeOpencodeServiceFile(t, configDir, configSrv.URL, "pw", 1)
	missing := filepath.Join(dir, "nonexistent.json")

	t.Run("stateWithURL and configPasswordOnly uses state url", func(t *testing.T) {
		stateRequests, configRequests = 0, 0
		exec := &fakeSqliteExec{seenFrom: 2}
		d := &OpencodeDeliverer{
			StateFiles: []string{stateWithURL, configPasswordOnly},
			DBPath:     filepath.Join(dir, "opencode.db"),
			Exec:       exec,
			Alive:      aliveAlways,
		}
		out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeDelivered {
			t.Fatalf("out = %v, reason = %q, want OutcomeDelivered", out, reason)
		}
		if stateRequests != 1 || configRequests != 0 {
			t.Errorf("stateRequests = %d, configRequests = %d; want stateRequests = 1, configRequests = 0", stateRequests, configRequests)
		}
	})

	t.Run("missing and configWithURL uses config url", func(t *testing.T) {
		stateRequests, configRequests = 0, 0
		exec := &fakeSqliteExec{seenFrom: 2}
		d := &OpencodeDeliverer{
			StateFiles: []string{missing, configWithURL},
			DBPath:     filepath.Join(dir, "opencode.db"),
			Exec:       exec,
			Alive:      aliveAlways,
		}
		out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeDelivered {
			t.Fatalf("out = %v, reason = %q, want OutcomeDelivered", out, reason)
		}
		if stateRequests != 0 || configRequests != 1 {
			t.Errorf("stateRequests = %d, configRequests = %d; want stateRequests = 0, configRequests = 1", stateRequests, configRequests)
		}
	})

	t.Run("passwordOnly behaves as unusable file", func(t *testing.T) {
		d := &OpencodeDeliverer{
			StateFiles: []string{configPasswordOnly},
			DBPath:     filepath.Join(dir, "opencode.db"),
			Exec:       &fakeSqliteExec{},
			Alive:      aliveAlways,
		}
		out, reason, err := d.Deliver(context.Background(), opencodePlanner("ses_abc123"), "relevo: round 1\n\nbody", "/x/r.md", time.Time{})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeUnavailable {
			t.Fatalf("out = %v, want OutcomeUnavailable", out)
		}
		if reason != "opencode service not running" {
			t.Errorf("reason = %q, want %q", reason, "opencode service not running")
		}
	})
}

func basicAuth(user, pass string) string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

// TestOpencodeConfirmSeen covers the two shapes a delivered turn can take
// (#393): OpenCode 2.0.14's session_message row, and the pre-2.0 part/message
// pair. Only a user turn counts, and either table alone confirms.
func TestOpencodeConfirmSeen(t *testing.T) {
	const origin = "relevo: round 1 to builder"

	tables := []string{
		"create table message (id text, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
	}
	userText := `{"type":"text","text":"` + origin + `"}`
	v2UserText := `{"text":"` + origin + `"}`

	cases := []struct {
		name  string
		stmts []string
		want  bool
	}{
		{
			name: "session_inbox row whose payload carries the origin -> seen",
			stmts: append(append([]string{}, tables...),
				"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
				"insert into session_inbox values ('inbox1', 'ses_x', 'message', '{\"text\":\""+origin+"\"}', 'queue', 0, 1)"),
			want: true,
		},
		{
			name: "db with no session_inbox table still works",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'user', 0, 1, 1, '"+v2UserText+"')"),
			want: true,
		},
		{
			name: "session_message user row containing the origin -> seen",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'user', 0, 1, 1, '"+v2UserText+"')"),
			want: true,
		},
		{
			name: "legacy part/message user row containing the origin -> seen",
			stmts: append(append([]string{}, tables...),
				`insert into message values ('msg1', '{"role":"user"}')`,
				"insert into part values ('p1', 'msg1', 'ses_x', '"+userText+"')"),
			want: true,
		},
		{
			name: "assistant row containing the origin -> not seen",
			stmts: append(append([]string{}, tables...),
				"insert into session_message values ('m1', 'ses_x', 'assistant', 0, 1, 1, '"+v2UserText+"')",
				`insert into message values ('msg1', '{"role":"assistant"}')`,
				"insert into part values ('p1', 'msg1', 'ses_x', '"+userText+"')"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := sqliteFixture(t, tc.stmts...)
			d := &OpencodeDeliverer{DBPath: db, Exec: cliExec{}}

			got, err := d.seen(context.Background(), "ses_x", origin)
			if err != nil {
				t.Fatalf("seen: %v", err)
			}
			if got != tc.want {
				t.Errorf("seen = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpencodeDeliverPostsOnce(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)

	db := sqliteFixture(t,
		"create table message (id text, data text)",
		"create table part (id text, message_id text, session_id text, data text)",
		"create table session_message (id text, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)",
		"create table session_inbox (id text, session_id text, type text, payload text, delivery text, enqueued_seq integer, time_created integer)",
	)

	startTime := time.Unix(1000, 0)
	curTime := startTime
	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     db,
		Exec:       cliExec{},
		Alive:      aliveAlways,
		Now:        func() time.Time { return curTime },
	}

	payload1 := "relevo: round 1 · to planner · payload 1\n\nbody 1"
	plannerEp := opencodePlanner("ses_abc123")

	// Call Deliver for the same payload 5 times (advancing injected clock by 2s each, below FallbackAfter)
	for i := 1; i <= 5; i++ {
		out, reason, err := d.Deliver(context.Background(), plannerEp, payload1, "/x/001-report.md", startTime)
		if err != nil {
			t.Fatalf("call %d Deliver: %v", i, err)
		}
		if out != OutcomeUnavailable {
			t.Fatalf("call %d out = %v, want OutcomeUnavailable", i, out)
		}
		if reason != "posted but not seen in the session" {
			t.Fatalf("call %d reason = %q, want \"posted but not seen in the session\"", i, reason)
		}
		mu.Lock()
		gotPosts := posts
		mu.Unlock()
		if gotPosts != 1 {
			t.Fatalf("after call %d, got %d POSTs, want 1", i, gotPosts)
		}
		curTime = curTime.Add(2 * time.Second)
	}

	// Then make the row appear in session_inbox -> the next call reports delivered with no POST.
	out, err := exec.Command("sqlite3", db,
		"insert into session_inbox values ('inbox1', 'ses_abc123', 'message', '"+payload1+"', 'queue', 0, 100)").CombinedOutput()
	if err != nil {
		t.Fatalf("insert session_inbox: %v: %s", err, out)
	}

	out6, reason6, err := d.Deliver(context.Background(), plannerEp, payload1, "/x/001-report.md", startTime)
	if err != nil {
		t.Fatalf("call 6 Deliver: %v", err)
	}
	if out6 != OutcomeDelivered {
		t.Fatalf("call 6 out = %v, reason = %q, want OutcomeDelivered", out6, reason6)
	}
	mu.Lock()
	gotPosts := posts
	mu.Unlock()
	if gotPosts != 1 {
		t.Fatalf("after call 6, got %d POSTs, want 1", gotPosts)
	}

	// A second payload (different origin) still POSTs once on its own.
	payload2 := "relevo: round 1 · to planner · payload 2\n\nbody 2"
	curTime = curTime.Add(2 * time.Second)
	out7, reason7, err := d.Deliver(context.Background(), plannerEp, payload2, "/x/002-report.md", startTime)
	if err != nil {
		t.Fatalf("payload2 Deliver: %v", err)
	}
	if out7 != OutcomeUnavailable {
		t.Fatalf("payload2 out = %v, reason = %q, want OutcomeUnavailable", out7, reason7)
	}
	mu.Lock()
	gotPosts = posts
	mu.Unlock()
	if gotPosts != 2 {
		t.Fatalf("after payload 2, got %d POSTs, want 2", gotPosts)
	}
}
