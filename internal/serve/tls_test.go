package serve

import (
	"encoding/pem"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func testSecretStore(t *testing.T) SecretStore {
	t.Helper()
	return SecretStore{DB: testServeDB(t), Root: t.TempDir()}
}

func TestInitTLSAndLoad(t *testing.T) {
	secrets := testSecretStore(t)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	hosts := []string{"example.com", "10.0.0.1"}

	fp, err := InitTLS(secrets, hosts, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	if fp == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	// The key and certificate are secrets now, not files (P5 §4.5).
	keyPEM, ok, err := secrets.SecretGet(tlsKeySecret)
	if err != nil || !ok || len(keyPEM) == 0 {
		t.Fatalf("serve.tls.key secret = (len %d, ok %v, err %v), want present", len(keyPEM), ok, err)
	}
	crtPEM, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil || !ok || len(crtPEM) == 0 {
		t.Fatalf("serve.tls.cert secret = (len %d, ok %v, err %v), want present", len(crtPEM), ok, err)
	}

	cert, err := LoadTLS(secrets)
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
	certFP, err := Fingerprint(secrets)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	leafFP := FingerprintOf(cert.Leaf.Raw)
	if certFP != leafFP {
		t.Errorf("Fingerprint = %q, FingerprintOf(leaf.Raw) = %q", certFP, leafFP)
	}
	if fp != leafFP {
		t.Errorf("InitTLS returned fp = %q, want %q", fp, leafFP)
	}
}

func TestInitTLSRefusesOverwrite(t *testing.T) {
	secrets := testSecretStore(t)
	now := time.Now()

	if _, err := InitTLS(secrets, nil, now); err != nil {
		t.Fatalf("first InitTLS: %v", err)
	}

	if _, err := InitTLS(secrets, nil, now); !errors.Is(err, ErrTLSExists) {
		t.Fatalf("second InitTLS err = %v, want ErrTLSExists", err)
	}
}

// TestTLSImportsLegacyFiles pins P5 §4.5's import: a serve root left by a pre-P5
// relevo holds server.key and server.crt files, and the first TLS read adopts
// them into the secrets and removes them.
func TestTLSImportsLegacyFiles(t *testing.T) {
	d := testServeDB(t)
	root := t.TempDir()
	seeder := SecretStore{DB: d, Root: root}
	if _, err := InitTLS(seeder, []string{"127.0.0.1"}, time.Now()); err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	keyPEM, _, err := seeder.SecretGet(tlsKeySecret)
	if err != nil {
		t.Fatal(err)
	}
	crtPEM, _, err := seeder.SecretGet(tlsCertSecret)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(crtPEM)
	if block == nil {
		t.Fatal("seeded certificate is not PEM")
	}
	wantFP := FingerprintOf(block.Bytes)

	// Move the material back into the legacy files and clear the secrets, so
	// the next read has to import.
	if err := os.WriteFile(root+"/server.key", keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/server.crt", crtPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Tx(func(tx *db.Tx) error {
		if err := tx.SecretDelete(tlsKeySecret); err != nil {
			return err
		}
		return tx.SecretDelete(tlsCertSecret)
	}); err != nil {
		t.Fatal(err)
	}

	cert, err := LoadTLS(seeder)
	if err != nil {
		t.Fatalf("LoadTLS after import: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("cert.Leaf is nil after import")
	}
	if got := FingerprintOf(cert.Leaf.Raw); got != wantFP {
		t.Errorf("imported fingerprint = %q, want %q", got, wantFP)
	}
	for _, name := range []string{"server.key", "server.crt"} {
		if _, err := os.Stat(root + "/" + name); !os.IsNotExist(err) {
			t.Errorf("%s still present after import (err %v), want it removed", name, err)
		}
	}
	if _, ok, err := seeder.SecretGet(tlsKeySecret); err != nil || !ok {
		t.Errorf("serve.tls.key after import = (ok %v, err %v), want present", ok, err)
	}
}
