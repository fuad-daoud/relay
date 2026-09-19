package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ErrKeyFormat indicates that key data is malformed or invalidly encoded.
var ErrKeyFormat = errors.New("invalid key format")

// ErrKeyType indicates that a key is not an ed25519 key.
var ErrKeyType = errors.New("invalid key type; ed25519 required")

// ClientID is "SHA256:" + base64.RawStdEncoding(sha256(raw 32-byte ed25519 public key)).
// Same shape as ssh-keygen -l; 7 + 43 = 50 characters. Never empty.
type ClientID string

// Keypair contains an ed25519 private key and its corresponding public key.
type Keypair struct {
	Private ed25519.PrivateKey // 64 bytes
	Public  ed25519.PublicKey  // 32 bytes
}

// Generate generates a fresh ed25519 keypair using crypto/rand.
func Generate() (Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keypair{}, err
	}
	return Keypair{
		Private: priv,
		Public:  pub,
	}, nil
}

// IDOf returns the ClientID for the given ed25519 public key.
func IDOf(pub ed25519.PublicKey) ClientID {
	sum := sha256.Sum256(pub)
	return ClientID("SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]))
}

// MarshalPrivate encodes k's private key as PKCS#8 DER inside a PEM block of type "PRIVATE KEY".
func MarshalPrivate(k Keypair) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}), nil
}

// ParsePrivate parses a PKCS#8 PEM-encoded private key. If the PEM block is not of type
// "PRIVATE KEY" or the key is not ed25519, an error is returned.
func ParsePrivate(pemBytes []byte) (Keypair, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return Keypair{}, ErrKeyFormat
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Keypair{}, fmt.Errorf("%w: %v", ErrKeyFormat, err)
	}
	priv, ok := parsedKey.(ed25519.PrivateKey)
	if !ok {
		return Keypair{}, ErrKeyType
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return Keypair{}, ErrKeyType
	}
	return Keypair{
		Private: priv,
		Public:  pub,
	}, nil
}

// MarshalPublic formats an ed25519 public key as "ed25519 <base64>[ <comment>]".
func MarshalPublic(pub ed25519.PublicKey, comment string) string {
	b64 := base64.StdEncoding.EncodeToString(pub)
	if comment != "" {
		return "ed25519 " + b64 + " " + comment
	}
	return "ed25519 " + b64
}

// ParsePublic parses an enrollment line formatted as "ed25519 <base64>[ <comment>]".
// If the format is invalid or the key length is not 32 bytes, ErrKeyFormat is returned.
func ParsePublic(line string) (ed25519.PublicKey, error) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "ed25519 ") {
		return nil, ErrKeyFormat
	}
	rest := strings.TrimPrefix(line, "ed25519 ")
	parts := strings.SplitN(rest, " ", 2)
	b64Key := parts[0]
	if b64Key == "" {
		return nil, ErrKeyFormat
	}
	raw, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyFormat, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d", ErrKeyFormat, ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
