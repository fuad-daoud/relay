package relay

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestTickPersistsAnExpiredReservation(t *testing.T) {
	f := &fakePanes{}
	rt, clock := seedSpawning(t, f)
	clock.Advance(consultSpawnTimeout + time.Second)

	d := NewDaemon(rt, time.Second)
	for i := 0; i < 2; i++ {
		if err := d.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Consults[0].State; got != store.ConsultSilent {
		t.Errorf("consult state on disk = %q, want %q", got, store.ConsultSilent)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindFindings {
			n++
		}
	}
	if n != 1 {
		t.Errorf("findings entries logged across 2 ticks = %d, want 1", n)
	}
}
