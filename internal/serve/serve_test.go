package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

func signedRequest(t *testing.T, kp remote.Keypair, method, target string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	var sum []byte
	if body != nil {
		bodyReader = bytes.NewReader(body)
		s := sha256.Sum256(body)
		sum = s[:]
	}
	req, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	hdr := remote.Sign(kp, method, target, sum, time.Now(), nonce)
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return req
}

func TestClientsAddRevokeLookup(t *testing.T) {
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "test client")

	// 1. Unknown -> KeyUnknown
	_, status := c.Lookup(id)
	if status != remote.KeyUnknown {
		t.Fatalf("Lookup unknown: got %v, want KeyUnknown", status)
	}

	// 2. Add -> KeyActive
	cl, err := c.Add("client1", pubLine, time.Now())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if cl.ID != id || cl.Label != "client1" {
		t.Fatalf("Add result mismatch: %+v", cl)
	}
	pub, status := c.Lookup(id)
	if status != remote.KeyActive {
		t.Fatalf("Lookup after add: got %v, want KeyActive", status)
	}
	if !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup returned wrong public key")
	}

	// Adding already enrolled client without revocation -> ErrAlreadyEnrolled
	_, err = c.Add("client1", pubLine, time.Now())
	if !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("Add duplicate: got %v, want ErrAlreadyEnrolled", err)
	}

	// 3. Revoke -> KeyRevoked
	if err := c.Revoke(id, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, status = c.Lookup(id)
	if status != remote.KeyRevoked {
		t.Fatalf("Lookup after revoke: got %v, want KeyRevoked", status)
	}

	// Revoke unknown client -> ErrNoSuchClient
	if err := c.Revoke("SHA256:unknown", time.Now()); !errors.Is(err, ErrNoSuchClient) {
		t.Fatalf("Revoke unknown: got %v, want ErrNoSuchClient", err)
	}

	// 4. Re-add -> KeyActive
	cl2, err := c.Add("client1-renewed", pubLine, time.Now())
	if err != nil {
		t.Fatalf("Re-add: %v", err)
	}
	if cl2.Label != "client1-renewed" {
		t.Fatalf("Re-add label mismatch: %+v", cl2)
	}
	_, status = c.Lookup(id)
	if status != remote.KeyActive {
		t.Fatalf("Lookup after re-add: got %v, want KeyActive", status)
	}

	// 5. File survives a reload
	cLoaded, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients reload: %v", err)
	}
	pubLoaded, statusLoaded := cLoaded.Lookup(id)
	if statusLoaded != remote.KeyActive {
		t.Fatalf("Lookup after reload: got %v, want KeyActive", statusLoaded)
	}
	if !bytes.Equal(pubLoaded, kp.Public) {
		t.Fatalf("Loaded pub key mismatch")
	}
	if cLoaded.LabelOf(id) != "client1-renewed" {
		t.Fatalf("LabelOf: got %q, want client1-renewed", cLoaded.LabelOf(id))
	}
}

func TestClientsLookupSeesEnrollFromAnotherInstance(t *testing.T) {
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c1, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients 1: %v", err)
	}
	c2, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients 2: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "client 1")

	// c2 should not know id initially
	if _, status := c2.Lookup(id); status != remote.KeyUnknown {
		t.Fatalf("Lookup before add: got %v, want KeyUnknown", status)
	}

	// Add on c1
	if _, err := c1.Add("client1", pubLine, time.Now()); err != nil {
		t.Fatalf("Add on c1: %v", err)
	}

	// Lookup on c2 returns KeyActive without any reload call
	pub, status := c2.Lookup(id)
	if status != remote.KeyActive {
		t.Fatalf("Lookup on c2: got %v, want KeyActive", status)
	}
	if !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup returned wrong public key")
	}
}

func TestClientsRefreshKeepsListOnParseError(t *testing.T) {
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "client 1")

	if _, err := c.Add("client1", pubLine, time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Corrupt the file. Size will differ from the formatted JSON written by Add.
	corrupted := []byte("{invalid json, not a client list")
	if err := os.WriteFile(clientsPath, corrupted, 0o600); err != nil {
		t.Fatalf("write corrupted clients.json: %v", err)
	}

	// Lookup still answers from the old list
	pub, status := c.Lookup(id)
	if status != remote.KeyActive {
		t.Fatalf("Lookup after corrupt file: got %v, want KeyActive", status)
	}
	if !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup returned wrong public key")
	}
}

func TestClientsRefreshOnDelete(t *testing.T) {
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "client 1")

	if _, err := c.Add("client1", pubLine, time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	pub, status := c.Lookup(id)
	if status != remote.KeyActive {
		t.Fatalf("Lookup before delete: got %v, want KeyActive", status)
	}
	if !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup returned wrong public key")
	}

	// Remove the file
	if err := os.Remove(clientsPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Lookup -> KeyUnknown
	_, status = c.Lookup(id)
	if status != remote.KeyUnknown {
		t.Fatalf("Lookup after remove: got %v, want KeyUnknown", status)
	}
}

// TestOwnerLabel checks the request log's owner field (#100 step 6): an
// enrolled caller's label, and "-" -- not "" -- for a request that never
// authenticated at all (the zero ClientID an auth failure leaves behind).
func TestOwnerLabel(t *testing.T) {
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(clientsPath)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := c.Add("laptop", remote.MarshalPublic(kp.Public, "test client"), time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := ownerLabel(c, id); got != "laptop" {
		t.Errorf("ownerLabel(enrolled) = %q, want laptop", got)
	}
	// Mutation target: drop the caller == "" guard and this reads "" (via
	// LabelOf's own fallback on an empty id) instead of "-".
	if got := ownerLabel(c, ""); got != "-" {
		t.Errorf(`ownerLabel(unauthenticated) = %q, want "-"`, got)
	}
}

func newTestServer(t *testing.T, maxBundleBytes int64) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		Root:           root,
		MaxBundleBytes: maxBundleBytes,
		Now:            time.Now,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	return s, root
}

func testRuntime(t *testing.T, s *Server, id remote.ClientID) relay.Runtime {
	t.Helper()
	rt, err := s.runtime(id)
	if err != nil {
		t.Fatalf("runtime(%s): %v", id, err)
	}
	return rt
}

