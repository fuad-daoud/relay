package remote

import (
	"bytes"
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

func TestParsePrivateRejectsWrongType(t *testing.T) {
	block := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: make([]byte, 64),
	})
	_, err := ParsePrivate(block)
	if !errors.Is(err, ErrKeyType) {
		t.Fatalf("ParsePrivate(wrong type): got %v, want ErrKeyType", err)
	}
}

func TestParsePrivateRejectsWrongLength(t *testing.T) {
	block := pem.EncodeToMemory(&pem.Block{
		Type:  "RELAY ED25519 PRIVATE KEY",
		Bytes: make([]byte, 32),
	})
	_, err := ParsePrivate(block)
	if !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("ParsePrivate(wrong length): got %v, want ErrKeyFormat", err)
	}
}

func TestParsePrivateRejectsNotPEM(t *testing.T) {
	_, err := ParsePrivate([]byte("garbage"))
	if !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("ParsePrivate(not PEM): got %v, want ErrKeyFormat", err)
	}
}

func TestMarshalPrivateType(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pemBytes, err := MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("MarshalPrivate: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("pem.Decode returned nil block")
	}
	if block.Type != "RELAY ED25519 PRIVATE KEY" {
		t.Fatalf("block.Type = %q, want %q", block.Type, "RELAY ED25519 PRIVATE KEY")
	}
	if !bytes.Equal(block.Bytes, kp.Private) {
		t.Fatalf("block.Bytes mismatch: got %x, want %x", block.Bytes, kp.Private)
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
