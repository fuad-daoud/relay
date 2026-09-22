package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
)

func testSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	return set
}

const threeKinds = `[
  {"harness":"agy","provider":"t","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"t","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"t","model":"m","roles":["builder"]}
]`

func TestAssembleKinds(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	tbl := testSet(t, threeKinds)

	// Default table has agy, claude, opencode
	kinds, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	expected := []string{"agy", "claude", "opencode"}
	if len(kinds) != len(expected) {
		t.Fatalf("kinds len = %d, want %d: %v", len(kinds), len(expected), kinds)
	}
	for i, k := range expected {
		if kinds[i] != k {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], k)
		}
	}

	// Add a binding with a custom builder kind
	b := store.Binding{
		Name: "custom",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "custom-kind",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds2, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	foundCustom := false
	for _, k := range kinds2 {
		if k == "custom-kind" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom-kind not found in assembled kinds: %v", kinds2)
	}
}

func TestAssembleKindsSurfacesErrors(t *testing.T) {
	// A store pointing at a file (not a directory) causes st.List() to fail.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "file-not-dir")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	st := store.New(filePath)
	tbl := testSet(t, threeKinds)

	kinds, err := assembleKinds(tbl, st)
	if err == nil {
		t.Error("assembleKinds should surface error from store.List(), got nil")
	}
	// ...and must still hand back what it did learn. A diagnostic that refuses
	// to diagnose because one of its own inputs is unreadable is worse than one
	// that reports the gap and checks the rest.
	if len(kinds) == 0 {
		t.Error("assembleKinds must still return the candidate-derived kinds when the store is unreadable")
	}
}

