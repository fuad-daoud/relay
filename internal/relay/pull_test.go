package relay

import (
	"context"
	"testing"
)

func TestPullWithNothingPending(t *testing.T) {
	f := &fakePanes{}
	rt, b := seedBound(t, f)

	if _, found, err := Pull(context.Background(), rt, b.Name); err != nil || found {
		t.Fatalf("found=%v err=%v, want false/nil", found, err)
	}
}
