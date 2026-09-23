package relay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

	t.Run("skips the rusage trailer line", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gate.log")
		content := "a\nb\n\nrelay-rusage:cpu_usec=1 mem_peak=2\n\nrelay-exit:2\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		want := []string{"a", "b", "relay-exit:2"}
		got := tailLines(path, 3)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tailLines() = %v, want %v", got, want)
		}
	})
}

// TestGateStepScopesTheGate pins #313: the gate starts in its own
// relay-gate-* scope, using the template's GateCPUQuota as its CPUQuota, and
// with no scope at all when the runtime has no template.
func TestGateStepScopesTheGate(t *testing.T) {
	runGate := func(t *testing.T, rt Runtime, b store.Binding) {
		t.Helper()
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			_, _, _, err := gateStep(context.Background(), rt, tx, b)
			return err
		}); err != nil {
			t.Fatalf("gateStep: %v", err)
		}
	}

	t.Run("template with a gate quota", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		rt.Scope = &ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%"}
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		spec := fr.specs[0]
		if spec.Scope == nil {
			t.Fatal("gate spec.Scope = nil, want a scope from the template")
		}
		wantUnit := "relay-gate-local-" + b.Name + "-" + strconv.Itoa(b.Round)
		if spec.Scope.Unit != wantUnit {
			t.Errorf("Scope.Unit = %q, want %q", spec.Scope.Unit, wantUnit)
		}
		if spec.Scope.CPUQuota != "300%" {
			t.Errorf("Scope.CPUQuota = %q, want the gate quota 300%%", spec.Scope.CPUQuota)
		}
		if spec.Scope.GateCPUQuota != "" {
			t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
		}
	})

	t.Run("nil template", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		if fr.specs[0].Scope != nil {
			t.Errorf("Scope = %+v, want nil when rt.Scope is nil", fr.specs[0].Scope)
		}
	})

	// The gate runs on its round's core while the round is still open (#314).
	t.Run("template pool and a pinned round", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentBinding(t)
		rt.Runner = fr
		rt.Scope = &ScopeSpec{CPUWeight: 100, CPUQuota: "150%", AllowedCPUs: "0-3"}
		two := 2
		b.RoundCPU = &two
		b.Gate = "make check"
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		runGate(t, rt, b)

		if len(fr.specs) != 1 {
			t.Fatalf("specs = %+v, want one Start", fr.specs)
		}
		if got := fr.specs[0].Scope.AllowedCPUs; got != "2" {
			t.Errorf("gate Scope.AllowedCPUs = %q, want the round's core 2", got)
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