func TestAuthRejects(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kpEnrolled, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubLine := remote.MarshalPublic(kpEnrolled.Public, "enrolled")
	if _, err := s.clients.Add("enrolled", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Run("not_enrolled", func(t *testing.T) {
		kpUnknown, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		req := signedRequest(t, kpUnknown, "GET", "/v1/whoami", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var errBody remote.ErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if errBody.Code != remote.CodeNotEnrolled {
			t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeNotEnrolled)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		kpRevoked, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		line := remote.MarshalPublic(kpRevoked.Public, "revoked")
		if _, err := s.clients.Add("revoked", line, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.clients.Revoke(remote.IDOf(kpRevoked.Public), time.Now()); err != nil {
			t.Fatal(err)
		}

		req := signedRequest(t, kpRevoked, "GET", "/v1/whoami", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var errBody remote.ErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if errBody.Code != remote.CodeRevoked {
			t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRevoked)
		}
	})

	t.Run("bad_signature", func(t *testing.T) {
		req := signedRequest(t, kpEnrolled, "GET", "/v1/whoami", nil)
		// Corrupt signature
		req.Header.Set(remote.HeaderSignature, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var errBody remote.ErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if errBody.Code != remote.CodeBadSignature {
			t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeBadSignature)
		}
	})

	t.Run("stale", func(t *testing.T) {
		staleTime := time.Now().Add(-10 * time.Minute)
		nonce, err := remote.NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(nil)
		hdr := remote.Sign(kpEnrolled, "GET", "/v1/whoami", sum[:], staleTime, nonce)

		req, err := http.NewRequest("GET", "/v1/whoami", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, vv := range hdr {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var errBody remote.ErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if errBody.Code != remote.CodeStale {
			t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeStale)
		}
	})
}

func TestAuthBodyCap(t *testing.T) {
	s, _ := newTestServer(t, 1024)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubLine := remote.MarshalPublic(kp.Public, "cap-tester")
	if _, err := s.clients.Add("cap-tester", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 1025 bytes is 1 byte over 1024
	body := make([]byte, 1025)
	req := signedRequest(t, kp, "POST", "/v1/whoami", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if errBody.Code != remote.CodeTooLarge {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTooLarge)
	}
}

func TestWhoAmI(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "alice")
	if _, err := s.clients.Add("alice", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	req := signedRequest(t, kp, "GET", "/v1/whoami", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if who.ID != id {
		t.Fatalf("ID = %q, want %q", who.ID, id)
	}
	if who.Label != "alice" {
		t.Fatalf("Label = %q, want alice", who.Label)
	}
	if who.ServerVersion != remote.Version {
		t.Fatalf("ServerVersion = %d, want %d", who.ServerVersion, remote.Version)
	}
	if len(who.Transports) != 1 || who.Transports[0] != "git-bundle" {
		t.Fatalf("Transports = %v, want [git-bundle]", who.Transports)
	}
	if len(who.Features) != 2 || who.Features[0] != remote.FeatureTier || who.Features[1] != remote.FeatureQueue {
		t.Fatalf("Features = %v, want [%s %s]", who.Features, remote.FeatureTier, remote.FeatureQueue)
	}
	if who.Builders == nil || who.Builders.Cap <= 0 {
		t.Fatalf("Builders = %+v, want a positive Cap", who.Builders)
	}
	if who.BuilderTier != "harness" {
		t.Fatalf("BuilderTier = %q, want harness", who.BuilderTier)
	}
	if who.MaxTier != "edit" {
		t.Fatalf("MaxTier = %q, want edit", who.MaxTier)
	}
	if who.Builders.Quota != "" {
		t.Fatalf("Builders.Quota = %q, want empty without a scope", who.Builders.Quota)
	}

	// With a scope configured, WhoAmI carries its slice and CPU quota (#295).
	scoped, err := New(Config{
		Root:  t.TempDir(),
		Now:   time.Now,
		Scope: &relay.ScopeSpec{Slice: "relay.slice", CPUQuota: "200%"},
	})
	if err != nil {
		t.Fatalf("New scoped server: %v", err)
	}
	if _, err := scoped.clients.Add("alice", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}
	scopedRec := httptest.NewRecorder()
	scoped.Handler().ServeHTTP(scopedRec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if scopedRec.Code != http.StatusOK {
		t.Fatalf("scoped status = %d, want 200; body: %s", scopedRec.Code, scopedRec.Body.String())
	}
	var scopedWho remote.WhoAmI
	if err := json.NewDecoder(scopedRec.Body).Decode(&scopedWho); err != nil {
		t.Fatalf("decode scoped body: %v", err)
	}
	if scopedWho.Builders == nil || scopedWho.Builders.Quota != "200%" {
		t.Fatalf("scoped Builders = %+v, want Quota 200%%", scopedWho.Builders)
	}
	if scopedWho.Builders.Slice != "relay.slice" {
		t.Fatalf("scoped Builders.Slice = %q, want relay.slice", scopedWho.Builders.Slice)
	}
}

func TestWhoAmIBuilderTierFromPolicy(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{Tier: map[string]string{"builder": "edit"}, MaxTier: "yolo"})

	req := signedRequest(t, kp, "GET", "/v1/whoami", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if who.BuilderTier != "edit" {
		t.Fatalf("BuilderTier = %q, want edit", who.BuilderTier)
	}
	if who.MaxTier != "yolo" {
		t.Fatalf("MaxTier = %q, want yolo", who.MaxTier)
	}
}

func TestNonV1Is426(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	req, err := http.NewRequest("GET", "/v0/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if errBody.Code != remote.CodeVersion {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeVersion)
	}
	if errBody.Message != "this server speaks v1" {
		t.Fatalf("error message = %q, want %q", errBody.Message, "this server speaks v1")
	}
}

func TestCreateBinding(t *testing.T) {
	s, root := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "creator")
	if _, err := s.clients.Add("creator", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
	})
	req := signedRequest(t, kp, "POST", "/v1/bindings", createBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var view remote.BindingView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Name != "api" || view.Round != 1 || view.State != "active" {
		t.Fatalf("view mismatch: %+v", view)
	}

	// Bare repo exists at Serve.BareRepo
	idDir, ok := id.Dir()
	if !ok {
		t.Fatalf("id.Dir() failed for %s", id)
	}
	bareRepoPath := filepath.Join(root, "repos", idDir, "repo123.git")
	if _, err := os.Stat(filepath.Join(bareRepoPath, "HEAD")); err != nil {
		t.Fatalf("bare repo HEAD missing at %s: %v", bareRepoPath, err)
	}

	// binding.json has Owner
	b, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Owner != string(id) {
		t.Fatalf("binding.Owner = %q, want %q", b.Owner, id)
	}
}

func TestOwnerDirIsFlatHex(t *testing.T) {
	s, root := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "creator")
	if _, err := s.clients.Add("creator", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
	})
	req := signedRequest(t, kp, "POST", "/v1/bindings", createBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	bindingsDir := filepath.Join(root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", bindingsDir, err)
	}
	t.Logf("ls %s:", bindingsDir)
	for _, e := range entries {
		t.Logf("  %s (isDir=%v)", e.Name(), e.IsDir())
	}

	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	entry := entries[0]
	expectedDir, ok := id.Dir()
	if !ok {
		t.Fatalf("id.Dir() failed for %s", id)
	}
	if entry.Name() != expectedDir {
		t.Fatalf("entry name = %q, want %q", entry.Name(), expectedDir)
	}
	if len(entry.Name()) != 64 {
		t.Fatalf("len(entry.Name()) = %d, want 64", len(entry.Name()))
	}
	for _, c := range entry.Name() {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("entry name %q contains non-lower-hex char: %c", entry.Name(), c)
		}
	}
	if !entry.IsDir() {
		t.Fatalf("entry %s is not a directory", entry.Name())
	}

	ownerSubDir := filepath.Join(bindingsDir, entry.Name())
	subEntries, err := os.ReadDir(ownerSubDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", ownerSubDir, err)
	}
	t.Logf("ls %s:", ownerSubDir)
	for _, sub := range subEntries {
		t.Logf("  %s", sub.Name())
	}

	bindingJSONPath := filepath.Join(ownerSubDir, "api", "bind.json")
	if _, err := os.Stat(bindingJSONPath); err != nil {
		t.Fatalf("bind.json missing at %s: %v", bindingJSONPath, err)
	}
	apiEntries, err := os.ReadDir(filepath.Join(ownerSubDir, "api"))
	if err != nil {
		t.Fatalf("ReadDir api: %v", err)
	}
	t.Logf("ls %s/api:", ownerSubDir)
	for _, a := range apiEntries {
		t.Logf("  %s", a.Name())
	}
}

// newTierTestServer builds a server with one builder candidate and the
// given policy, for handleCreateBinding tier-resolution tests (#141 remote
// half).
func newTierTestServer(t *testing.T, pol policy.Policy) (*Server, remote.Keypair) {
	t.Helper()
	root := t.TempDir()

	candPath := filepath.Join(root, "candidates.json")
	candJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		Root:       root,
		Candidates: cSet,
		Policy:     pol,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}
	return srv, kp
}

func createBindingRequest(t *testing.T, srv *Server, kp remote.Keypair, req remote.CreateBindingRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq := signedRequest(t, kp, "POST", "/v1/bindings", body)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httpReq)
	return rec
}

func TestCreateBindingResolvesTierFromPolicy(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{Tier: map[string]string{"builder": "edit"}})

	rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var view remote.BindingView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Tier != "edit" {
		t.Fatalf("view.Tier = %q, want edit", view.Tier)
	}

	id := remote.IDOf(kp.Public)
	b, err := testRuntime(t, srv, id).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Tier != "edit" {
		t.Fatalf("stored binding Tier = %q, want edit", b.Tier)
	}
}

func TestCreateBindingNoPolicyTierDefaultsToHarness(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{})

	rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var view remote.BindingView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Tier != "harness" {
		t.Fatalf("view.Tier = %q, want harness", view.Tier)
	}

	id := remote.IDOf(kp.Public)
	b, err := testRuntime(t, srv, id).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Tier != "harness" {
		t.Fatalf("stored binding Tier = %q, want harness", b.Tier)
	}
}

