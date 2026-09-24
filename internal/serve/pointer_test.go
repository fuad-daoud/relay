package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestDaemonPointerKVRoundTrip(t *testing.T) {
	d := testServeDB(t)
	startedAt := time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC)
	want := DaemonPointer{
		Root:      "/srv/data/relevo-serve/serve",
		PID:       4242,
		Listen:    ":7777",
		StartedAt: startedAt,
	}
	if err := WriteDaemonPointer(d, want); err != nil {
		t.Fatalf("WriteDaemonPointer: %v", err)
	}
	got, ok, err := ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer: %v", err)
	}
	if !ok {
		t.Fatal("ReadDaemonPointer ok = false, want true")
	}
	if got != want {
		t.Errorf("ReadDaemonPointer = %+v, want %+v", got, want)
	}
	if err := RemoveDaemonPointer(d); err != nil {
		t.Fatalf("RemoveDaemonPointer: %v", err)
	}
	_, ok, err = ReadDaemonPointer(d)
	if err != nil {
		t.Fatalf("ReadDaemonPointer after remove: %v", err)
	}
	if ok {
		t.Error("ReadDaemonPointer after remove ok = true, want false")
	}
}

// TestReadPointerFile pins the legacy file reader that only internal/migrate
// still uses (P5 D1).
func TestReadPointerFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, PointerFileName), []byte(`{"root":"/srv/serve","pid":4242}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadPointer(root)
	if err != nil || !ok {
		t.Fatalf("ReadPointer = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if got.Root != "/srv/serve" || got.PID != 4242 {
		t.Errorf("ReadPointer = %+v, want root /srv/serve pid 4242", got)
	}

	if _, ok, err := ReadPointer(t.TempDir()); err != nil || ok {
		t.Errorf("ReadPointer(missing) = (ok %v, err %v), want (false, nil)", ok, err)
	}

	malformedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(malformedRoot, PointerFileName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPointer(malformedRoot); err == nil {
		t.Error("ReadPointer malformed JSON error = nil, want non-nil")
	}
}

func TestInitialised(t *testing.T) {
	t.Run("empty database and root", func(t *testing.T) {
		got, err := Initialised(t.TempDir(), testServeDB(t))
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if got {
			t.Error("Initialised = true, want false")
		}
	})

	t.Run("clients kv row", func(t *testing.T) {
		d := testServeDB(t)
		if err := d.KVPut(clientsKVKey, []byte("[]")); err != nil {
			t.Fatal(err)
		}
		got, err := Initialised(t.TempDir(), d)
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if !got {
			t.Error("Initialised = false, want true for a serve.clients row")
		}
	})

	t.Run("tls key secret", func(t *testing.T) {
		d := testServeDB(t)
		if err := d.Tx(func(tx *db.Tx) error {
			return tx.SecretPut(tlsKeySecret, []byte("key"), time.Now().UTC())
		}); err != nil {
			t.Fatal(err)
		}
		got, err := Initialised(t.TempDir(), d)
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if !got {
			t.Error("Initialised = false, want true for a serve.tls.key secret")
		}
	})

	t.Run("bindings directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "bindings"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := Initialised(root, testServeDB(t))
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if !got {
			t.Error("Initialised = false, want true for a bindings directory")
		}
	})

	t.Run("legacy clients.json file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "clients.json"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Initialised(root, testServeDB(t))
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if !got {
			t.Error("Initialised = false, want true for a legacy clients.json")
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		got, err := Initialised(filepath.Join(t.TempDir(), "nope"), testServeDB(t))
		if err != nil {
			t.Fatalf("Initialised: %v", err)
		}
		if got {
			t.Error("Initialised = true, want false")
		}
	})
}

func TestResolveAdminRoot(t *testing.T) {
	cases := []struct {
		name          string
		explicitState string
		pointer       *DaemonPointer
		alive         bool
		wantRoot      string
		wantNote      string
	}{
		{"explicit state", "/explicit", nil, false, "/explicit/serve", ""},
		{"missing pointer", "", nil, false, "default", ""},
		{"live different pointer", "", &DaemonPointer{Root: "/daemon/serve", PID: 4242}, true, "/daemon/serve", "using the running daemon's state: /daemon/serve (pid 4242)"},
		{"live default pointer", "", &DaemonPointer{Root: "default", PID: 4242}, true, "default", ""},
		{"stale pointer", "", &DaemonPointer{Root: "/daemon/serve", PID: 4242}, false, "default", "stale daemon pointer: /daemon/serve (pid 4242 is not running)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testServeDB(t)
			defaultRoot := filepath.Join(t.TempDir(), "default")
			if tc.pointer != nil {
				p := *tc.pointer
				if p.Root == "default" {
					p.Root = defaultRoot
				}
				if err := WriteDaemonPointer(d, p); err != nil {
					t.Fatalf("WriteDaemonPointer: %v", err)
				}
			}
			wantRoot := tc.wantRoot
			if wantRoot == "default" {
				wantRoot = defaultRoot
			}
			root, note, err := ResolveAdminRoot(tc.explicitState, d, defaultRoot, func(int) bool { return tc.alive })
			if err != nil {
				t.Fatalf("ResolveAdminRoot: %v", err)
			}
			if root != wantRoot || note != tc.wantNote {
				t.Errorf("ResolveAdminRoot = (%q, %q), want (%q, %q)", root, note, wantRoot, tc.wantNote)
			}
		})
	}
}
