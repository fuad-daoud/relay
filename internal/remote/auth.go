package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// HeaderClient names the HTTP header carrying the ClientID fingerprint.
	HeaderClient = "Relay-Client"

	// HeaderTimestamp names the HTTP header carrying the decimal unix epoch seconds.
	HeaderTimestamp = "Relay-Timestamp"

	// HeaderNonce names the HTTP header carrying base64-encoded random bytes.
	HeaderNonce = "Relay-Nonce"

	// HeaderSignature names the HTTP header carrying the base64-encoded ed25519 signature.
	HeaderSignature = "Relay-Signature"
)

// MaxClockSkew defines the maximum allowed difference between request timestamp and server time.
const MaxClockSkew = 5 * time.Minute

// KeyStatus represents the status of a client key in the enrollment database.
type KeyStatus int

const (
	// KeyUnknown indicates a client key is not enrolled.
	KeyUnknown KeyStatus = iota

	// KeyActive indicates a client key is valid and active.
	KeyActive

	// KeyRevoked indicates a client key has been revoked.
	KeyRevoked
)

// KeyLookup resolves a ClientID to its ed25519 public key and status.
// When status is KeyUnknown, pub is nil.
type KeyLookup func(id ClientID) (pub ed25519.PublicKey, status KeyStatus)

// Auth sentinels carrying exactly the wire messages named in the spec.
var (
	ErrUnknownClient = errors.New("unknown client")
	ErrRevoked       = errors.New("revoked")
	ErrBadSignature  = errors.New("bad signature")
	ErrStale         = errors.New("stale or replayed")
)

// Canonical returns the exact bytes to be signed for an HTTP request:
//
//	method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + hex(sha256(body))
//
// target is the request path plus "?" + RawQuery when the query is non-empty,
// exactly as it appears on the HTTP request line (including query parameters such
// as ?since=<sha> on the bundle endpoint, so that query parameters are protected by
// the signature).
// bodySHA256 is the 32-byte sha256 checksum of the body. If bodySHA256 is empty or nil,
// the sha256 hash of zero bytes is computed and formatted.
func Canonical(method, target, timestamp, nonce string, bodySHA256 []byte) []byte {
	var bodyHex string
	if len(bodySHA256) == 0 {
		sum := sha256.Sum256(nil)
		bodyHex = hex.EncodeToString(sum[:])
	} else {
		bodyHex = hex.EncodeToString(bodySHA256)
	}

	canon := method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + bodyHex
	return []byte(canon)
}

// NewNonce returns 16 random bytes from crypto/rand, base64-encoded using StdEncoding.
func NewNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:]), nil
}

// Sign computes an ed25519 signature over the canonical request representation and
// returns an http.Header containing HeaderClient, HeaderTimestamp, HeaderNonce, and HeaderSignature.
func Sign(k Keypair, method, target string, bodySHA256 []byte, now time.Time, nonce string) http.Header {
	ts := strconv.FormatInt(now.Unix(), 10)
	canon := Canonical(method, target, ts, nonce, bodySHA256)
	sig := ed25519.Sign(k.Private, canon)

	h := make(http.Header)
	h.Set(HeaderClient, string(IDOf(k.Public)))
	h.Set(HeaderTimestamp, ts)
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return h
}

// NonceWindow tracks observed nonces over a rolling TTL window to prevent replay attacks.
type NonceWindow struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

// NewNonceWindow creates a new NonceWindow with the specified TTL.
func NewNonceWindow(ttl time.Duration) *NonceWindow {
	return &NonceWindow{
		ttl:     ttl,
		entries: make(map[string]time.Time),
	}
}

// Seen records nonce with expiry now+ttl and returns whether it was already present
// and unexpired. Expired entries are pruned on every call. Safe for concurrent use.
func (w *NonceWindow) Seen(nonce string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Prune expired entries on every call
	for k, exp := range w.entries {
		if !exp.After(now) {
			delete(w.entries, k)
		}
	}

	if exp, ok := w.entries[nonce]; ok && exp.After(now) {
		return true
	}

	w.entries[nonce] = now.Add(w.ttl)
	return false
}

// Verify authenticates an HTTP request. Checks are evaluated in strict order:
//  1. HeaderClient is present and non-empty -> ErrUnknownClient
//  2. Lookup client ID in lookup: KeyUnknown -> ErrUnknownClient; KeyRevoked -> ErrRevoked
//  3. Decode HeaderSignature as 64-byte base64 -> ErrBadSignature
//  4. ed25519.Verify signature over Canonical string -> ErrBadSignature
//  5. Parse HeaderTimestamp as decimal unix seconds; |now - ts| <= MaxClockSkew -> ErrStale
//  6. nonces.Seen(nonce, now) == false -> ErrStale
//  7. Return authenticated ClientID, nil
func Verify(h http.Header, method, target string, bodySHA256 []byte, now time.Time, lookup KeyLookup, nonces *NonceWindow) (ClientID, error) {
	// 1. id := h.Get(HeaderClient); "" -> ErrUnknownClient
	id := ClientID(h.Get(HeaderClient))
	if id == "" {
		return "", ErrUnknownClient
	}

	// 2. pub, status := lookup(id)
	//    KeyUnknown -> ErrUnknownClient; KeyRevoked -> ErrRevoked
	pub, status := lookup(id)
	switch status {
	case KeyUnknown:
		return "", ErrUnknownClient
	case KeyRevoked:
		return "", ErrRevoked
	case KeyActive:
		// continue
	default:
		return "", ErrUnknownClient
	}
	if len(pub) != ed25519.PublicKeySize {
		return "", ErrUnknownClient
	}

	// 3. sig := base64 decode HeaderSignature; decode failure or len != 64 -> ErrBadSignature
	sigStr := h.Get(HeaderSignature)
	sig, err := base64.StdEncoding.DecodeString(sigStr)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", ErrBadSignature
	}

	// 4. ed25519.Verify(pub, Canonical(method, target, h.Get(HeaderTimestamp), h.Get(HeaderNonce), bodySHA256), sig)
	//    false -> ErrBadSignature
	canon := Canonical(method, target, h.Get(HeaderTimestamp), h.Get(HeaderNonce), bodySHA256)
	if !ed25519.Verify(pub, canon, sig) {
		return "", ErrBadSignature
	}

	// 5. ts := parse HeaderTimestamp as int64; parse failure -> ErrStale;
	//    |now.Unix() - ts| > MaxClockSkew seconds -> ErrStale
	tsStr := h.Get(HeaderTimestamp)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", ErrStale
	}
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	if time.Duration(diff)*time.Second > MaxClockSkew {
		return "", ErrStale
	}

	// 6. nonces.Seen(h.Get(HeaderNonce), now) -> ErrStale
	if nonces != nil && nonces.Seen(h.Get(HeaderNonce), now) {
		return "", ErrStale
	}

	// 7. return id, nil
	return id, nil
}