func TestCreateBindingTierAboveMaxRefused(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{})

	rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
		Tier:       "yolo",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Code != remote.CodeTierAboveMax {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTierAboveMax)
	}

	id := remote.IDOf(kp.Public)
	if _, err := testRuntime(t, srv, id).Store.Load("api"); err == nil {
		t.Fatal("binding was stored despite tier_above_max refusal")
	}
}

func TestCreateBindingBogusTierInvalid(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{})

	rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
		Tier:       "bogus",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
}

func TestCreateInvalid(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubLine := remote.MarshalPublic(kp.Public, "user")
	if _, err := s.clients.Add("user", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Bad name
	badNameBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "invalid/name",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("a", 40),
	})
	req := signedRequest(t, kp, "POST", "/v1/bindings", badNameBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
}

func TestCreateDuplicate(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubLine := remote.MarshalPublic(kp.Public, "user")
	if _, err := s.clients.Add("user", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("b", 40),
	})
	req1 := signedRequest(t, kp, "POST", "/v1/bindings", body)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("status 1 = %d, want 201", rec1.Code)
	}

	req2 := signedRequest(t, kp, "POST", "/v1/bindings", body)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("status 2 = %d, want 409", rec2.Code)
	}
}

func TestListIsOwnerScoped(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	kpB, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("bob", remote.MarshalPublic(kpB.Public, "bob"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// Client A creates "api"
	bodyA, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("1", 40),
	})
	reqCreate := signedRequest(t, kpA, "POST", "/v1/bindings", bodyA)
	recCreate := httptest.NewRecorder()
	handler.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body: %s", recCreate.Code, recCreate.Body.String())
	}

	// Client B lists -> empty
	reqList := signedRequest(t, kpB, "GET", "/v1/bindings", nil)
	recList := httptest.NewRecorder()
	handler.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", recList.Code)
	}
	var views []remote.BindingView
	if err := json.NewDecoder(recList.Body).Decode(&views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("B's list returned %d views, want 0", len(views))
	}

	// Client B GETs "api" -> 404
	reqGet := signedRequest(t, kpB, "GET", "/v1/bindings/api", nil)
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusNotFound {
		t.Fatalf("B's GET of api status = %d, want 404", recGet.Code)
	}
}

func TestGetTouchesLastSeen(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("2", 40),
	})
	reqCreate := signedRequest(t, kp, "POST", "/v1/bindings", createBody)
	recCreate := httptest.NewRecorder()
	handler.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", recCreate.Code)
	}

	// Check initial LastSeen
	b, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	initial := b.Serve.LastSeen

	time.Sleep(10 * time.Millisecond)

	reqGet := signedRequest(t, kp, "GET", "/v1/bindings/api", nil)
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", recGet.Code)
	}

	bAfter, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if !bAfter.Serve.LastSeen.After(initial) {
		t.Fatalf("touched LastSeen = %v not after initial = %v", bAfter.Serve.LastSeen, initial)
	}
}

