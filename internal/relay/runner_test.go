package relay

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerScriptsAliveAndRecordsKills(t *testing.T) {
	f := newFakeRunner()
	var _ Runner = f

	h, err := f.Start(context.Background(), ProcSpec{Dir: "/tree", Argv: []string{"agy", "-p", "x"}, LogPath: "/state/x/001-builder.log"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.specs) != 1 || f.specs[0].Dir != "/tree" || f.specs[0].LogPath != "/state/x/001-builder.log" {
		t.Fatalf("specs = %+v", f.specs)
	}
	if h.PID == 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a time", h)
	}
	// Unscripted: alive forever.
	for i := 0; i < 3; i++ {
		if alive, _ := f.Alive(context.Background(), h); !alive {
			t.Fatalf("unscripted Alive #%d = false", i)
		}
	}
	// Scripted: true, true, then false forever.
	f.script(h.PID, true, true, false)
	want := []bool{true, true, false, false}
	for i, w := range want {
		if alive, _ := f.Alive(context.Background(), h); alive != w {
			t.Errorf("scripted Alive #%d = %v, want %v", i, alive, w)
		}
	}
	// ExitCode is absent until set.
	if _, ok := f.ExitCode(context.Background(), h, ""); ok {
		t.Error("ExitCode before exit() must be ok=false")
	}
	f.exit(h.PID, 3)
	if code, ok := f.ExitCode(context.Background(), h, ""); !ok || code != 3 {
		t.Errorf("ExitCode = %d, %v; want 3, true", code, ok)
	}

	// A second Start gets a distinct pid; Kill records it and makes it dead.
	h2, _ := f.Start(context.Background(), ProcSpec{Dir: "/tree", Argv: []string{"agy"}, LogPath: "/state/x/002-builder.log"})
	if h2.PID == h.PID {
		t.Fatal("two Starts returned the same pid")
	}
	if err := f.Kill(context.Background(), h2); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(f.kills) != 1 || f.kills[0] != h2 {
		t.Errorf("kills = %+v, want [h2]", f.kills)
	}
	if alive, _ := f.Alive(context.Background(), h2); alive {
		t.Error("a killed handle must read as not alive")
	}

	// Errors pass through.
	f.startErr = errors.New("no binary")
	if _, err := f.Start(context.Background(), ProcSpec{Argv: []string{"x"}}); err == nil {
		t.Error("startErr not returned")
	}
	f.aliveErr = errors.New("ps refused")
	if _, err := f.Alive(context.Background(), h); err == nil {
		t.Error("aliveErr not returned")
	}
}

// TestGoMaxProcsFor pins #315's rule: the CPUs a launched scope allows, or
// ok false when it limits nothing. A malformed AllowedCPUs or CPUQuota
// contributes nothing rather than panicking.
func TestGoMaxProcsFor(t *testing.T) {
	cases := []struct {
		name  string
		scope ScopeSpec
		want  int
		ok    bool
	}{
		{"no limits", ScopeSpec{}, 0, false},
		{"single cpu pin", ScopeSpec{AllowedCPUs: "2"}, 1, true},
		{"cpu range pin", ScopeSpec{AllowedCPUs: "0-2"}, 3, true},
		{"cpu list pin", ScopeSpec{AllowedCPUs: "0,2-3"}, 3, true},
		{"quota two cores", ScopeSpec{CPUQuota: "200%"}, 2, true},
		{"quota rounds up", ScopeSpec{CPUQuota: "150%"}, 2, true},
		{"quota below one core", ScopeSpec{CPUQuota: "50%"}, 1, true},
		{"quota wider than the pin", ScopeSpec{CPUQuota: "300%", AllowedCPUs: "1"}, 1, true},
		{"pin wider than the quota", ScopeSpec{CPUQuota: "100%", AllowedCPUs: "0-3"}, 1, true},
		{"malformed quota", ScopeSpec{CPUQuota: "abc"}, 0, false},
		{"malformed cpu list", ScopeSpec{AllowedCPUs: "x"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := GoMaxProcsFor(tc.scope)
			if got != tc.want || ok != tc.ok {
				t.Errorf("GoMaxProcsFor(%+v) = %d, %v; want %d, %v", tc.scope, got, ok, tc.want, tc.ok)
			}
		})
	}
}
