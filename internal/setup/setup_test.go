package setup

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/actors"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// pathEnv is the InstallEnv seam Plan consults. Only LookPath answers; every
// other method is unreachable from Plan and returns a zero value.
type pathEnv struct{ onPath map[string]bool }

func (e pathEnv) LookPath(binary string) (string, error) {
	if e.onPath[binary] {
		return "/bin/" + binary, nil
	}
	return "", fmt.Errorf("binary not found: %s", binary)
}

func (e pathEnv) HomePath(rel string) (string, error)      { return rel, nil }
func (e pathEnv) ReadFile(string) ([]byte, error)          { return nil, fs.ErrNotExist }
func (e pathEnv) MkdirAll(string) error                    { return nil }
func (e pathEnv) WriteFile(string, []byte) error           { return nil }
func (e pathEnv) LoadManifest() (map[string]string, error) { return map[string]string{}, nil }
func (e pathEnv) SaveManifest(map[string]string) error     { return nil }

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
	builders := set.ForRole("builder")
	if len(builders) != 0 {
		t.Errorf("ForRole(builder) = %v, want none: the candidates carry no roles (R5)", builders)
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
			t.Errorf("candidate %s carries roles %v / tier %q, want neither (R5)", c.Ref(), c.Roles, c.Tier)
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
		t.Errorf("policy order/tier = %v/%v, want none (R5)", pol.Order, pol.Tier)
	}
	if pol.MaxTier != "yolo" {
		t.Errorf("MaxTier = %q, want yolo", pol.MaxTier)
	}

	actorSet, _, err := actors.ParseActors(files.Actors)
	if err != nil {
		t.Fatalf("actors.ParseActors: %v", err)
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

// TestPlanCandidatesHaveNoRolesKey pins W4: config init must not write a
// "roles": null; the field is omitempty, so the encoded candidates carry no
// roles key at all.
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
