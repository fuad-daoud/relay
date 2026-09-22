package relay

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/usage"
)

// closeRound drives the ordinary close: report file, done marker, idle
// builder, one reconcile. Returns the report entry.
func TestRecordUsageFoldsWithRuntimePrices(t *testing.T) {
	rt := Runtime{Now: func() time.Time { return baseTime }}
	rt.Usage = &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 1_000_000}}}}
	rt.Prices = usage.Prices{Models: map[string]usage.ModelPrice{"test/m": {In: 2}}}
	u := recordUsage(context.Background(), rt, usage.Source{Harness: "claude", Mode: usage.ModeHeadless, Start: baseTime, End: baseTime.Add(time.Second)})
	if u.Cost.Basis != usage.Estimated || u.Cost.USD != 2 {
		t.Errorf("usage = %+v, want estimated $2", u)
	}
	if u.DurationMS != 1000 || u.Harness != "claude" {
		t.Errorf("duration/harness = %d/%q", u.DurationMS, u.Harness)
	}
}
