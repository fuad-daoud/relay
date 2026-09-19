package relay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestGateLineForms(t *testing.T) {
	tests := []struct {
		name string
		rec  store.GateRecord
		tail []string
		want string
	}{
		{
			name: "pass",
			rec:  store.GateRecord{Command: "make check", Result: "pass", ExitCode: 0, DurationMS: 100000, LogPath: "/p/001-gate.log"},
			want: "Gate: make check -- PASS (exit 0, 1m40s). Output: /p/001-gate.log",
		},
		{
			name: "fail",
			rec:  store.GateRecord{Command: "make check", Result: "fail", ExitCode: 2, DurationMS: 100000, LogPath: "/p/001-gate.log"},
			tail: []string{"line four", "line five"},
			want: "Gate: make check -- FAIL (exit 2, 1m40s). Output: /p/001-gate.log\n  line four\n  line five",
		},
		{
			name: "timeout",
			rec:  store.GateRecord{Command: "make check", Result: "timeout", DurationMS: (10 * time.Minute).Milliseconds(), LogPath: "/p/001-gate.log"},
			want: "Gate: make check -- TIMEOUT after 10m0s. Output: /p/001-gate.log",
		},
		{
			name: "error",
			rec:  store.GateRecord{Command: "make check", Result: "error", Note: "no runner"},
			want: "Gate: make check -- ERROR: no runner.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gateLine(tt.rec, tt.tail); got != tt.want {
				t.Errorf("gateLine() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestTailLines(t *testing.T) {
	t.Run("last n non-empty lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		content := "one\n\ntwo\nthree\n\nfour\nfive\nsix\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		want := []string{"four", "five", "six"}
		got := tailLines(path, 3)
		if len(got) != len(want) {
			t.Fatalf("tailLines() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tailLines() = %v, want %v", got, want)
			}
		}
	})

	t.Run("fewer lines than n returns all non-empty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		if err := os.WriteFile(path, []byte("only\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := tailLines(path, 5)
		if len(got) != 1 || got[0] != "only" {
			t.Fatalf("tailLines() = %v, want [only]", got)
		}
	})

	t.Run("missing file returns nil", func(t *testing.T) {
		got := tailLines(filepath.Join(t.TempDir(), "absent.log"), 5)
		if got != nil {
			t.Fatalf("tailLines() = %v, want nil", got)
		}
	})
}

func TestGateTimeoutFor(t *testing.T) {
	t.Run("binding override wins", func(t *testing.T) {
		b := store.Binding{GateTimeoutMS: 5000}
		pol := policy.Policy{}
		if got := gateTimeoutFor(b, pol); got != 5*time.Second {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, 5*time.Second)
		}
	})

	t.Run("falls back to policy default", func(t *testing.T) {
		b := store.Binding{}
		pol := policy.Policy{}
		if got := gateTimeoutFor(b, pol); got != policy.DefaultGateTimeout {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, policy.DefaultGateTimeout)
		}
	})

	t.Run("falls back to policy override", func(t *testing.T) {
		ms := 90000
		b := store.Binding{}
		pol := policy.Policy{Gate: &policy.GatePolicy{TimeoutMS: &ms}}
		if got := gateTimeoutFor(b, pol); got != 90*time.Second {
			t.Fatalf("gateTimeoutFor() = %v, want %v", got, 90*time.Second)
		}
	})
}
