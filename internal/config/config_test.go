package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return Open(d)
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedConfigDir writes one of every file an import consumes, plus a client.pub
// and a hooks directory.
func seedConfigDir(t *testing.T, dir string) {
	t.Helper()

	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"),
		`{"order":{"builder":["claude/anthropic/sonnet"]}}`, 0o644)
	writeFile(t, filepath.Join(dir, "roles.json"), `{}`, 0o644)
	writeFile(t, filepath.Join(dir, "prices.json"),
		`{"as_of":"2026-01-01","source":"file","models":{}}`, 0o644)
	writeFile(t, filepath.Join(dir, "servers.json"),
		`{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`, 0o644)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("remote.MarshalPrivate: %v", err)
	}
	writeFile(t, filepath.Join(dir, "client.key"), string(pem), 0o600)
	writeFile(t, filepath.Join(dir, "client.pub"), "ed25519 fake\n", 0o644)
	writeFile(t, filepath.Join(dir, "typesafe.key"), "  test-key  \n", 0o600)

	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "10-first"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "05-zero"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "hooks", "state_changed.d", "20-data"), "not executable\n", 0o644)
	writeFile(t, filepath.Join(dir, "hooks", "round_started.d", "only"), "#!/bin/sh\n", 0o755)
}

func TestImportAllFiles(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	seedConfigDir(t, dir)
	now := time.Now().UTC()

	res, err := s.ImportFiles(dir, now)
	if err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	if !contains(res.Imported, "hooks") {
		t.Errorf("Imported = %v, want it to name hooks", res.Imported)
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if L.Candidates.Len() != 1 || L.Candidates.Refs()[0] != "claude/anthropic/sonnet" {
		t.Errorf("Candidates = %v, want one claude candidate", L.Candidates.Refs())
	}
	if got := L.Policy.OrderFor("builder"); !reflect.DeepEqual(got, []string{"claude/anthropic/sonnet"}) {
		t.Errorf("order.builder = %v", got)
	}
	if L.RolesFile == nil {
		t.Error("RolesFile = nil, want the stored roles section")
	}
	if L.Prices.Source != "file" || L.Prices.AsOf != "2026-01-01" {
		t.Errorf("Prices = %+v, want the file's source and as_of", L.Prices)
	}
	if _, ok := L.Servers["zen"]; !ok {
		t.Errorf("Servers = %v, want zen", L.Servers)
	}
	if L.ClientKey == nil {
		t.Error("ClientKey = nil, want the stored key")
	}
	if L.Typesafe != "test-key" {
		t.Errorf("Typesafe = %q, want test-key", L.Typesafe)
	}

	wantHooks := HooksMap{
		"state_changed": {
			{filepath.Join(dir, "hooks", "state_changed.d", "05-zero")},
			{filepath.Join(dir, "hooks", "state_changed.d", "10-first")},
		},
		"round_started": {
			{filepath.Join(dir, "hooks", "round_started.d", "only")},
		},
	}
	if !reflect.DeepEqual(L.Hooks, wantHooks) {
		t.Errorf("Hooks = %#v, want %#v", L.Hooks, wantHooks)
	}

	for _, name := range []string{
		"candidates.json", "policy.json", "roles.json", "prices.json",
		"servers.json", "client.key", "client.pub", "typesafe.key",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present after import (err=%v)", name, err)
		}
	}

	// The hooks scripts are never removed, so the config dir stays.
	if _, err := os.Stat(filepath.Join(dir, "hooks", "state_changed.d", "10-first")); err != nil {
		t.Errorf("hook script removed: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("config dir removed while hooks/ is inside it: %v", err)
	}
}

func TestImportInvalidPolicyWritesNothing(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"), `{"max_switches":-1}`, 0o644)

	_, err := s.ImportFiles(dir, time.Now().UTC())
	if err == nil {
		t.Fatal("ImportFiles with an invalid policy.json: want error, got nil")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "policy.json")) {
		t.Errorf("error %v does not name the offending path", err)
	}

	for _, name := range []string{"candidates.json", "policy.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed or missing after a refused import: %v", name, err)
		}
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if L.Candidates.Len() != 0 {
		t.Errorf("Candidates = %v, want nothing stored", L.Candidates.Refs())
	}
	if v, err := s.Version(); err != nil || v != 0 {
		t.Errorf("Version = (%d, %v), want (0, nil): nothing was written", v, err)
	}
}

