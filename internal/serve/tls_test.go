package serve

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitTLSAndLoad(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	hosts := []string{"example.com", "10.0.0.1"}

	fp, err := InitTLS(dir, hosts, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	if fp == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	keyPath := filepath.Join(dir, "server.key")
	crtPath := filepath.Join(dir, "server.crt")

	keyFi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat server.key: %v", err)
	}
	if keyFi.Mode().Perm() != 0o600 {
		t.Errorf("server.key permissions = %o, want 0600", keyFi.Mode().Perm())
	}

	crtFi, err := os.Stat(crtPath)
	if err != nil {
		t.Fatalf("stat server.crt: %v", err)
	}
	if crtFi.Mode().Perm() != 0o644 {
		t.Errorf("server.crt permissions = %o, want 0644", crtFi.Mode().Perm())
	}

	cert, err := LoadTLS(dir)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("cert.Leaf is nil")
	}

	// Validity 10 years
	if !cert.Leaf.NotBefore.Equal(now) {
		t.Errorf("NotBefore = %v, want %v", cert.Leaf.NotBefore, now)
	}
	wantAfter := now.AddDate(10, 0, 0)
	if !cert.Leaf.NotAfter.Equal(wantAfter) {
		t.Errorf("NotAfter = %v, want %v", cert.Leaf.NotAfter, wantAfter)
	}

	// SANs
	hasDNS := func(name string) bool {
		for _, d := range cert.Leaf.DNSNames {
			if d == name {
				return true
			}
		}
		return false
	}
	hasIP := func(ipStr string) bool {
		target := net.ParseIP(ipStr)
		for _, ip := range cert.Leaf.IPAddresses {
			if ip.Equal(target) {
				return true
			}
		}
		return false
	}

	if !hasDNS("example.com") {
		t.Errorf("missing DNS SAN example.com in %v", cert.Leaf.DNSNames)
	}
	if !hasDNS("localhost") {
		t.Errorf("missing DNS SAN localhost in %v", cert.Leaf.DNSNames)
	}
	if !hasIP("10.0.0.1") {
		t.Errorf("missing IP SAN 10.0.0.1 in %v", cert.Leaf.IPAddresses)
	}
	if !hasIP("127.0.0.1") {
		t.Errorf("missing IP SAN 127.0.0.1 in %v", cert.Leaf.IPAddresses)
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		if net.ParseIP(hn) != nil {
			if !hasIP(hn) {
				t.Errorf("missing machine hostname IP SAN %s in %v", hn, cert.Leaf.IPAddresses)
			}
		} else {
			if !hasDNS(hn) {
				t.Errorf("missing machine hostname DNS SAN %s in %v", hn, cert.Leaf.DNSNames)
			}
		}
	}

	// Fingerprint check
	fileFP, err := Fingerprint(crtPath)
	if err != nil {
		t.Fatalf("Fingerprint(crtPath): %v", err)
	}
	leafFP := FingerprintOf(cert.Leaf.Raw)
	if fileFP != leafFP {
		t.Errorf("Fingerprint(crt) = %q, FingerprintOf(leaf.Raw) = %q", fileFP, leafFP)
	}
	if fp != leafFP {
		t.Errorf("InitTLS returned fp = %q, want %q", fp, leafFP)
	}
}

func TestInitTLSRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	_, err := InitTLS(dir, nil, now)
	if err != nil {
		t.Fatalf("first InitTLS: %v", err)
	}

	_, err = InitTLS(dir, nil, now)
	if !errors.Is(err, ErrTLSExists) {
		t.Fatalf("second InitTLS err = %v, want ErrTLSExists", err)
	}
}
