package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
)

// webhookTimeout bounds one webhook POST: long enough for a normal endpoint,
// short enough that a dead one never holds a goroutine open.
const webhookTimeout = 5 * time.Second

// WebhookSink posts matching lifecycle events to the URLs configured in
// policy.json's notify.webhooks (#4). Dispatch fires a detached goroutine per
// matching webhook and returns immediately: a webhook is best-effort, like a
// hook script -- it never blocks a tick and never fails the caller. Delivery
// failures are logged, not raised.
type WebhookSink struct {
	Client *http.Client     // nil -> a client with a 5s timeout
	Hooks  []policy.Webhook // the webhooks to notify
	// Log is the plain-text sink a failure line is written to when Runs is
	// nil (a caller with no database, and the tests): nil -> discard.
	Log io.Writer
	// Runs is the run log a delivery failure is appended to (P3b round 2
	// §4.4): the production sink. nil -> Log is used instead.
	Runs RunLog
}

var _ Dispatcher = (*WebhookSink)(nil)

// Dispatch fires a POST for every configured webhook whose Events filter
// matches ev, each in its own goroutine. It returns immediately without
// waiting for any post to complete.
func (s *WebhookSink) Dispatch(_ context.Context, ev Event) {
	if s == nil {
		return
	}
	for _, h := range s.Hooks {
		if !matches(h, ev) {
			continue
		}
		go s.post(h, ev)
	}
}

// matches reports whether ev passes h's Events filter. An empty filter
// matches every event. "state_changed:<state>" matches a state_changed event
// whose State equals <state>; every other entry matches on event type alone.
func matches(h policy.Webhook, ev Event) bool {
	if len(h.Events) == 0 {
		return true
	}
	for _, entry := range h.Events {
		if entry == string(ev.Type) {
			return true
		}
		if entry == string(ev.Type)+":"+ev.State {
			return true
		}
	}
	return false
}

// jsonPayload is the "json" format body: the event plus its rendered text.
type jsonPayload struct {
	Type      string    `json:"type"`
	Binding   string    `json:"binding"`
	State     string    `json:"state"`
	OldState  string    `json:"old_state"`
	Round     int       `json:"round"`
	Timestamp time.Time `json:"timestamp"`
	Text      string    `json:"text"`
}

// humanEvent renders ev's type (and, for state_changed, its State) as the
// human-readable word(s) used in the notification text: "NEEDS YOU" for a
// state_changed event with State "needs_you", "builder stalled" for a
// builder_stalled event, and so on.
func humanEvent(ev Event) string {
	if ev.Type == EventStateChanged {
		return strings.ToUpper(strings.ReplaceAll(ev.State, "_", " "))
	}
	return strings.ReplaceAll(string(ev.Type), "_", " ")
}

// text renders ev as the one-line notification text shared by every format,
// e.g. "relevo: api-auth NEEDS YOU round 4 (was active)" or
// "relevo: api-auth builder stalled round 3".
func text(ev Event) string {
	stateSuffix := ""
	if ev.Type == EventStateChanged && ev.OldState != "" {
		stateSuffix = fmt.Sprintf(" (was %s)", ev.OldState)
	}
	return fmt.Sprintf("relevo: %s %s round %d%s", ev.BindingID, humanEvent(ev), ev.Round, stateSuffix)
}

// payload renders ev as h.Format's POST body and its content type.
func payload(h policy.Webhook, ev Event) ([]byte, string) {
	msg := text(ev)

	switch h.Format {
	case "slack":
		b, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: msg})
		return b, "application/json"
	case "discord":
		b, _ := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: msg})
		return b, "application/json"
	default: // "json" or ""
		b, _ := json.Marshal(jsonPayload{
			Type:      string(ev.Type),
			Binding:   ev.BindingID,
			State:     ev.State,
			OldState:  ev.OldState,
			Round:     ev.Round,
			Timestamp: ev.Timestamp,
			Text:      msg,
		})
		return b, "application/json"
	}
}

// post sends one webhook POST with a 5s timeout. Failures -- a non-2xx
// status or a transport error -- are logged, never returned: the caller
// (Dispatch) has already moved on. The URL is never logged in full, only its
// host, since a webhook URL (e.g. a Slack incoming-webhook URL) is a secret.
func (s *WebhookSink) post(h policy.Webhook, ev Event) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}

	body, contentType := payload(h, ev)

	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		s.logFailure(h, ev, err.Error())
		return
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := client.Do(req)
	if err != nil {
		s.logFailure(h, ev, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailure(h, ev, fmt.Sprintf("%d", resp.StatusCode))
	}
}

// logFailure records one failed delivery: a HookRun whose event is the
// webhook event, whose argv is ["webhook", <host>] and whose error is the status
// or transport error (§4.4). With no run log it writes the old text line to
// Log instead (discarding it when Log is nil):
// "<ts> webhook <url-host> <event>: <status|err>".
func (s *WebhookSink) logFailure(h policy.Webhook, ev Event, detail string) {
	// A webhook URL is a bearer secret (its path or query often carries the
	// token), so only its host is ever recorded -- in the run log as in the
	// text line.
	host := h.URL
	if u, err := url.Parse(h.URL); err == nil && u.Host != "" {
		host = u.Host
	}

	if s.Runs != nil {
		_ = s.Runs.Append(HookRun{
			At:    time.Now().UTC(),
			Event: string(ev.Type),
			Argv:  []string{"webhook", host},
			Error: detail,
		})
		return
	}

	w := s.Log
	if w == nil {
		w = io.Discard
	}

	fmt.Fprintf(w, "%s webhook %s %s: %s\n", time.Now().UTC().Format(time.RFC3339), host, ev.Type, detail)
}
