package legacy

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRewriteJSON pins that only a JSON string value starting with the old
// directory changes, a longer name merely beginning with it does not, and the
// input bytes are never modified.
func TestRewriteJSON(t *testing.T) {
	pairs := []Prefix{{Old: "/a/old", New: "/a/new"}}
	body := []byte(`{"p":"/a/old/x","q":"/a/old","r":"/a/older/x","s":"pre /a/old/x","n":1}`)
	want := `{"p":"/a/new/x","q":"/a/new","r":"/a/older/x","s":"pre /a/old/x","n":1}`

	before := string(body)
	got := RewriteJSON(body, pairs)
	if string(got) != want {
		t.Errorf("RewriteJSON = %s, want %s", got, want)
	}
	if string(body) != before {
		t.Errorf("RewriteJSON modified its input: %s, want %s", body, before)
	}

	nilPairs := RewriteJSON(body, nil)
	if !bytes.Equal(nilPairs, body) {
		t.Errorf("RewriteJSON with no pairs = %s, want the input %s", nilPairs, body)
	}
}

// TestRootsPrefixes pins the pair order and the empty/equal-side drop rule.
func TestRootsPrefixes(t *testing.T) {
	t.Run("all four roots", func(t *testing.T) {
		r := Roots{OldState: "/s/old", NewState: "/s/new", OldConfig: "/c/old", NewConfig: "/c/new"}
		want := []Prefix{{Old: "/s/old", New: "/s/new"}, {Old: "/c/old", New: "/c/new"}}
		if got := r.Prefixes(); !reflect.DeepEqual(got, want) {
			t.Errorf("Prefixes() = %+v, want %+v", got, want)
		}
	})

	t.Run("empty OldConfig", func(t *testing.T) {
		r := Roots{OldState: "/s/old", NewState: "/s/new"}
		want := []Prefix{{Old: "/s/old", New: "/s/new"}}
		if got := r.Prefixes(); !reflect.DeepEqual(got, want) {
			t.Errorf("Prefixes() = %+v, want %+v", got, want)
		}
	})

	t.Run("Old == New is dropped", func(t *testing.T) {
		r := Roots{OldState: "/s", NewState: "/s", OldConfig: "/c/old", NewConfig: "/c/new"}
		want := []Prefix{{Old: "/c/old", New: "/c/new"}}
		if got := r.Prefixes(); !reflect.DeepEqual(got, want) {
			t.Errorf("Prefixes() = %+v, want %+v", got, want)
		}
	})

	t.Run("zero Roots", func(t *testing.T) {
		if got := (Roots{}).Prefixes(); len(got) != 0 {
			t.Errorf("Prefixes() = %+v, want none", got)
		}
	})
}

func TestStateRoot(t *testing.T) {
	home := "/home/u"

	t.Run("XDG_STATE_HOME set", func(t *testing.T) {
		getenv := func(key string) string {
			if key == "XDG_STATE_HOME" {
				return "/xdg/state"
			}
			return ""
		}
		want := filepath.Join("/xdg/state", "relay")
		if got := StateRoot(getenv, home); got != want {
			t.Errorf("StateRoot() = %q, want %q", got, want)
		}
	})

	t.Run("XDG_STATE_HOME unset", func(t *testing.T) {
		want := filepath.Join(home, ".local", "state", "relay")
		if got := StateRoot(func(string) string { return "" }, home); got != want {
			t.Errorf("StateRoot() = %q, want %q", got, want)
		}
	})
}

func TestConfigRoot(t *testing.T) {
	want := filepath.Join("/cfg", "relay")
	if got := ConfigRoot("/cfg"); got != want {
		t.Errorf("ConfigRoot() = %q, want %q", got, want)
	}
}

func TestProbeAllFourAbsent(t *testing.T) {
	got, err := Probe(rootsUnder(t.TempDir()))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != (Status{}) {
		t.Errorf("Probe = %+v, want every root absent", got)
	}
}

func TestProbeAllFourPresent(t *testing.T) {
	r := rootsUnder(t.TempDir())
	mkdirAll(t, r.OldState, r.NewState, r.OldConfig, r.NewConfig)
	got, err := Probe(r)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	want := Status{OldState: true, NewState: true, OldConfig: true, NewConfig: true}
	if got != want {
		t.Errorf("Probe = %+v, want %+v", got, want)
	}
}

func TestProbeNonDirectoryCountsAsPresent(t *testing.T) {
	r := rootsUnder(t.TempDir())
	if err := os.WriteFile(r.OldState, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(r)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !got.OldState {
		t.Errorf("Probe = %+v, want OldState true for a non-directory", got)
	}
	if got.NewState || got.OldConfig || got.NewConfig {
		t.Errorf("Probe = %+v, want only OldState true", got)
	}
}

func TestProbeUnreadableParentIsAnErrorNamingThePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	parent := filepath.Join(dir, "locked")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) }) // RemoveAll can't descend into 000
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}

	r := Roots{OldState: filepath.Join(parent, "relay")}
	_, err := Probe(r)
	if err == nil {
		t.Fatal("Probe over an unreadable parent must error")
	}
	if !strings.Contains(err.Error(), r.OldState) {
		t.Errorf("Probe error = %q, want it to name %q", err, r.OldState)
	}
}

// TestUnmigratedAndStaleTruthTable pins both predicates over all sixteen
// Status values.
func TestUnmigratedAndStaleTruthTable(t *testing.T) {
	tests := []struct {
		name           string
		oldState       bool
		newState       bool
		oldConfig      bool
		newConfig      bool
		wantUnmigrated bool
		wantStale      bool
	}{
		{"nothing", false, false, false, false, false, false},
		{"new config only", false, false, false, true, false, false},
		{"old config only", false, false, true, false, true, false},
		{"both config", false, false, true, true, false, true},
		{"new state only", false, true, false, false, false, false},
		{"new state, new config", false, true, false, true, false, false},
		{"new state, old config", false, true, true, false, false, false}, // NewState exists: config is in its DB
		{"new state, both config", false, true, true, true, false, true},
		{"old state only", true, false, false, false, true, false},
		{"old state, new config", true, false, false, true, true, false},
		{"old state, old config", true, false, true, false, true, false},
		{"old state, both config", true, false, true, true, true, true},
		{"both state", true, true, false, false, false, true},
		{"both state, new config", true, true, false, true, false, true},
		{"both state, old config", true, true, true, false, false, true}, // leftover old config is stale, not unmigrated
		{"all four", true, true, true, true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Status{
				OldState:  tt.oldState,
				NewState:  tt.newState,
				OldConfig: tt.oldConfig,
				NewConfig: tt.newConfig,
			}
			if got := s.Unmigrated(); got != tt.wantUnmigrated {
				t.Errorf("Unmigrated() = %v, want %v for %+v", got, tt.wantUnmigrated, s)
			}
			if got := s.Stale(); got != tt.wantStale {
				t.Errorf("Stale() = %v, want %v for %+v", got, tt.wantStale, s)
			}
		})
	}
}