func TestUnavailableGatesServerWide(t *testing.T) {
	root := t.TempDir()
	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		Root:       root,
		Candidates: cSet,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idA := remote.IDOf(kpA.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// A calls unavailable on token
	unavailBody, _ := json.Marshal(remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "rate limited test",
	})
	req := signedRequest(t, kpA, "POST", "/v1/unavailable", unavailBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Check server-wide ledger.json at root exists
	serverLedgerPath := filepath.Join(root, "ledger.json")
	if _, err := os.Stat(serverLedgerPath); err != nil {
		t.Fatalf("server-wide ledger.json missing: %v", err)
	}

	// Check no per-owner store dir gains a ledger.json
	idADir, ok := idA.Dir()
	if !ok {
		t.Fatalf("idA.Dir() failed for %s", idA)
	}
	ownerLedgerPath := filepath.Join(root, "bindings", idADir, "ledger.json")
	if _, err := os.Stat(ownerLedgerPath); err == nil {
		t.Fatalf("per-owner ledger.json unexpectedly exists at %s", ownerLedgerPath)
	}
}

// TestAvailableClearsServerWideGate: POST /v1/available mirrors
// /v1/unavailable. It clears the server-wide ledger's rate-limit gate for the
// subject's provider, answers with that provider and how many entries went,
// takes a bare provider (so a second call is a no-op, not an error), refuses
// an empty subject, and reads an unknown token as the client mistake it is.
func TestAvailableClearsServerWideGate(t *testing.T) {
	root := t.TempDir()
	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		Root:       root,
		Candidates: cSet,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// Gate the provider through the server-wide endpoint.
	unavailBody, _ := json.Marshal(remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "rate limited test",
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/unavailable", unavailBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Lift it by bare provider.
	availBody, _ := json.Marshal(remote.AvailableRequest{Subject: "anthropic"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", availBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("available status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp remote.AvailableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode available response: %v", err)
	}
	if resp.Removed != 1 {
		t.Errorf("removed = %d, want 1", resp.Removed)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", resp.Provider)
	}

	// The server-wide ledger has no rate_limited entry left.
	l, err := ledger.Load(filepath.Join(root, "ledger.json"))
	if err != nil {
		t.Fatalf("ledger.Load: %v", err)
	}
	for _, e := range l.Entries {
		if e.Kind == ledger.RateLimited {
			t.Errorf("ledger still holds %+v, want the rate-limit gate gone", e)
		}
	}

	// A second call lifts nothing, and is not an error.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", availBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("second available status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	resp = remote.AvailableResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode second available response: %v", err)
	}
	if resp.Removed != 0 {
		t.Errorf("second removed = %d, want 0", resp.Removed)
	}

	// An empty subject is a bad request.
	emptyBody, _ := json.Marshal(remote.AvailableRequest{Subject: ""})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", emptyBody))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty subject status = %d, want 400", rec.Code)
	}

	// An unknown token is a client mistake, not a server failure.
	unknownBody, _ := json.Marshal(remote.AvailableRequest{Subject: "claude/anthropic/nope"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", unknownBody))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown token status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
}

// TestAvailableRefusesUnknownProvider is #301 over the wire: the pre-check
// handleAvailable used to carry only ever refused unknown *tokens* and let
// any bare provider through, so `relay available anthropc` forwarded to a
// server read as a no-op. relay.Available now refuses a typo itself, and the
// 422 carries the local verb's words.
func TestAvailableRefusesUnknownProvider(t *testing.T) {
	root := t.TempDir()
	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		Root:       root,
		Candidates: cSet,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(remote.AvailableRequest{Subject: "anthropc"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown provider status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
	// The message is read decoded: on the wire its quotes are JSON-escaped.
	var errBody remote.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if want := `no configured candidate uses provider "anthropc"`; !strings.Contains(errBody.Message, want) {
		t.Errorf("message = %q, want it containing %q", errBody.Message, want)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}

	// A repo these tests create gets auto-maintenance off. Every `git commit`
	// otherwise spawns `git maintenance run --auto --quiet --detach`, which
	// outlives the command and writes under .git/objects while t.TempDir()'s
	// RemoveAll is removing the tree -- and that cleanup failure fails the
	// test, not just the teardown (#304). Repo-local config, so every later
	// git command on it inherits it, including ones the code under test runs.
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

// scriptRunner is a fake relay.Runner. Liveness is tracked per pid (#285):
// two bindings' processes must be tellable apart, which a single shared
// flag cannot do. setAlive(a) flips every pid this runner has ever started
// to a -- the shape every pre-#285 test wants, since each of them tracks
// exactly one binding's process, so "every pid" and "the one pid" agree.
// finish(pid) flips exactly one pid dead, for a test with more than one
// binding in flight at once.
type scriptRunner struct {
	mu           sync.Mutex
	specs        []relay.ProcSpec
	aliveHandles []relay.ProcHandle
	alive        map[int]bool
	nextPID      int
	// startErr, when set, is returned by Start instead of starting anything
	// -- a builder spawn failure (#250 items 1 and 3).
	startErr error
}

func newScriptRunner() *scriptRunner {
	return &scriptRunner{alive: map[int]bool{}, nextPID: 4242}
}

func (r *scriptRunner) Start(ctx context.Context, spec relay.ProcSpec) (relay.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return relay.ProcHandle{}, r.startErr
	}
	pid := r.nextPID
	r.nextPID++
	r.specs = append(r.specs, spec)
	r.alive[pid] = true
	if spec.LogPath != "" {
		_ = os.WriteFile(spec.LogPath, []byte("builder started\n"), 0o644)
	}
	return relay.ProcHandle{PID: pid, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h relay.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliveHandles = append(r.aliveHandles, h)
	return r.alive[h.PID], nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h relay.ProcHandle, logPath string) (code int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	alive, tracked := r.alive[h.PID]
	if !tracked {
		// A pid this runner never started (a fresh runner standing in for a
		// daemon restart, #285) has no exit trailer to report: "unknown",
		// the same as a real relay-exit: trailer that was never written
		// because the supervisor died with the process. This is what tells
		// "confirmed dead, code 0" (tracked, not alive) apart from "lost,
		// no idea" (never tracked) -- headless.go's restart-requeue check
		// keys on exactly that difference.
		return 0, false
	}
	return 0, !alive
}

func (r *scriptRunner) Kill(ctx context.Context, h relay.ProcHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[h.PID] = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h relay.ProcHandle, streamPath string) (relay.ProcRusage, bool) {
	return relay.ProcRusage{}, false
}

// setAlive flips every pid this runner has started to a. Kept for every
// test that predates per-pid liveness and tracks exactly one binding's
// process.
func (r *scriptRunner) setAlive(a bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid := range r.alive {
		r.alive[pid] = a
	}
}

// finish marks pid exited without disturbing any other pid this runner is
// tracking -- what a multi-binding test (#285's admit tests) needs that
// setAlive cannot give it.
func (r *scriptRunner) finish(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[pid] = false
}

func doSigned(t *testing.T, ts *httptest.Server, kp remote.Keypair, method, path string, body []byte, contentType string) (*http.Response, []byte) {
	t.Helper()
	req := signedRequest(t, kp, method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(ts.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, respBody
}

func makeRoundForm(t *testing.T, round int, plan string, bundleBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if len(bundleBytes) > 0 {
		part, err := mw.CreateFormFile("bundle", "bundle.bundle")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bundleBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// makeRoundFormTags is makeRoundForm with the optional "tags" field (#242).
func makeRoundFormTags(t *testing.T, round int, plan string, bundleBytes []byte, tagsJSON string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if tagsJSON != "" {
		if err := mw.WriteField("tags", tagsJSON); err != nil {
			t.Fatal(err)
		}
	}
	if len(bundleBytes) > 0 {
		part, err := mw.CreateFormFile("bundle", "bundle.bundle")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bundleBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

type testEnv struct {
	srv       *Server
	ts        *httptest.Server
	kp        remote.Keypair
	id        remote.ClientID
	clientDir string
	rootSHA   string
	headSHA   string
	repoID    string
	runner    *scriptRunner
	gitClient *git.Client
	transport *remote.BundleTransport
}

func setupTestEnv(t *testing.T, cfgOpts ...func(*Config)) *testEnv {
	t.Helper()
	ctx := context.Background()
	gitClient := git.NewClient("git", 0, 0)

	clientDir := t.TempDir()
	runGit(t, clientDir, "init")
	runGit(t, clientDir, "config", "user.name", "test")
	runGit(t, clientDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(clientDir, "file.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientDir, "add", "file.txt")
	runGit(t, clientDir, "commit", "-m", "initial commit")

	headSHA, ok, err := gitClient.RefSHA(ctx, clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("headSHA: %v, ok=%v", err, ok)
	}
	rootSHA, err := gitClient.RootCommit(ctx, clientDir)
	if err != nil {
		t.Fatalf("rootCommit: %v", err)
	}
	repoID, err := remote.RepoID(rootSHA)
	if err != nil {
		t.Fatalf("repoID: %v", err)
	}

	serverRoot := t.TempDir()
	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	srvCfg := Config{
		Root:       serverRoot,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	}
	for _, opt := range cfgOpts {
		opt(&srvCfg)
	}
	srv, err := New(srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	transport := remote.NewBundleTransport(gitClient, t.TempDir())

	return &testEnv{
		srv:       srv,
		ts:        ts,
		kp:        kp,
		id:        id,
		clientDir: clientDir,
		rootSHA:   rootSHA,
		headSHA:   headSHA,
		repoID:    repoID,
		runner:    runner,
		gitClient: gitClient,
		transport: transport,
	}
}

func (env *testEnv) runtime(t *testing.T) relay.Runtime {
	t.Helper()
	return testRuntime(t, env.srv, env.id)
}

func TestRoundStartAbsorbsAndChecksOut(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	outRef := "refs/relay/api/out"
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	snap, err := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	bundleBytes, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatalf("read snap body: %v", err)
	}

	formBytes, ct := makeRoundForm(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start round status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if _, err := os.Stat(b.Worktree); err != nil {
		t.Fatalf("worktree stat: %v", err)
	}
	wtHead, ok, err := env.gitClient.RefSHA(ctx, b.Worktree, "HEAD")
	if err != nil || !ok || wtHead != env.headSHA {
		t.Fatalf("worktree HEAD = (%q, %v, %v), want %q", wtHead, ok, err, env.headSHA)
	}

	planContent, err := os.ReadFile(rt.Store.PlanPath("api", 1))
	if err != nil || string(planContent) != "# Round 1 Plan\nDo stuff" {
		t.Fatalf("plan content: got (%q, %v)", string(planContent), err)
	}

	env.runner.mu.Lock()
	specs := env.runner.specs
	env.runner.mu.Unlock()
	if len(specs) != 1 {
		t.Fatalf("runner specs count = %d, want 1", len(specs))
	}
	if specs[0].Dir != b.Worktree {
		t.Fatalf("runner spec Dir = %q, want %q", specs[0].Dir, b.Worktree)
	}
}

func TestRoundStartSetsShippedTags(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	// The client tags its base commit, and also ships a tag for a commit the
	// server has never seen: the round must still start, that tag skipped.
	runGit(t, env.clientDir, "-c", "tag.gpgsign=false", "tag", "v1.2.3", env.headSHA)
	missingSHA := strings.Repeat("f", 40)

	outRef := "refs/relay/api/out"
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	snap, err := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	bundleBytes, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatalf("read snap body: %v", err)
	}

	tagsJSON, err := json.Marshal([]remote.TagRef{
		{Name: "v1.2.3", SHA: env.headSHA},
		{Name: "unrelated", SHA: missingSHA},
		{Name: "release/1.0", SHA: env.headSHA},
		{Name: "..", SHA: env.headSHA},
	})
	if err != nil {
		t.Fatal(err)
	}

	formBytes, ct := makeRoundFormTags(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes, string(tagsJSON))
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start round status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}

	got, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/v1.2.3")
	if err != nil || !ok {
		t.Fatalf("bare refs/tags/v1.2.3: %v, ok=%v", err, ok)
	}
	if got != env.headSHA {
		t.Fatalf("refs/tags/v1.2.3 = %q, want the base sha %q", got, env.headSHA)
	}

	if _, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/unrelated"); err != nil || ok {
		t.Fatalf("refs/tags/unrelated: ok=%v err=%v, want it absent", ok, err)
	}

	got, ok, err = env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/release/1.0")
	if err != nil || !ok {
		t.Fatalf("bare refs/tags/release/1.0: %v, ok=%v", err, ok)
	}
	if got != env.headSHA {
		t.Fatalf("refs/tags/release/1.0 = %q, want the base sha %q", got, env.headSHA)
	}

	if _, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/.."); err != nil || ok {
		t.Fatalf("refs/tags/..: ok=%v err=%v, want it absent (skipped, invalid tag)", ok, err)
	}

	// release/1.0 now shares headSHA with v1.2.3, so "describe --tags" is
	// free to report either; check the worktree sees both tags instead.
	pointsAtHead := strings.TrimSpace(runGit(t, b.Worktree, "tag", "--points-at", "HEAD"))
	for _, want := range []string{"v1.2.3", "release/1.0"} {
		found := false
		for _, tag := range strings.Split(pointsAtHead, "\n") {
			if tag == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("worktree tag --points-at HEAD = %q, want it to include %q", pointsAtHead, want)
		}
	}
}

// makeRoundFormWithTier is makeRoundForm plus an optional "tier" field,
// written after "plan" and before "bundle" per the wire contract (#141
// remote half).
func makeRoundFormWithTier(t *testing.T, round int, plan, tier string, bundleBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if tier != "" {
		if err := mw.WriteField("tier", tier); err != nil {
			t.Fatal(err)
		}
	}
	if len(bundleBytes) > 0 {
		part, err := mw.CreateFormFile("bundle", "bundle.bundle")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bundleBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func TestRoundStartHonoursTierField(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	rt := env.runtime(t)
	before, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if before.Tier != "harness" {
		t.Fatalf("binding stored at Tier = %q, want harness", before.Tier)
	}

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundFormWithTier(t, 1, "# Round 1 Plan\nDo stuff", "edit", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start round status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	env.runner.mu.Lock()
	specs := env.runner.specs
	env.runner.mu.Unlock()
	if len(specs) != 1 {
		t.Fatalf("runner specs count = %d, want 1", len(specs))
	}
	if !slices.Contains(specs[0].Argv, "--permission-mode") || !slices.Contains(specs[0].Argv, "acceptEdits") {
		t.Fatalf("runner spec Argv = %v, want --permission-mode acceptEdits", specs[0].Argv)
	}

	after, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if after.RoundTier != "edit" {
		t.Fatalf("binding RoundTier = %q, want edit", after.RoundTier)
	}
}

func TestRoundStartTierAboveMaxIs422(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundFormWithTier(t, 1, "# Round 1 Plan\nDo stuff", "yolo", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("start round status = %d, want 422; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeTierAboveMax {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTierAboveMax)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if relay.RoundStateOf(b, entries) != remote.RoundIdle {
		t.Fatalf("round state = %v, want idle", relay.RoundStateOf(b, entries))
	}

	env.runner.mu.Lock()
	specsLen := len(env.runner.specs)
	env.runner.mu.Unlock()
	if specsLen != 0 {
		t.Fatalf("runner specs count = %d, want 0", specsLen)
	}
}

func TestRoundStartWhileRunningIs409(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first start status = %d, want 201", resp.StatusCode)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second start status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

func TestRoundResendSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	reportText := "# Report 1\nDone.\n\n```relay\nstatus: done\n```\n"
	_ = os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644)
	_ = os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644)
	env.runner.setAlive(false)
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	resendBytes, ct2 := makeRoundForm(t, 1, "# Plan 1", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", resendBytes, ct2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resend status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
}

// TestRoundStartWithABrokenRunnerAcceptsThenHaltsAsync pins #285's async
// admit contract, which supersedes #250 items 1 and 3's synchronous
// guarantee (this test used to be TestRoundResendThatCannotStartIs409 and
// asserted the opposite): Send now only stages a round (Defer); the spawn
// happens in admit, after the round is already accepted, so a spawn
// failure no longer fails the send itself. POST /rounds returns 201 and
// the binding halts to needs_you -- visible on this very response because
// a single binding under the cap admits synchronously within the same
// request, and durably in the store either way. The round's plan log entry
// stays: the round really was staged, only the spawn (a later, separate
// step) failed.
//
// Mutation check: make admit's per-round spawn failure propagate out of
// admit() as handleStartRound's send error, and this test must fail on
// status != 201.
func TestRoundStartWithABrokenRunnerAcceptsThenHaltsAsync(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	outRef := "refs/relay/api/out"
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	snap, err := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	bundleBytes, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatalf("read snap body: %v", err)
	}

	env.runner.mu.Lock()
	env.runner.startErr = errors.New("boom: no such binary")
	env.runner.mu.Unlock()

	formBytes, ct := makeRoundForm(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes)
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start round status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal binding view: %v; body: %s", err, string(body))
	}
	if view.State != string(store.StateNeedsYou) {
		t.Errorf("response state = %q, want needs_you", view.State)
	}
	if view.Halt == "" || !strings.Contains(view.Halt, "spawn failed") {
		t.Errorf("response Halt = %q, want it to contain %q", view.Halt, "spawn failed")
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateNeedsYou {
		t.Errorf("binding state = %s, want needs_you", b.State)
	}
	if b.Halt == "" || !strings.Contains(b.Halt, "spawn failed") {
		t.Errorf("binding Halt = %q, want it to contain %q", b.Halt, "spawn failed")
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var sawPlan bool
	for _, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPlan {
			sawPlan = true
		}
	}
	if !sawPlan {
		t.Error("want a round 1 plan log entry: the round was staged, only the spawn failed")
	}
}

func TestRoundResendDifferentPlanIs409(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	reportText := "# Report 1\nDone.\n\n```relay\nstatus: done\n```\n"
	_ = os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644)
	_ = os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644)
	env.runner.setAlive(false)
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	resendBytes, ct2 := makeRoundForm(t, 1, "# Different Plan", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", resendBytes, ct2)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resend status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundStarted {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundStarted)
	}
}

func TestRoundStartNotFastForward(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "commit", "--allow-empty", "-m", "server commit")

	reportText := "# Report 1\nDone.\n\n```relay\nstatus: done\n```\n"
	_ = os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644)
	_ = os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644)
	env.runner.setAlive(false)
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Client commits on top of initial commit (diverging from server branch)
	clientFile := filepath.Join(env.clientDir, "client.txt")
	if err := os.WriteFile(clientFile, []byte("client divergence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, env.clientDir, "add", "client.txt")
	runGit(t, env.clientDir, "commit", "-m", "client commit")
	clientHead, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("client HEAD: %v, ok=%v", err, ok)
	}
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, clientHead, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}

	snap2, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes2, _ := io.ReadAll(snap2.Body)
	_ = snap2.Body.Close()

	formBytes2, ct2 := makeRoundForm(t, 2, "# Plan 2", bundleBytes2)
	resp2, body2 := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes2, ct2)
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("start not-ff status = %d, want 422; body: %s", resp2.StatusCode, string(body2))
	}
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body2, &errBody)
	if errBody.Code != remote.CodeNotFastForward {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeNotFastForward)
	}
}

func TestRoundCloseServesFilesBundleAck(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}

	workFile := filepath.Join(b.Worktree, "result.txt")
	if err := os.WriteFile(workFile, []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "round 1 result")

	reportText := "# Report 1\nCompleted work.\n\n```relay\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	streamText := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n" +
		`{"type":"error","message":"Unexpected server error"}` + "\n"
	if err := os.WriteFile(rt.Store.BuilderStreamPath("api", 1), []byte(streamText), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get binding status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.RoundState != remote.RoundClosed {
		t.Fatalf("round_state = %q, want %q", view.RoundState, remote.RoundClosed)
	}
	if view.ClosedRound != 1 {
		t.Fatalf("closed_round = %d, want 1", view.ClosedRound)
	}
	bareBranchSHA, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/relay/api")
	if err != nil || !ok {
		t.Fatalf("bare branch sha: %v, ok=%v", err, ok)
	}
	if view.ResultCommit != bareBranchSHA {
		t.Fatalf("result_commit = %q, want %q", view.ResultCommit, bareBranchSHA)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/report", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get report status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "status: done") {
		t.Fatalf("report body missing status: done:\n%s", string(body))
	}

	resp, _ = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/diff", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get diff status = %d, want 200", resp.StatusCode)
	}

	resp, _ = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get log status = %d, want 200", resp.StatusCode)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/stream", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get stream status = %d, want 200", resp.StatusCode)
	}
	if string(body) != streamText {
		t.Fatalf("stream body = %q, want %q", string(body), streamText)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/bundle?since="+env.headSHA, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get bundle status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	moved, err := env.transport.Absorb(ctx, env.clientDir, remote.ContentTypeGitBundle, bytes.NewReader(body), []string{"refs/heads/relay/api"})
	if err != nil {
		t.Fatalf("client Absorb: %v", err)
	}
	if moved["refs/heads/relay/api"] != view.ResultCommit {
		t.Fatalf("client moved branch = %q, want %q", moved["refs/heads/relay/api"], view.ResultCommit)
	}
	clientHeadSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/heads/relay/api")
	if err != nil || !ok || clientHeadSHA != view.ResultCommit {
		t.Fatalf("client branch sha = %q, want %q", clientHeadSHA, view.ResultCommit)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}
	var ackView remote.BindingView
	_ = json.Unmarshal(body, &ackView)
	if ackView.AckedRound != 1 {
		t.Fatalf("acked_round = %d, want 1", ackView.AckedRound)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get binding status = %d, want 200", resp.StatusCode)
	}
	var idleView remote.BindingView
	_ = json.Unmarshal(body, &idleView)
	if idleView.RoundState != remote.RoundIdle {
		t.Fatalf("round_state = %q, want %q", idleView.RoundState, remote.RoundIdle)
	}
}

// fakeStreamUsage is a usage.Reader fake: it answers a headless source
// pointed at the round's stream with its samples, and nothing otherwise,
// recording every source it was asked to read.
type fakeStreamUsage struct {
	samples []usage.Sample
	note    string
	sources []usage.Source
}

func (f *fakeStreamUsage) Read(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.sources = append(f.sources, src)
	if src.Mode == usage.ModeHeadless && strings.HasSuffix(src.StreamPath, "001-builder.jsonl") {
		return f.samples, f.note
	}
	return nil, "not the round's stream"
}

func (f *fakeStreamUsage) Peek(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	return nil, ""
}

// TestRoundCloseRecordsStreamUsage checks that the server measures a
// closed remote round from the headless builder's stream (#216): the
// report entry it queues at close carries the summed tokens and a
// measured/estimated cost, not "no reader".
func TestRoundCloseRecordsStreamUsage(t *testing.T) {
	prices := usage.Prices{Models: map[string]usage.ModelPrice{
		"anthropic/haiku": {In: 1, Out: 5},
	}}
	fu := &fakeStreamUsage{
		samples: []usage.Sample{
			{Provider: "anthropic", Model: "haiku", Tokens: usage.Tokens{In: 100, Out: 200}, USD: 0.05, HasCost: true},
			{Provider: "anthropic", Model: "haiku", Tokens: usage.Tokens{In: 1_000_000}},
		},
	}
	env := setupTestEnv(t, func(c *Config) {
		c.Usage = fu
		c.Prices = prices
	})
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}

	workFile := filepath.Join(b.Worktree, "result.txt")
	if err := os.WriteFile(workFile, []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "round 1 result")

	reportText := "# Report 1\nCompleted work.\n\n```relay\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// The reader was asked about the round's stream, headless.
	asked := false
	for _, src := range fu.sources {
		if src.Mode == usage.ModeHeadless && strings.HasSuffix(src.StreamPath, "001-builder.jsonl") {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("reader never asked for the round's stream; sources: %+v", fu.sources)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var report *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindReport {
			report = &entries[i]
		}
	}
	if report == nil {
		t.Fatal("no report entry after the round closed")
	}
	u := report.Usage
	if u == nil {
		t.Fatalf("report entry Usage = nil, want the stream's figure; note %q", report.Note)
	}
	if u.Tokens.In != 1_000_100 || u.Tokens.Out != 200 {
		t.Fatalf("Usage.Tokens = %+v, want in 1000100 out 200", u.Tokens)
	}
	if u.Cost.Basis != usage.Estimated {
		t.Fatalf("Usage.Cost.Basis = %q, want estimated", u.Cost.Basis)
	}
	// One measured sample (0.05) plus one estimated at 1/M input tokens
	// over 1M input tokens (1.00).
	if u.Cost.USD != 1.05 {
		t.Fatalf("Usage.Cost.USD = %v, want 1.05", u.Cost.USD)
	}
	if u.Model != "haiku" {
		t.Fatalf("Usage.Model = %q, want haiku", u.Model)
	}
}

func TestRoundCloseDirtyShipsSideRef(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(b.Worktree, "untracked.txt"), []byte("dirty content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reportText := "# Report 1\nDone.\n\n```relay\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get binding status = %d, want 200", resp.StatusCode)
	}
	var view remote.BindingView
	_ = json.Unmarshal(body, &view)
	if view.DirtyCommit == "" {
		t.Fatal("view.DirtyCommit is empty, want non-empty")
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/bundle?since="+env.headSHA, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get bundle status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	inboundRefs := []string{"refs/heads/relay/api", "refs/relay/api/round-1"}
	moved, err := env.transport.Absorb(ctx, env.clientDir, remote.ContentTypeGitBundle, bytes.NewReader(body), inboundRefs)
	if err != nil {
		t.Fatalf("Absorb: %v", err)
	}
	if _, movedBranch := moved["refs/heads/relay/api"]; movedBranch {
		t.Fatal("branch was unexpectedly moved in absorb")
	}

	clientBranchSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/heads/relay/api")
	if ok && clientBranchSHA != env.headSHA {
		t.Fatalf("client branch sha = %q, want %q", clientBranchSHA, env.headSHA)
	}

	sideSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/relay/api/round-1")
	if err != nil || !ok || sideSHA != view.DirtyCommit {
		t.Fatalf("side ref sha = (%q, %v, %v), want %q", sideSHA, ok, err, view.DirtyCommit)
	}
	catOut := runGit(t, env.clientDir, "cat-file", "-p", sideSHA)
	if !strings.Contains(catOut, "parent "+env.headSHA) {
		t.Fatalf("side ref commit missing parent %q:\n%s", env.headSHA, catOut)
	}
}

func TestFilesBeforeCloseIs404(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	for _, kind := range []string{"report", "diff", "plan", "stream"} {
		resp, _ := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/"+kind, nil, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET files/%s status = %d, want 404", kind, resp.StatusCode)
		}
	}
}

