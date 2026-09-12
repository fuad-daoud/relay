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