func TestImportEmptyDirIsNoOp(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := s.ImportFiles(dir, time.Now().UTC())
	if err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	if len(res.Imported) != 0 || len(res.Removed) != 0 {
		t.Errorf("ImportResult = %+v, want empty", res)
	}
	if v, err := s.Version(); err != nil || v != 0 {
		t.Errorf("Version = (%d, %v), want (0, nil)", v, err)
	}
}

func TestImportLeavesBakFilesAlone(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "candidates.json.bak-20260101"), `[]`, 0o644)

	if _, err := s.ImportFiles(dir, time.Now().UTC()); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "candidates.json.bak-20260101")); err != nil {
		t.Errorf(".bak file removed or missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "candidates.json")); !os.IsNotExist(err) {
		t.Errorf("candidates.json still present (err=%v)", err)
	}
}

func TestLoadFilesEqualsLoadAfterImport(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	seedConfigDir(t, dir)

	fromFiles, err := LoadFiles(dir)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if _, err := s.ImportFiles(dir, time.Now().UTC()); err != nil {
		t.Fatalf("ImportFiles: %v", err)
	}
	fromDB, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(fromFiles.Candidates.Refs(), fromDB.Candidates.Refs()) {
		t.Errorf("Candidates differ: %v vs %v", fromFiles.Candidates.Refs(), fromDB.Candidates.Refs())
	}
	if !reflect.DeepEqual(fromFiles.Policy, fromDB.Policy) {
		t.Errorf("Policy differs: %+v vs %+v", fromFiles.Policy, fromDB.Policy)
	}
	if !reflect.DeepEqual(fromFiles.RolesFile, fromDB.RolesFile) {
		t.Errorf("RolesFile differs: %+v vs %+v", fromFiles.RolesFile, fromDB.RolesFile)
	}
	if !reflect.DeepEqual(fromFiles.Prices, fromDB.Prices) {
		t.Errorf("Prices differ: %+v vs %+v", fromFiles.Prices, fromDB.Prices)
	}
	if !reflect.DeepEqual(fromFiles.Servers, fromDB.Servers) {
		t.Errorf("Servers differ: %+v vs %+v", fromFiles.Servers, fromDB.Servers)
	}
	if !reflect.DeepEqual(fromFiles.Hooks, fromDB.Hooks) {
		t.Errorf("Hooks differ: %#v vs %#v", fromFiles.Hooks, fromDB.Hooks)
	}
	if !reflect.DeepEqual(fromFiles.ClientKey, fromDB.ClientKey) {
		t.Errorf("ClientKey differs")
	}
	if fromFiles.Typesafe != fromDB.Typesafe {
		t.Errorf("Typesafe differs: %q vs %q", fromFiles.Typesafe, fromDB.Typesafe)
	}
	if !reflect.DeepEqual(fromFiles.Warnings, fromDB.Warnings) {
		t.Errorf("Warnings differ: %v vs %v", fromFiles.Warnings, fromDB.Warnings)
	}
}

// TestImportTxFailureKeepsFiles is the §8 step 3 mutation guard: a failed
// import transaction must leave every file in place. The test holds a write
// transaction on the same database while ImportFiles runs, so its own
// BEGIN IMMEDIATE cannot take the write lock. Moving the file removal before
// the transaction makes this test fail.
func TestImportTxFailureKeepsFiles(t *testing.T) {
	s := openStore(t)
	dir := filepath.Join(t.TempDir(), "relevo")
	writeFile(t, filepath.Join(dir, "candidates.json"),
		`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`, 0o644)
	writeFile(t, filepath.Join(dir, "policy.json"),
		`{"order":{"builder":["claude/anthropic/sonnet"]}}`, 0o644)

	err := s.db.Tx(func(*db.Tx) error {
		_, ierr := s.ImportFiles(dir, time.Now().UTC())
		return ierr
	})
	if err == nil {
		t.Fatal("ImportFiles beside a held write transaction: want error, got nil")
	}
	if !errors.Is(err, db.ErrBusy) {
		t.Fatalf("ImportFiles error = %v, want it to wrap db.ErrBusy", err)
	}

	for _, name := range []string{"candidates.json", "policy.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed after a failed commit: %v", name, err)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