func TestBundleWrongRoundIs404(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	reportText := "# Report 1\nDone.\n\n```relay\nstatus: done\n```\n"
	_ = os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644)
	_ = os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644)
	env.runner.setAlive(false)
	_ = env.srv.Tick(ctx)

	resp, _ = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/2/bundle", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET round 2 bundle status = %d, want 404", resp.StatusCode)
	}
}

func TestAckUnclosedIs409(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relay/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ack status = %d, want 409; body: %s", resp.StatusCode, string(body))
	}
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

func TestTickWalksEveryOwner(t *testing.T) {
	ctx := context.Background()
	gitClient := git.NewClient("git", 0, 0)

	serverRoot := t.TempDir()
	cJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	candPath := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(candPath, []byte(cJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	srv, err := New(Config{
		Root:       serverRoot,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Owner A
	clientDirA := t.TempDir()
	runGit(t, clientDirA, "init")
	runGit(t, clientDirA, "config", "user.name", "testA")
	runGit(t, clientDirA, "config", "user.email", "testA@example.com")
	_ = os.WriteFile(filepath.Join(clientDirA, "f.txt"), []byte("a\n"), 0o644)
	runGit(t, clientDirA, "add", "f.txt")
	runGit(t, clientDirA, "commit", "-m", "init a")
	headA, _, _ := gitClient.RefSHA(ctx, clientDirA, "HEAD")
	rootA, _ := gitClient.RootCommit(ctx, clientDirA)
	repoIDA, _ := remote.RepoID(rootA)

	kpA, _ := remote.Generate()
	idA := remote.IDOf(kpA.Public)
	_, _ = srv.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now())

	createBodyA, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "binding-a",
		RepoID:     repoIDA,
		BaseCommit: headA,
	})
	doSigned(t, ts, kpA, "POST", "/v1/bindings", createBodyA, "application/json")
	_ = gitClient.UpdateRef(ctx, clientDirA, "refs/relay/binding-a/out", headA, "")
	transA := remote.NewBundleTransport(gitClient, t.TempDir())
	snapA, _ := transA.Snapshot(ctx, clientDirA, []string{"refs/relay/binding-a/out"}, "")
	bytesA, _ := io.ReadAll(snapA.Body)
	_ = snapA.Body.Close()
	formA, ctA := makeRoundForm(t, 1, "# Plan A", bytesA)
	doSigned(t, ts, kpA, "POST", "/v1/bindings/binding-a/rounds", formA, ctA)

	// Owner B
	clientDirB := t.TempDir()
	runGit(t, clientDirB, "init")
	runGit(t, clientDirB, "config", "user.name", "testB")
	runGit(t, clientDirB, "config", "user.email", "testB@example.com")
	_ = os.WriteFile(filepath.Join(clientDirB, "f.txt"), []byte("b\n"), 0o644)
	runGit(t, clientDirB, "add", "f.txt")
	runGit(t, clientDirB, "commit", "-m", "init b")
	headB, _, _ := gitClient.RefSHA(ctx, clientDirB, "HEAD")
	rootB, _ := gitClient.RootCommit(ctx, clientDirB)
	repoIDB, _ := remote.RepoID(rootB)

	kpB, _ := remote.Generate()
	idB := remote.IDOf(kpB.Public)
	_, _ = srv.clients.Add("bob", remote.MarshalPublic(kpB.Public, "bob"), time.Now())

	createBodyB, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "binding-b",
		RepoID:     repoIDB,
		BaseCommit: headB,
	})
	doSigned(t, ts, kpB, "POST", "/v1/bindings", createBodyB, "application/json")
	_ = gitClient.UpdateRef(ctx, clientDirB, "refs/relay/binding-b/out", headB, "")
	transB := remote.NewBundleTransport(gitClient, t.TempDir())
	snapB, _ := transB.Snapshot(ctx, clientDirB, []string{"refs/relay/binding-b/out"}, "")
	bytesB, _ := io.ReadAll(snapB.Body)
	_ = snapB.Body.Close()
	formB, ctB := makeRoundForm(t, 1, "# Plan B", bytesB)
	doSigned(t, ts, kpB, "POST", "/v1/bindings/binding-b/rounds", formB, ctB)

	// Owner C: enrolled client with no bindings
	kpC, _ := remote.Generate()
	_, _ = srv.clients.Add("charlie", remote.MarshalPublic(kpC.Public, "charlie"), time.Now())

	// Both owners have 1 running binding. Check initial alive calls count.
	runner.mu.Lock()
	initialAliveCount := len(runner.aliveHandles)
	runner.mu.Unlock()

	// Call s.Tick
	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Assert runner.Alive was called for A and B only, not for C (exactly 2 calls)
	runner.mu.Lock()
	newAliveHandles := runner.aliveHandles[initialAliveCount:]
	specsCount := len(runner.specs)
	runner.mu.Unlock()

	if len(newAliveHandles) != 2 {
		t.Fatalf("new alive calls = %d, want exactly 2", len(newAliveHandles))
	}
	if specsCount != 2 {
		t.Fatalf("runner specs count = %d, want 2", specsCount)
	}

	rtA := testRuntime(t, srv, idA)
	bA, _ := rtA.Store.Load("binding-a")
	rtB := testRuntime(t, srv, idB)
	bB, _ := rtB.Store.Load("binding-b")

	seenA, seenB := false, false
	for _, h := range newAliveHandles {
		if h.PID == bA.Builder.PID {
			seenA = true
		}
		if h.PID == bB.Builder.PID {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("seenA=%v, seenB=%v; want both true", seenA, seenB)
	}
}