func TestAssembleKindsWithNoCandidatesUsesBindingsOnly(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	b := store.Binding{
		Name: "claude-binding",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "claude",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds, err := assembleKinds(testSet(t, "[]"), st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "claude" {
		t.Fatalf("kinds = %v, want [claude]", kinds)
	}
}

func TestAssembleDefinitionsFollowsCandidateRoles(t *testing.T) {
	set := testSet(t, `[
	  {"harness":"agy","provider":"t","model":"m","roles":["builder"]},
	  {"harness":"claude","provider":"t","model":"m","roles":["reviewer"]},
	  {"harness":"claude","provider":"t","model":"n","roles":["builder"]}
	]`)

	got := assembleDefinitions(set, []string{"agy", "claude", "opencode"})

	want := map[string][]string{
		"agy":      {"plan-executor", "researcher"},
		"claude":   {"plan-executor", "researcher", "reviewer"},
		"opencode": {"plan-executor", "researcher"}, // binding-only kind: builder's set
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions = %v, want %v", got, want)
	}
}

func TestAssembleDefinitionsNilSet(t *testing.T) {
	got := assembleDefinitions(nil, []string{"claude"})
	want := map[string][]string{"claude": {"plan-executor", "researcher"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions(nil) = %v, want %v", got, want)
	}
}

func TestLedgerChecks(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	gates := []ledger.Gate{
		{Token: "claude/anthropic/sonnet", Kind: ledger.RateLimited, Since: now, Until: time.Time{}},
		{Token: "agy/google/m", Kind: ledger.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
	}

	checks := ledgerChecks(gates)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}

	if checks[0].Group != "claude" || checks[0].Name != "ledger" || checks[0].Severity != doctor.SevWarn {
		t.Errorf("check 0 = %+v", checks[0])
	}
	if !strings.HasPrefix(checks[0].Fix, "relay available ") {
		t.Errorf("check 0 Fix = %q, want prefix %q", checks[0].Fix, "relay available ")
	}

	if checks[1].Group != "agy" || checks[1].Name != "ledger" || checks[1].Severity != doctor.SevWarn {
		t.Errorf("check 1 = %+v", checks[1])
	}
	if !strings.HasPrefix(checks[1].Fix, "wait until ") {
		t.Errorf("check 1 Fix = %q, want prefix %q", checks[1].Fix, "wait until ")
	}

	if got := ledgerChecks(nil); len(got) != 0 {
		t.Errorf("ledgerChecks(nil) = %+v, want empty", got)
	}
}

func TestPolicyChecks(t *testing.T) {
	warnings := []relay.PolicyWarning{
		{Role: "builder", Index: 1, Token: "claude/test/nope", Text: `order.builder[1] "claude/test/nope" is not a configured candidate`},
		{Role: "builder", Index: -1, Token: "opencode/test/m", Text: `builder: opencode/test/m serves the role but is not in order.builder`},
	}

	checks := policyChecks(warnings)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}

	for i, w := range warnings {
		c := checks[i]
		if c.Group != "" || c.Name != "policy" || c.Severity != doctor.SevWarn {
			t.Errorf("check %d = %+v", i, c)
		}
		if c.Detail != w.Text {
			t.Errorf("check %d Detail = %q, want %q", i, c.Detail, w.Text)
		}
		if c.Fix != "edit ~/.config/relay/policy.json" {
			t.Errorf("check %d Fix = %q, want %q", i, c.Fix, "edit ~/.config/relay/policy.json")
		}
	}

	if got := policyChecks(nil); len(got) != 0 {
		t.Errorf("policyChecks(nil) = %+v, want empty", got)
	}
}

// TestServerChecksScopesWarning pins #285's scopes warning and #295's quota:
// a queue-aware, enrolled server without systemd scopes gets a SevWarn row
// naming the daemon-restart risk, in addition to its builders census text on
// the ok row; Scopes:true gets no such warning row; a server that is not
// queue-aware (a pre-queue server) carries no builders text at all. Pure
// over a hand-built []relay.ServerProbe -- no herdr, no network.
//
// Mutation check: drop the `!p.Builders.Scopes` guard in serverChecks and
// the contabo warning row (Scopes:true) reappears, failing this test.
func TestServerChecksScopesWarning(t *testing.T) {
	probes := []relay.ServerProbe{
		{
			Name: "zen", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: false},
		},
		{
			Name: "contabo", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: true, Slice: "relay.slice", Quota: "200%"},
		},
		{
			Name: "quotaonly", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Quota: "150%"},
		},
		{
			Name: "sliceonly", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Slice: "relay.slice"},
		},
		{
			Name: "plain", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true},
		},
		{
			Name: "old", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
		},
	}

	checks := serverChecks(probes)

	var zenOK, zenWarn, contaboOK, contaboWarn, quotaOK, sliceOK, plainOK, oldOK *doctor.Check
	for i := range checks {
		c := &checks[i]
		switch {
		case strings.HasPrefix(c.Detail, "zen: enrolled"):
			zenOK = c
		case strings.HasPrefix(c.Detail, "scopes unavailable on zen"):
			zenWarn = c
		case strings.HasPrefix(c.Detail, "contabo: enrolled"):
			contaboOK = c
		case strings.HasPrefix(c.Detail, "scopes unavailable on contabo"):
			contaboWarn = c
		case strings.HasPrefix(c.Detail, "quotaonly: enrolled"):
			quotaOK = c
		case strings.HasPrefix(c.Detail, "sliceonly: enrolled"):
			sliceOK = c
		case strings.HasPrefix(c.Detail, "plain: enrolled"):
			plainOK = c
		case strings.HasPrefix(c.Detail, "old: enrolled"):
			oldOK = c
		}
	}

	if zenOK == nil || !strings.Contains(zenOK.Detail, "builders 2/3, 1 queued, scopes off") {
		t.Fatalf("zen ok check = %+v, want it naming the builders census", zenOK)
	}
	if zenWarn == nil || zenWarn.Severity != doctor.SevWarn ||
		zenWarn.Detail != "scopes unavailable on zen: a daemon restart kills its builders" {
		t.Fatalf("zen scopes warning = %+v, want the exact message", zenWarn)
	}

	if contaboOK == nil || !strings.Contains(contaboOK.Detail, "builders 2/3, 1 queued, scopes on (relay.slice, 200%)") {
		t.Fatalf("contabo ok check = %+v, want it naming the builders census and quota", contaboOK)
	}
	if contaboWarn != nil {
		t.Fatalf("contabo scopes warning = %+v, want none (Scopes is true)", contaboWarn)
	}

	if quotaOK == nil || !strings.Contains(quotaOK.Detail, "builders 1/3, 0 queued, scopes on (150%)") {
		t.Fatalf("quotaonly ok check = %+v, want it naming the quota alone", quotaOK)
	}
	if sliceOK == nil || !strings.Contains(sliceOK.Detail, "builders 1/3, 0 queued, scopes on (relay.slice)") {
		t.Fatalf("sliceonly ok check = %+v, want it naming the slice alone", sliceOK)
	}
	if plainOK == nil || !strings.HasSuffix(plainOK.Detail, "builders 1/3, 0 queued, scopes on") {
		t.Fatalf("plain ok check = %+v, want it naming scopes on", plainOK)
	}

	if oldOK == nil || strings.Contains(oldOK.Detail, "builders ") {
		t.Fatalf("old (pre-queue) ok check = %+v, want no builders text", oldOK)
	}
}

// The bind preflight must be bounded: the herdr client allows 30s per call, so
// an unbounded preflight can add a minute to `relay bind`.
// An adopted pane's integration row must survive a binary that is not on PATH:
// the user launched that agent themselves, but the integration still decides
// whether the round can ever be observed to finish. Fails if the call site stops
// passing `adopted` through.
