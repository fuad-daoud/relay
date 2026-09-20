package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPointerRoundTrip(t *testing.T) {
	defaultRoot := t.TempDir()
	startedAt := time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC)
	want := DaemonPointer{
		Root:      "/srv/data/relay-serve/serve",
		PID:       4242,
		Listen:    ":7777",
		StartedAt: startedAt,
	}
	if err := WritePointer(defaultRoot, want); err != nil {
		t.Fatalf("WritePointer: %v", err)
	}
	got, ok, err := ReadPointer(defaultRoot)
	if err != nil {
		t.Fatalf("ReadPointer: %v", err)
	}
	if !ok {
		t.Fatal("ReadPointer ok = false, want true")
	}
	if got != want {
		t.Errorf("ReadPointer = %+v, want %+v", got, want)
	}
	if err := RemovePointer(defaultRoot); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	_, ok, err = ReadPointer(defaultRoot)
	if err != nil {
		t.Fatalf("ReadPointer after remove: %v", err)
	}
	if ok {
		t.Error("ReadPointer after remove ok = true, want false")
	}

	malformedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(malformedRoot, PointerFileName), []byte("{"), 0o644); err != nil {
		t.Fatalf("write malformed pointer: %v", err)
	}
	if _, _, err := ReadPointer(malformedRoot); err == nil {
		t.Error("ReadPointer malformed JSON error = nil, want non-nil")
	}
}

func TestInitialised(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name  string
		setup func(string) error
		want  bool
	}{
		{"clients.json file", func(root string) error { return os.WriteFile(filepath.Join(root, "clients.json"), nil, 0o644) }, true},
		{"server.key file", func(root string) error { return os.WriteFile(filepath.Join(root, "server.key"), nil, 0o600) }, true},
		{"bindings directory", func(root string) error { return os.Mkdir(filepath.Join(root, "bindings"), 0o755) }, true},
		{"empty directory", func(string) error { return nil }, false},
		{"missing directory", func(string) error { return nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(base, tc.name)
			if tc.name != "missing directory" {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.setup(root); err != nil {
				t.Fatal(err)
			}
			got, err := Initialised(root)
			if err != nil {
				t.Fatalf("Initialised: %v", err)
			}
			if got != tc.want {
				t.Errorf("Initialised = %v, want %v", got, tc.want)
			}
		})
	}
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
			defaultRoot := filepath.Join(t.TempDir(), "default")
			if tc.pointer != nil {
				p := *tc.pointer
				if p.Root == "default" {
					p.Root = defaultRoot
				}
				if err := WritePointer(defaultRoot, p); err != nil {
					t.Fatalf("WritePointer: %v", err)
				}
			}
			wantRoot := tc.wantRoot
			if wantRoot == "default" {
				wantRoot = defaultRoot
			}
			root, note, err := ResolveAdminRoot(tc.explicitState, defaultRoot, func(int) bool { return tc.alive })
			if err != nil {
				t.Fatalf("ResolveAdminRoot: %v", err)
			}
			if root != wantRoot || note != tc.wantNote {
				t.Errorf("ResolveAdminRoot = (%q, %q), want (%q, %q)", root, note, wantRoot, tc.wantNote)
			}
		})
	}
}
