package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

const (
	OpencodeRequestTimeout = 5 * time.Second
	OpencodeConfirmWindow  = 3 * time.Second
	OpencodeConfirmPoll    = 250 * time.Millisecond
	DefaultFallbackAfter   = 30 * time.Second
)

// OpencodeDeliverer is the PlannerDeliverer for opencode planners
// (docs/specs/2026-09-22-opencode-delivery-design.md): it POSTs the
// payload to opencode's own HTTP API and confirms delivery by reading the
// session back out of opencode's sqlite db, because a 2xx from the wrong
// server process is not evidence anything arrived.
type OpencodeDeliverer struct {
	Client        *http.Client // nil -> &http.Client{Timeout: OpencodeRequestTimeout}
	StateFiles    []string     // candidate service.json files, tried in order
	DBPath        string       // $XDG_DATA_HOME/opencode/opencode.db
	Exec          usage.Exec   // the sqlite3 shell-out; nil -> OutcomeNotMine
	Now           func() time.Time
	Alive         func(pid int) bool // nil -> the same rule the claim store uses
	FallbackAfter time.Duration      // zero -> DefaultFallbackAfter
}

// opencodeService is $XDG_CONFIG_HOME/opencode/service.json. Verified shape
// on opencode 2.0.12: {"id","version","url","pid","password"}.
type opencodeService struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

var opencodeSessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

func validSessionID(id string) bool {
	return opencodeSessionIDPattern.MatchString(id)
}

func (d *OpencodeDeliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *OpencodeDeliverer) alive(pid int) bool {
	if d.Alive != nil {
		return d.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

func (d *OpencodeDeliverer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: OpencodeRequestTimeout}
}

func (d *OpencodeDeliverer) fallbackAfter() time.Duration {
	if d.FallbackAfter > 0 {
		return d.FallbackAfter
	}
	return DefaultFallbackAfter
}

// Deliver implements PlannerDeliverer for opencode planners. See §4.1 of
// the design doc for the exact, ordered condition table this follows.
func (d *OpencodeDeliverer) Deliver(ctx context.Context, planner store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error) {
	if planner.Kind != "opencode" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil {
		return OutcomeNotMine, "no sqlite3", nil
	}
	if !validSessionID(planner.SessionID) {
		return OutcomeNotMine, "no opencode session id", nil
	}
	if !queuedAt.IsZero() && d.now().Sub(queuedAt) > d.fallbackAfter() {
		reason := fmt.Sprintf("opencode push gave up after %s", d.fallbackAfter())
		slog.Info("opencode push not confirmed; payload stays pending for the background wait", "session", planner.SessionID, "reason", reason)
		return OutcomeNotMine, reason, nil
	}

	var (
		svc   opencodeService
		found bool
	)
	for _, path := range d.StateFiles {
		s, err := readOpencodeService(path)
		if err == nil && s.URL != "" {
			svc = s
			found = true
			break
		}
	}
	if !found || !d.alive(svc.PID) {
		return OutcomeUnavailable, "opencode service not running", nil
	}
	if !loopbackOpencodeURL(svc.URL) {
		return OutcomeUnavailable, "opencode service url is not loopback", nil
	}

	origin := firstPayloadLine(payload)

	// Already there? A previous tick may have delivered and crashed before
	// confirming. Check first, so a retry never double-posts.
	alreadySeen, err := d.seen(ctx, planner.SessionID, origin)
	if err != nil {
		return OutcomeUnavailable, "sqlite3: " + firstErrorLine(err), nil
	}
	if alreadySeen {
		return OutcomeDelivered, "already present", nil
	}

	req, err := d.buildRequest(ctx, svc, planner.SessionID, payload)
	if err != nil {
		return OutcomeNotMine, "", fmt.Errorf("build opencode request: %w", err)
	}

	resp, err := d.client().Do(req)
	if err != nil {
		return OutcomeUnavailable, "post: " + firstErrorLine(err), nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OutcomeUnavailable, fmt.Sprintf("post: %d", resp.StatusCode), nil
	}

	// 200 means admitted, not delivered (§3.4): confirm by reading the
	// session back, polling briefly since the owning process's event bus
	// takes a moment to record the turn.
	deadline := time.Now().Add(OpencodeConfirmWindow)
	for {
		seen, err := d.seen(ctx, planner.SessionID, origin)
		if err != nil {
			return OutcomeUnavailable, "sqlite3: " + firstErrorLine(err), nil
		}
		if seen {
			slog.Info("opencode push delivered", "session", planner.SessionID)
			return OutcomeDelivered, "", nil
		}
		if !time.Now().Before(deadline) {
			return OutcomeUnavailable, "posted but not seen in the session", nil
		}
		select {
		case <-ctx.Done():
			return OutcomeUnavailable, "posted but not seen in the session", nil
		case <-time.After(OpencodeConfirmPoll):
		}
	}
}

// readOpencodeService reads and parses $XDG_CONFIG_HOME/opencode/service.json.
func readOpencodeService(path string) (opencodeService, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return opencodeService{}, err
	}
	var svc opencodeService
	if err := json.Unmarshal(raw, &svc); err != nil {
		return opencodeService{}, err
	}
	return svc, nil
}

