package ui

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/serve"
)

// TestPlannerSourceResolvesEveryKey: a planner source answers every key
// with the one runtime and the key itself -- there is no branch on a
// planner.
// TestServerSourceRuntimeSplitsKey: a server source splits "owner/name"
// into the owner's runtime and the bare binding name, and refuses keys it
// cannot split or whose owner id is malformed.
func TestServerSourceRuntimeSplitsKey(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)

	src := ServerSource(srv)
	rt, name, ok := src.Runtime(string(id) + "/persist")
	if !ok {
		t.Fatal("Runtime(<id>/persist) = !ok, want ok")
	}
	if name != "persist" {
		t.Errorf("name = %q, want persist", name)
	}
	if rt.Store == nil {
		t.Error("resolved runtime has no Store")
	}

	if _, err := src.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

// TestServerSourceRuntimeRefusesBadKeys covers the !ok cases separately so
// a failure names the offending key.
func TestServerSourceRuntimeRefusesBadKeys(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)

	src := ServerSource(srv)
	for _, key := range []string{"persist", string(id) + "/", "bogus/x"} {
		if _, _, ok := src.Runtime(key); ok {
			t.Errorf("Runtime(%q) = ok, want !ok", key)
		}
	}
}

// TestPlannerSourceBaseIsRuntime pins the contract: on a planner Base is
// the runtime itself, so scope all reads the planner's own database (§2).
// TestServerSourceBaseHasNoDB pins the contract: a server's Base carries
// no database -- the server box does not run relay.db, so scope all is
// refused there with the existing "no database" notice (§2).
func TestServerSourceBaseHasNoDB(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if base := ServerSource(srv).Base(); base.DB != nil {
		t.Fatalf("serverSource.Base().DB = %v, want nil", base.DB)
	}
}
