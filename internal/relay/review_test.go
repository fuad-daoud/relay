package relay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// reviewFixtureDiff is the x.go/y.go fixture (verbatim from
// internal/patch's own fixture): x.go has a context, a delete and two adds;
// y.go a delete and an add.
const reviewFixtureDiff = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"

// seedReviewBinding saves a "webshop" binding with the given round and
// writes reviewFixtureDiff as round 1's captured diff, the way
// TestDiffCommand (cmd/relay/main_test.go:191) seeds a store for `relay
// diff`.
func seedReviewBinding(t *testing.T, round int) Runtime {
	t.Helper()
	s := store.New(t.TempDir())
	rt := Runtime{Store: s}
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: round, State: store.StateActive}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.DiffPath("webshop", 1), []byte(reviewFixtureDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	return rt
}

func writeComments(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comments.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write comments: %v", err)
	}
	return path
}

func TestParseCommentsAnchoredAndGeneral(t *testing.T) {
	in := "x.go:3: make it more\n\nno anchor here\n# comment\ny.go:1: b\n"
	comments := ParseComments([]byte(in))

	var anchored, general int
	for _, c := range comments {
		if c.Path == "" {
			general++
		} else {
			anchored++
		}
	}
	if anchored != 2 {
		t.Errorf("anchored = %d, want 2 (%+v)", anchored, comments)
	}
	if general != 1 {
		t.Errorf("general = %d, want 1 (%+v)", general, comments)
	}
}

func TestReviewRefusesUnknownAnchor(t *testing.T) {
	rt := seedReviewBinding(t, 2)
	commentsFile := writeComments(t, "x.go:9: nope\n")

	_, err := Review(context.Background(), rt, ReviewOptions{Name: "webshop", File: commentsFile})
	if err == nil {
		t.Fatal("Review: expected error for an anchor not in the diff, got nil")
	}
	if !strings.Contains(err.Error(), "x.go:9") {
		t.Errorf("error %q must list the bad anchor", err.Error())
	}

	wantOut := filepath.Join(rt.Store.Dir("webshop"), "001-review-plan.md")
	if _, statErr := os.Stat(wantOut); statErr == nil {
		t.Errorf("Review must not write %s when an anchor is unknown", wantOut)
	}
}

func TestReviewRendersPlan(t *testing.T) {
	rt := seedReviewBinding(t, 2)
	commentsFile := writeComments(t, "x.go:3: say more\ngeneral thing\n")

	res, err := Review(context.Background(), rt, ReviewOptions{Name: "webshop", File: commentsFile})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	wantPath := filepath.Join(rt.Store.Dir("webshop"), "001-review-plan.md")
	if res.PlanPath != wantPath {
		t.Fatalf("PlanPath = %q, want %q", res.PlanPath, wantPath)
	}

	body, err := os.ReadFile(res.PlanPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	content := string(body)

	for _, want := range []string{
		"# Review of round 1",
		"Fix ONLY",
		"### Task 1 -- x.go:3",
		" context",
		"-old",
		"+new",
		"+more",
		"- general thing",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("plan must contain %q; got:\n%s", want, content)
		}
	}
}

// TestReviewSendHandsThePlanToSend pins Review's --send path: the review
// plan becomes the next round's plan, the same as any other Send.
func TestReviewSendHandsThePlanToSend(t *testing.T) {
	f := &fakeHerdr{}
	rt, _ := seedBound(t, f)

	if err := os.WriteFile(rt.Store.DiffPath("webshop", 1), []byte(reviewFixtureDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	commentsFile := writeComments(t, "x.go:3: say more\n")

	res, err := Review(context.Background(), rt, ReviewOptions{Name: "webshop", File: commentsFile, Send: true})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Sent == nil {
		t.Fatal("Sent = nil, want a SendResult")
	}
	if res.Sent.Round != 2 {
		t.Errorf("Sent.Round = %d, want 2", res.Sent.Round)
	}

	if len(f.prompts) != 1 {
		t.Fatalf("got %d prompts, want 1", len(f.prompts))
	}
	if !strings.Contains(f.prompts[0].Text, rt.Store.PlanPath("webshop", 2)) {
		t.Error("prompt must name round 2's plan path (the review plan, copied in by Send)")
	}

	sentBody, err := os.ReadFile(rt.Store.PlanPath("webshop", 2))
	if err != nil {
		t.Fatalf("read round 2 plan: %v", err)
	}
	reviewBody, err := os.ReadFile(res.PlanPath)
	if err != nil {
		t.Fatalf("read review plan: %v", err)
	}
	if string(sentBody) != string(reviewBody) {
		t.Error("round 2's plan must equal the review plan")
	}
}

func TestReviewDefaultsToNewestCompletedRound(t *testing.T) {
	rt := seedReviewBinding(t, 2)
	commentsFile := writeComments(t, "x.go:3: say more\n")

	res, err := Review(context.Background(), rt, ReviewOptions{Name: "webshop", File: commentsFile})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1 (binding's Round-1)", res.Round)
	}
}
