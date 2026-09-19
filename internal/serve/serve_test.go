package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/remote"
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
	bareRepoPath := filepath.Join(root, "repos", string(id), "repo123.git")
	if _, err := os.Stat(filepath.Join(bareRepoPath, "HEAD")); err != nil {
		t.Fatalf("bare repo HEAD missing at %s: %v", bareRepoPath, err)
	}

	// binding.json has Owner
	b, err := s.runtime(id).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Owner != string(id) {
		t.Fatalf("binding.Owner = %q, want %q", b.Owner, id)
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
	b, err := s.runtime(id).Store.Load("api")
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

	bAfter, err := s.runtime(id).Store.Load("api")
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
	ownerLedgerPath := filepath.Join(root, "bindings", string(idA), "ledger.json")
	if _, err := os.Stat(ownerLedgerPath); err == nil {
		t.Fatalf("per-owner ledger.json unexpectedly exists at %s", ownerLedgerPath)
	}
}
