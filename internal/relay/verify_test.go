package relay

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// relayBlock wraps body in the fenced block a report or a findings file ends
// with, so the tests below read as the artefact rather than the fence.
func relayBlock(body string) string {
	return "findings in prose\n\n```relay\n" + body + "\n```\n"
}

// TestVerifyQuestionNamesEveryFile pins what the reviewer is handed (#144):
// the plan, the report, the diff and the gate log paths, "none" for a round
// with no gate, and the block template it must answer with. Mutation check:
// drop any one argument from the format string and this fails.
func TestVerifyQuestionNamesEveryFile(t *testing.T) {
	q := verifyQuestion("webshop", 1,
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"/state/webshop/001-diff.patch",
		"/state/webshop/001-gate.log")

	for _, want := range []string{
		"Verify round 1 of binding \"webshop\"",
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"/state/webshop/001-diff.patch",
		"/state/webshop/001-gate.log",
		"verdict: accepted | rejected",
		"reasons: [\"...\"]",
		"```relay",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("question does not name %q:\n%s", want, q)
		}
	}

	if q := verifyQuestion("webshop", 1, "p", "r", "d", ""); !strings.Contains(q, "Gate:   none") {
		t.Errorf("empty gate log must read \"Gate:   none\", got:\n%s", q)
	}
}

// TestParseVerdict is the table for #144's block parser: the two verdicts,
// both reason forms, and the unstructured answers that must not be read as a
// judgement.
func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name        string
		findings    string
		wantVerdict string
		wantReasons []string
	}{
		{
			name:        "accepted",
			findings:    relayBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
		{
			name:        "rejected with a JSON reasons array",
			findings:    relayBlock("verdict: rejected\nreasons: [\"a\", \"b\"]"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "rejected with a YAML-ish reason list",
			findings:    relayBlock("verdict: rejected\nreasons:\n- a\n- b"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "no block at all",
			findings:    "just prose, no verdict block",
			wantVerdict: "unstructured",
		},
		{
			name:        "a verdict that is neither word",
			findings:    relayBlock("verdict: maybe"),
			wantVerdict: "unstructured",
		},
		{
			name: "the last block wins",
			findings: relayBlock("verdict: rejected\nreasons: [\"stale\"]") +
				"\nmore prose\n\n" + relayBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, reasons := parseVerdict([]byte(c.findings))
			if verdict != c.wantVerdict {
				t.Errorf("verdict = %q, want %q", verdict, c.wantVerdict)
			}
			if !reflect.DeepEqual(reasons, c.wantReasons) {
				t.Errorf("reasons = %v, want %v", reasons, c.wantReasons)
			}
		})
	}

	if v, _ := parseVerdict(nil); v != "unstructured" {
		t.Errorf("parseVerdict(nil) = %q, want unstructured", v)
	}
}

// TestVerifyConsultScope pins #313: the verify reviewer runs in its own
// relay-verify-* scope, with the template's CPUQuota and the gate quota left
// behind in the template.
func TestVerifyConsultScope(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }
	rt.Scope = &ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var consult *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == verifyRole {
			consult = &got.Consults[i]
		}
	}
	if consult == nil {
		t.Fatalf("no %q consult on the binding: %+v", verifyRole, got.Consults)
	}

	// specs[0] is the builder's own process; the verify consult is second.
	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want the builder's plus one verify consult", len(fr.specs))
	}
	spec := fr.specs[1]
	if spec.Scope == nil {
		t.Fatal("verify spec.Scope = nil, want a scope from the template")
	}
	if !strings.HasPrefix(spec.Scope.Unit, "relay-verify-local-webshop-1-") {
		t.Errorf("Scope.Unit = %q, want it to start relay-verify-local-webshop-1-", spec.Scope.Unit)
	}
	if !strings.HasSuffix(spec.Scope.Unit, consult.ID) {
		t.Errorf("Scope.Unit = %q, want it to end with the consult id %q", spec.Scope.Unit, consult.ID)
	}
	if spec.Scope.CPUQuota != "150%" {
		t.Errorf("Scope.CPUQuota = %q, want the template's 150%%, not the gate quota", spec.Scope.CPUQuota)
	}
	if spec.Scope.GateCPUQuota != "" {
		t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
	}
	if spec.Scope.AllowedCPUs != "0-3" {
		t.Errorf("Scope.AllowedCPUs = %q, want the whole pool 0-3: verify starts after the core is released", spec.Scope.AllowedCPUs)
	}
}
