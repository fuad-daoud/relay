package serve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func waitForAddr(s *Server) (net.Addr, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := s.Addr(); addr != nil {
			return addr, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("timed out waiting for server address")
}

func TestListenTLSWhoAmI(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	secrets := SecretStore{DB: testServeDB(t), Root: dir}
	fp, err := InitTLS(secrets, []string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}

	cert, err := LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}

	srv, err := New(Config{
		DB:   testServeDB(t),
		Root: dir,
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{
			Addr: "127.0.0.1:0",
			TLS:  &cert,
		})
	}()

	addr, err := waitForAddr(srv)
	if err != nil {
		t.Fatal(err)
	}

	pinnedClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return errors.New("no peer certificates presented")
					}
					gotFP := FingerprintOf(rawCerts[0])
					if gotFP != fp {
						return fmt.Errorf("fingerprint mismatch: got %s, want %s", gotFP, fp)
					}
					return nil
				},
			},
		},
	}

	url := fmt.Sprintf("https://%s/v1/whoami", addr.String())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	hdr := remote.Sign(kp, "GET", "/v1/whoami", nil, now, nonce)
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := pinnedClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var who remote.WhoAmI
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if who.Label != "alice" {
		t.Errorf("who.Label = %q, want alice", who.Label)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return within 5 seconds")
	}
}

func TestListenRefusesWithoutTLS(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Config{DB: testServeDB(t), Root: dir, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	// The refusal has to be observed with a deadline, not by calling
	// ListenAndServe straight: take the guard away and it binds the port and
	// blocks in Serve until the context is cancelled, so a direct call hangs
	// forever instead of failing and pins nothing (#216).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{Addr: "127.0.0.1:0"})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrNoTLS) {
			t.Fatalf("ListenAndServe without TLS err = %v, want ErrNoTLS", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe without TLS never returned: the no-certificate guard is gone and the server is serving")
	}
}

func TestListenInsecureHTTP(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	srv, err := New(Config{DB: testServeDB(t), Root: dir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("bob", remote.MarshalPublic(kp.Public, "bob"), now); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{
			Addr:         "127.0.0.1:0",
			InsecureHTTP: true,
		})
	}()

	addr, err := waitForAddr(srv)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{}
	url := fmt.Sprintf("http://%s/v1/whoami", addr.String())
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	hdr := remote.Sign(kp, "GET", "/v1/whoami", nil, now, nonce)
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var who remote.WhoAmI
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if who.Label != "bob" {
		t.Errorf("who.Label = %q, want bob", who.Label)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return within 5 seconds")
	}
}

// TestListenAndServeWaitsForRun pins #373 §4.1's drain: on cancel,
// ListenAndServe shuts the HTTP server down and returns only after Run has
// finished its in-flight tick. Tick cannot be held open through the real
// implementation without a large refactor, so this uses the unexported tickFn
// seam.
//
// Mutation check: take the `<-drained` wait out of ListenAndServe and this
// fails: it returns before the held tick is released.
func TestListenAndServeWaitsForRun(t *testing.T) {
	srv, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	var sawCancelled, tickReturned bool

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.tickFn = func(ctx context.Context) error {
		if ctx.Err() != nil {
			mu.Lock()
			sawCancelled = true
			mu.Unlock()
		}
		once.Do(func() { close(started) })
		<-release
		mu.Lock()
		tickReturned = true
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{Addr: "127.0.0.1:0", InsecureHTTP: true})
	}()

	if _, err := waitForAddr(srv); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first tick never started")
	}

	cancel()

	select {
	case err := <-errCh:
		t.Fatalf("ListenAndServe returned %v before the in-flight tick finished", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after the in-flight tick finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if !tickReturned {
		t.Error("the tick never returned")
	}
	if sawCancelled {
		t.Error("the tick saw a cancelled context, want the WithoutCancel one")
	}
}
