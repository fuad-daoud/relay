package relevo

import (
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestVerifyConsultScope pins #313: the verify reviewer runs in its own
// relevo-verify-* scope, with the template's CPUQuota and the gate quota left
// behind in the template.
func TestVerifyConsultScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}

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

	var c *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			c = &got.Consults[i]
		}
	}
	if c == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}

	// specs[0] is the builder's own process; the verify consult is second.
	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want the builder's plus one verify consult", len(fr.specs))
	}
	spec := fr.specs[1]
	if spec.Scope == nil {
		t.Fatal("verify spec.Scope = nil, want a scope from the template")
	}
	if !strings.HasPrefix(spec.Scope.Unit, "relevo-verify-local-webshop-1-") {
		t.Errorf("Scope.Unit = %q, want it to start relevo-verify-local-webshop-1-", spec.Scope.Unit)
	}
	if !strings.HasSuffix(spec.Scope.Unit, c.ID) {
		t.Errorf("Scope.Unit = %q, want it to end with the consult id %q", spec.Scope.Unit, c.ID)
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