func TestTickSkipsMissingBindingsDir(t *testing.T) {
	ctx := context.Background()
	serverRoot := t.TempDir()
	srv, err := New(Config{
		Root: serverRoot,
		Now:  time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("Tick on missing bindings dir returned error: %v", err)
	}
}

func TestCandidatesView(t *testing.T) {
	root := t.TempDir()

	candPath := filepath.Join(root, "candidates.json")
	candJSON := `[
		{"harness": "claude", "provider": "anthropic", "model": "haiku", "roles": ["builder"]},
		{"harness": "claude", "provider": "anthropic", "model": "sonnet", "roles": ["builder"]}
	]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}

	pol := policy.Policy{
		Order: map[string][]string{
			"builder": {"claude/anthropic/haiku", "claude/anthropic/sonnet"},
		},
	}

	now := time.Now()
	// Write a gate on haiku to the server-wide ledger
	l := ledger.Ledger{
		Entries: []ledger.Entry{
			{
				Kind:    ledger.SpawnFailed,
				Subject: "claude/anthropic/haiku",
				Source:  "relay",
				At:      now,
				Until:   now.Add(time.Hour),
				Note:    "test failure",
			},
		},
	}
	if err := ledger.Save(filepath.Join(root, "ledger.json"), l); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		Root:       root,
		Candidates: cSet,
		Policy:     pol,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}

	req := signedRequest(t, kp, "GET", "/v1/candidates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp remote.CandidatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(resp.Candidates))
	}

	haiku := resp.Candidates[0]
	sonnet := resp.Candidates[1]

	if haiku.Token != "claude/anthropic/haiku" || !haiku.Gated || haiku.Pick {
		t.Errorf("haiku = %+v, want Token=claude/anthropic/haiku, Gated=true, Pick=false", haiku)
	}
	if sonnet.Token != "claude/anthropic/sonnet" || sonnet.Gated || !sonnet.Pick {
		t.Errorf("sonnet = %+v, want Token=claude/anthropic/sonnet, Gated=false, Pick=true", sonnet)
	}

	pickCount := 0
	for _, c := range resp.Candidates {
		if c.Pick {
			pickCount++
		}
	}
	if pickCount != 1 {
		t.Errorf("pickCount = %d, want exactly 1", pickCount)
	}
}
