package remote

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

func TestKeyRoundTrip(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Private PEM round-trip
	pemBytes, err := MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	parsedKP, err := ParsePrivate(pemBytes)
	if err != nil {
		t.Fatalf("ParsePrivate: %v", err)
	}
	if !bytes.Equal(kp.Private, parsedKP.Private) {
		t.Fatal("parsed private key does not match original")
	}
	if !bytes.Equal(kp.Public, parsedKP.Public) {
		t.Fatal("parsed public key does not match original")
	}

	// Public line round-trip with comment including spaces
	comment := "my test key comment with spaces"
	pubLine := MarshalPublic(kp.Public, comment)
	parsedPub, err := ParsePublic(pubLine)
	if err != nil {
		t.Fatalf("ParsePublic: %v", err)
	}
	if !bytes.Equal(kp.Public, parsedPub) {
		t.Fatal("parsed public key line does not match original")
	}

	// Public line round-trip without comment
	pubLineNoComment := MarshalPublic(kp.Public, "")
	parsedPubNoComment, err := ParsePublic(pubLineNoComment)
	if err != nil {
		t.Fatalf("ParsePublic (no comment): %v", err)
	}
	if !bytes.Equal(kp.Public, parsedPubNoComment) {
		t.Fatal("parsed public key (no comment) does not match original")
	}

	// ID stable
	idOrig := IDOf(kp.Public)
	idParsed := IDOf(parsedPub)
	if idOrig != idParsed {
		t.Fatalf("IDOf mismatch: got %q, want %q", idParsed, idOrig)
	}
}

func TestParsePublicRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"rsa AAAA", "rsa AAAA"},
		{"wrong-length base64", "ed25519 AAAA"},
		{"empty", ""},
		{"random text", "not a valid line"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePublic(tc.input)
			if !errors.Is(err, ErrKeyFormat) {
				t.Fatalf("ParsePublic(%q): got %v, want ErrKeyFormat", tc.input, err)
			}
		})
	}
}

func TestParsePrivateRejectsRSA(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})

	_, err = ParsePrivate(pemBytes)
	if !errors.Is(err, ErrKeyType) {
		t.Fatalf("ParsePrivate(RSA): got %v, want ErrKeyType", err)
	}
}

func TestIDShape(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	id := string(IDOf(kp.Public))
	if !strings.HasPrefix(id, "SHA256:") {
		t.Fatalf("id %q does not start with SHA256:", id)
	}
	if len(id) != 50 {
		t.Fatalf("len(id) = %d, want 50 (id=%q)", len(id), id)
	}
	if strings.Contains(id, "=") {
		t.Fatalf("id %q contains padding =", id)
	}
}
