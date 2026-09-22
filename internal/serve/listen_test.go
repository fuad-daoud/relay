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
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/remote"
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

	fp, err := InitTLS(dir, []string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}

	cert, err := LoadTLS(dir)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}

	srv, err := New(Config{
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
	srv, err := New(Config{Root: dir, Now: time.Now})
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

	srv, err := New(Config{Root: dir, Now: func() time.Time { return now }})
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
