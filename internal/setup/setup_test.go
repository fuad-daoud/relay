package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

func TestPlanFindsBinariesInHarnessOrder(t *testing.T) {
	env := pathEnv{onPath: map[string]bool{"opencode": true, "claude": true}}
	files, err := Plan(env)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if want := []string{"claude", "opencode"}; !reflect.DeepEqual(files.Kinds, want) {
		t.Fatalf("Kinds = %v, want %v", files.Kinds, want)
	}

	dir := t.TempDir()
	candPath := filepath.Join(dir, "candidates.json")
	if err := os.WriteFile(candPath, files.Candidates, 0o644); err != nil {
		t.Fatalf("write candidates: %v", err)
	}
	set, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	if builders := set.ForRole("builder"); len(builders) != 0 {
		t.Errorf("ForRole(builder) = %v, want none", builders)
	}
	var cands []candidate.Candidate
	if err := json.Unmarshal(files.Candidates, &cands); err != nil {
		t.Fatalf("unmarshal candidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	for _, c := range cands {
		if c.Roles != nil || c.Tier != "" {
			t.Errorf("candidate %s carries roles %v / tier %q, want neither", c.Ref(), c.Roles, c.Tier)
		}
	}

	polPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(polPath, files.Policy, 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	pol, err := policy.Load(polPath)
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}
	if len(pol.Order) != 0 || len(pol.Tier) != 0 {
		t.Errorf("policy order/tier = %v/%v, want none", pol.Order, pol.Tier)
	}
	if pol.MaxTier != "yolo" {
		t.Errorf("MaxTier = %q, want yolo", pol.MaxTier)
	}

	actorSet, _, err := roles.ParseActors(files.Actors)
	if err != nil {
		t.Fatalf("roles.ParseActors: %v", err)
	}
	builder, ok := actorSet["builder"]
	if !ok {
		t.Fatalf("actors = %v, want a builder", actorSet)
	}
	if builder.Agent != "plan-executor" || builder.Tier != "yolo" {
		t.Errorf("builder actor = %+v, want plan-executor at tier yolo", builder)
	}
	var names []string
	for _, e := range builder.Candidates {
		names = append(names, e.Candidate)
	}
	if want := candidate.DeriveNames(cands); !reflect.DeepEqual(names, want) {
		t.Errorf("builder candidates = %v, want the plan's names %v", names, want)
	}
}

func TestPlanCandidatesHaveNoRolesKey(t *testing.T) {
	files, err := Plan(pathEnv{onPath: map[string]bool{"opencode": true}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if strings.Contains(string(files.Candidates), `"roles"`) {
		t.Errorf("candidates JSON must carry no roles key:\n%s", files.Candidates)
	}
}

func TestPlanNoneIsAnError(t *testing.T) {
	_, err := Plan(pathEnv{onPath: map[string]bool{}})
	if err == nil {
		t.Fatal("Plan with no binaries on PATH: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no harness binaries") {
		t.Fatalf("Plan error = %q, want it to mention %q", err, "no harness binaries")
	}
}
