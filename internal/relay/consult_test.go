package relay

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// verifyConsultID is the deterministic id the verify tests mint.
const verifyConsultID = "7f2a3c1d"

// claudeStream renders a claude harness stream whose last assistant text is
// text: the shape transcript.FinalText reads (#147).
func claudeStream(t *testing.T, text string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	return append(line, '\n')
}

// startVerifyRound puts webshop one reconcile past a verify round's close:
// the round closed, the reviewer was started in its throwaway worktree, and
// the process is scripted to have exited 0. The caller writes the stream the
// reviewer left behind, then ticks the consult.
// leaveVerifyStream writes what the reviewer's process left on its stream and
// scripts it as exited with code 0.
func leaveVerifyStream(t *testing.T, rt Runtime, fr *fakeRunner, text string) {
	t.Helper()
	streamPath := rt.Store.ConsultStreamPath("webshop", 1, verifyConsultID)
	if err := os.WriteFile(streamPath, claudeStream(t, text), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
}

// tickConsults runs one reconcile pass over the consults only.
func tickConsults(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		out, err = reconcileConsults(context.Background(), rt, tx, b)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("reconcileConsults: %v", err)
	}
	return out
}

// This is the mutation-test target named in the spec: delete the
// `State != ConsultRunning` guard at the top of reconcileConsults and this
// fails. Without the guard every tick re-queues findings already delivered.
// TestReconcileAbandonsLegacyPaneConsult pins #303's upgrade path: a consult
// endpoint written before this round has Mode "" (a pane consult). relay can
// no longer drive a pane, so the next reconcile closes it silent with the
// reason and reports it to the planner, instead of reconciling it as a pane.
// seedHeadlessConsult asks for a consult as a process on the webshop binding
// and returns the runtime and the record relay made.
// TestHeadlessConsultFinalMessageBecomesFindings: the process's last
// assistant message is the findings. Deleting the WriteFile leaves the
// consult running and this fails.
// TestHeadlessConsultExitWithoutTextIsSilent: a process that died without a
// final message is reported silent with its exit code and where to look.
// TestHeadlessConsultTimesOut: a process still alive past consultTimeout is
// killed and reported silent.
// TestVerifyVerdictParsedOntoFindingsAndBinding pins #144's verdict path: a
// verify consult whose findings end with `verdict: rejected` records the
// verdict and its two reasons on the findings entry and on the binding, names
// them in the payload, removes the throwaway worktree, and shows in `relay
// status` until the round after next.
// TestVerifyUnstructuredWhenNoBlock pins #144's prose case: findings without
// a readable block are delivered as unstructured rather than guessed at.
