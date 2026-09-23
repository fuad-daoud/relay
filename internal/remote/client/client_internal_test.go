// This file is package client (not client_test) on purpose: the retry backoff
// and the download deadline are the package-level knobs #373 §4.4 names, and
// the existing external test file cannot replace an unexported variable. It
// imports nothing from internal/relevo, so it adds no import cycle.
package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// testClient is a client pointed at ts, with no TLS pinning: an httptest
// server of these tests speaks plain HTTP.
func testClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return New(Servers{"zen": ServerEntry{URL: ts.URL, Insecure: true}}, kp, time.Now)
}

// injectSleep replaces the package's backoff sleep for one test, so a retried
// call does not wait out its real 1s, 2s, 4s. The returned function reports
// what was asked for.
func injectSleep(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var slept []time.Duration
	orig := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { sleep = orig })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), slept...)
	}
}

// TestGatewayStatusIsUnreachable pins #373 §4.4's 5xx-gateway mapping: a 502
// with a CDN's HTML body becomes an ErrUnreachable that names the status, and
// the body never reaches the caller as error text.
//
// Mutation: drop the isGatewayStatus arm from doRequest and this fails on the
// HTML body (and on errors.Is).
func TestGatewayStatusIsUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad gateway</body></html>"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	_, err := cl.WhoAmI(context.Background(), "zen")
	if err == nil {
		t.Fatal("WhoAmI against a 502 gateway succeeded")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want it to wrap ErrUnreachable", err)
	}
	if strings.Contains(err.Error(), "<html") {
		t.Fatalf("err = %q, want no HTML body in the error text", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %q, want it to name the status", err)
	}
}

// TestGatewayStatusRetriesThenSucceeds pins the retry's shape: a 503 twice,
// then a 200, is one successful GetBinding after three attempts, with the
// injected sleep recording exactly the 1s and 2s backoff.
func TestGatewayStatusRetriesThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<html>503</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api","round_state":"idle"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	slept := injectSleep(t)

	view, err := cl.GetBinding(context.Background(), "zen", "api")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if view.Name != "api" {
		t.Fatalf("view.Name = %q, want api", view.Name)
	}
	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	waits := slept()
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [1s 2s]", waits)
	}
}

// TestGatewayStatusExhaustsRetries pins the other end: four 502s make four
// attempts and the caller sees the ErrUnreachable of the last one.
func TestGatewayStatusExhaustsRetries(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	_, err := cl.WhoAmI(context.Background(), "zen")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != retryAttempts {
		t.Fatalf("attempts = %d, want %d", got, retryAttempts)
	}
}

// TestDoneIsNeverRetried pins #373 §4.4's not-idempotent list: a 502 on Done
// gets one attempt, never the four a retried call makes.
func TestDoneIsNeverRetried(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	err := cl.Done(context.Background(), "zen", "api")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("attempts = %d, want 1: Done is not idempotent and must not be retried", got)
	}
}

// TestStartRoundRetryReReadsTheSpooledBody pins #373 §4.4 for a retried
// StartRound: a 502 then a 200, and the second request carries exactly the
// plan and bundle bytes of the first.
//
// Mutation: drop the Seek(0, SeekStart) from StartRound's attempt and the
// second request's parts come out empty, failing the byte comparison.
func TestStartRoundRetryReReadsTheSpooledBody(t *testing.T) {
	type seen struct{ plan, bundle string }
	var mu sync.Mutex
	var reqs []seen
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := r.FormValue("plan")
		bundle := ""
		if f, _, err := r.FormFile("bundle"); err == nil {
			b, _ := io.ReadAll(f)
			bundle = string(b)
			_ = f.Close()
		}
		mu.Lock()
		reqs = append(reqs, seen{plan: plan, bundle: bundle})
		n := len(reqs)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>502</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api","round_state":"running"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	plan := []byte("# Round 1 plan\n")
	bundle := []byte("bundle-bytes-0123456789")
	view, err := cl.StartRound(context.Background(), "zen", "api", 1, plan, bytes.NewReader(bundle), "", "", nil, true)
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if view.Name != "api" {
		t.Fatalf("view.Name = %q, want api", view.Name)
	}

	mu.Lock()
	got := append([]seen(nil), reqs...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2 (one 502, one retry)", len(got))
	}
	if got[1].plan != string(plan) {
		t.Fatalf("retry plan = %q, want %q", got[1].plan, string(plan))
	}
	if got[1].bundle != string(bundle) {
		t.Fatalf("retry bundle = %q, want %q", got[1].bundle, string(bundle))
	}
}

// TestStartRoundWithoutRetryIsOneAttempt is the other half of the flag: a
// caller whose server never advertised idempotent_send gets one attempt.
func TestStartRoundWithoutRetryIsOneAttempt(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	injectSleep(t)

	_, err := cl.StartRound(context.Background(), "zen", "api", 1, []byte("# Plan"), nil, "", "", nil, false)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("attempts = %d, want 1 without the retry option", got)
	}
}

// TestClientVersionHeader pins #373 §4.4's informational header: every request
// carries client.Version, whatever the method.
func TestClientVersionHeader(t *testing.T) {
	var mu sync.Mutex
	var heads []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heads = append(heads, r.Header.Get(remote.HeaderClientVersion))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api"}`))
	}))
	defer ts.Close()
	cl := testClient(t, ts)

	old := Version
	Version = "v9.9.9-test"
	t.Cleanup(func() { Version = old })

	ctx := context.Background()
	if _, err := cl.WhoAmI(ctx, "zen"); err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if _, err := cl.GetBinding(ctx, "zen", "api"); err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if _, err := cl.Ack(ctx, "zen", "api", 1); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), heads...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	for i, h := range got {
		if h != "v9.9.9-test" {
			t.Fatalf("request %d header %s = %q, want the client's version on every request", i, remote.HeaderClientVersion, h)
		}
	}
}

// TestRoundFileDeadline pins #373 §4.4's new per-call deadline on the two
// downloads that had none: with a test-shortened deadline, a server that
// answers slower than it fails the call instead of blocking, and the failure
// is the retryable ErrUnreachable.
//
// Mutation: drop the WithTimeout from RoundFile and this call waits out the
// server's sleep and succeeds.
func TestRoundFileDeadline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("report\n"))
	}))
	defer ts.Close()
	cl := testClient(t, ts)
	slept := injectSleep(t)

	old := roundFileDeadline
	roundFileDeadline = 50 * time.Millisecond
	t.Cleanup(func() { roundFileDeadline = old })

	start := time.Now()
	rc, err := cl.RoundFile(context.Background(), "zen", "api", 1, "report")
	elapsed := time.Since(start)
	if rc != nil {
		_ = rc.Close()
	}
	if err == nil {
		t.Fatal("RoundFile against a server slower than the deadline succeeded")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want it to wrap ErrUnreachable", err)
	}
	if got := slept(); len(got) != retryAttempts-1 {
		t.Fatalf("sleeps = %v, want %d backoffs", got, retryAttempts-1)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RoundFile took %s: the deadline did not cut the call short", elapsed)
	}
}