// loopbackOpencodeURL reports whether raw is a http://127.0.0.1[:port] or
// http://localhost[:port] origin. relevo refuses anything else outright: it
// would be sending a planner's report, which can contain source, to
// whatever host the file names.
func loopbackOpencodeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}

// buildRequest builds the POST described in §3.3 of the design doc.
func (d *OpencodeDeliverer) buildRequest(ctx context.Context, svc opencodeService, sessionID, payload string) (*http.Request, error) {
	body, err := json.Marshal(struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}{Text: payload, Delivery: "queue", Resume: true})
	if err != nil {
		return nil, fmt.Errorf("encode opencode prompt: %w", err)
	}

	target := strings.TrimRight(svc.URL, "/") + "/api/session/" + sessionID + "/prompt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build opencode request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode", svc.Password)
	return req, nil
}

// firstPayloadLine returns payload's first line -- the origin marker
// OriginLine wrote and PushText preserves. It is not recomputed with
// OriginLine: Deliver does not have the entry's name/round/kind, and this
// is exactly what Queue actually wrote.
func firstPayloadLine(payload string) string {
	line, _, _ := strings.Cut(payload, "\n")
	return line
}

// firstErrorLine is the first line of err's message, so a multi-line
// sqlite3 or transport error does not blow up a one-line reason string.
func firstErrorLine(err error) string {
	first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return first
}

// seen reports whether origin already appears in sessionID's messages as a
// user text part (§3.4): the inbox table holds admitted work and is
// consumed, so a user text part is what proves the session actually took
// the turn. A failing sqlite3 is not seen and not a Go error -- the call
// site turns it into OutcomeUnavailable.
func (d *OpencodeDeliverer) seen(ctx context.Context, sessionID, origin string) (bool, error) {
	out, err := d.Exec.Run(ctx, "sqlite3", "-readonly", d.DBPath, opencodeConfirmQuery(sessionID, origin))
	if err != nil {
		return false, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	n, convErr := strconv.Atoi(first)
	if convErr != nil {
		return false, nil
	}
	return n > 0, nil
}

// opencodeConfirmQuery is the read-back that proves a session actually
// took the turn (§3.4). ' is doubled in both interpolated values, the
// SQL string-literal escape.
func opencodeConfirmQuery(sessionID, origin string) string {
	sid := strings.ReplaceAll(sessionID, "'", "''")
	org := strings.ReplaceAll(origin, "'", "''")
	return fmt.Sprintf(
		"select count(*) from part p join message m on m.id = p.message_id"+
			" where p.session_id = '%s'"+
			" and json_extract(m.data, '$.role') = 'user'"+
			" and json_extract(p.data, '$.type') = 'text'"+
			" and json_extract(p.data, '$.text') like '%%%s%%'",
		sid, org)
}
