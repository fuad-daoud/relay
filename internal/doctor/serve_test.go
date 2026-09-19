package doctor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func makeTestCertPEM(t *testing.T, notBefore, notAfter time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "relay serve",
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}
	return string(pem.EncodeToMemory(block))
}

func TestServeChecksNoServerKey(t *testing.T) {
	env := &fakeEnv{
		existingFiles: map[string]bool{},
		fileContents:  map[string]string{},
	}
	now := time.Now()
	checks := ServeChecks(env, "/fake/serve", now)
	if len(checks) != 0 {
		t.Fatalf("ServeChecks without server.key returned %d checks, want 0", len(checks))
	}
}

func TestServeChecksCertificate(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	serveRoot := "/fake/serve"

	t.Run("valid certificate", func(t *testing.T) {
		certPEM := makeTestCertPEM(t, now.Add(-time.Hour), now.Add(365*24*time.Hour))
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/server.crt": certPEM,
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "certificate")
		if c == nil {
			t.Fatal("missing serve: certificate check")
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "valid") {
			t.Errorf("detail = %q, want containing 'valid'", c.Detail)
		}
	})

	t.Run("expiring within 30d", func(t *testing.T) {
		certPEM := makeTestCertPEM(t, now.Add(-time.Hour), now.Add(15*24*time.Hour))
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/server.crt": certPEM,
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "certificate")
		if c == nil {
			t.Fatal("missing serve: certificate check")
		}
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "expires") {
			t.Errorf("detail = %q, want containing 'expires'", c.Detail)
		}
	})

	t.Run("expired certificate", func(t *testing.T) {
		certPEM := makeTestCertPEM(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/server.crt": certPEM,
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "certificate")
		if c == nil {
			t.Fatal("missing serve: certificate check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "expired") {
			t.Errorf("detail = %q, want containing 'expired'", c.Detail)
		}
	})

	t.Run("unreadable certificate", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/server.crt": "not a pem",
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "certificate")
		if c == nil {
			t.Fatal("missing serve: certificate check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "unreadable") {
			t.Errorf("detail = %q, want containing 'unreadable'", c.Detail)
		}
	})
}

func TestServeChecksClients(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"

	t.Run("multiple enrolled clients", func(t *testing.T) {
		clientsJSON := `[
			{"id": "id1", "label": "client1"},
			{"id": "id2", "label": "client2"}
		]`
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/clients.json": clientsJSON,
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if c.Detail != "2 enrolled" {
			t.Errorf("detail = %q, want '2 enrolled'", c.Detail)
		}
	})

	t.Run("none enrolled", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/clients.json": "[]",
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want SevWarn", c.Severity)
		}
		if !strings.Contains(c.Detail, "none enrolled") {
			t.Errorf("detail = %q, want containing 'none enrolled'", c.Detail)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			fileContents: map[string]string{
				serveRoot + "/clients.json": "{bad json",
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "clients")
		if c == nil {
			t.Fatal("missing serve: clients check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "parse error") {
			t.Errorf("detail = %q, want containing 'parse error'", c.Detail)
		}
	})
}

func TestServeChecksState(t *testing.T) {
	now := time.Now()
	serveRoot := "/fake/serve"

	t.Run("state writable", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if !strings.Contains(c.Detail, "writable") {
			t.Errorf("detail = %q, want containing 'writable'", c.Detail)
		}
	})

	t.Run("serveRoot stat fails", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				// serveRoot missing
			},
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
	})

	t.Run("probe fails", func(t *testing.T) {
		env := &fakeEnv{
			existingFiles: map[string]bool{
				serveRoot + "/server.key": true,
				serveRoot:                 true,
			},
			probeErr: errors.New("permission denied"),
		}
		checks := ServeChecks(env, serveRoot, now)
		c := findCheck(Report{Checks: checks}, "serve", "state")
		if c == nil {
			t.Fatal("missing serve: state check")
		}
		if c.Severity != SevFail {
			t.Errorf("severity = %v, want SevFail", c.Severity)
		}
		if !strings.Contains(c.Detail, "permission denied") && !strings.Contains(c.Detail, "not writable") {
			t.Errorf("detail = %q, want containing error", c.Detail)
		}
	})
}
