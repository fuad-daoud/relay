package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

// pauseCloseRound clears the round-open stamp and advances the round number
// the way queueReport does, so a pause fixture is "between rounds" with the
// round it last completed recorded on the binding.
func pauseCloseRound(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	b.RoundStartedAt = time.Time{}
	b.Builder = clearProcess(b.Builder)
	b.Round++
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save closed round: %v", err)
	}
	return b
}
