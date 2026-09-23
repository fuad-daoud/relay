package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	if len(builders) != 2 {
		t.Fatalf("builder candidates = %d, want 2", len(builders))
	}
	for _, c := range builders {
		if c.Tier != "yolo" {
			t.Errorf("candidate %s tier = %q, want yolo", c.Ref(), c.Tier)
		}
		if want := []string{"builder"}; !reflect.DeepEqual(c.Roles, want) {
			t.Errorf("candidate %s roles = %v, want %v", c.Ref(), c.Roles, want)
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
	wantOrder := []string{
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/glm-5.3-flash",
	}
	if got := pol.OrderFor("builder"); !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("OrderFor(builder) = %v, want %v", got, wantOrder)
	}
	if pol.MaxTier != "yolo" {
		t.Errorf("MaxTier = %q, want yolo", pol.MaxTier)
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

func TestWriteRefusesWithoutForceBothOrNeither(t *testing.T) {
	dir := t.TempDir()
	files := Files{
		Kinds:      []string{"claude"},
		Candidates: []byte("[]\n"),
		Policy:     []byte("{}\n"),
	}

	relevoDir := filepath.Join(dir, "relevo")
	if err := os.MkdirAll(relevoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	policyPath := filepath.Join(relevoDir, "policy.json")
	candidatesPath := filepath.Join(relevoDir, "candidates.json")
	if err := os.WriteFile(policyPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("pre-create policy: %v", err)
	}

	res, err := Write(dir, files, false)
	if err != nil {
		t.Fatalf("Write(force=false): %v", err)
	}
	if res.WroteCandidates || res.WrotePolicy {
		t.Fatalf("Write(force=false) wrote candidates=%v policy=%v, want both false",
			res.WroteCandidates, res.WrotePolicy)
	}
	if _, err := os.Stat(candidatesPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("candidates.json after refused write: err = %v, want not-exist", err)
	}
	if b, err := os.ReadFile(policyPath); err != nil || string(b) != "keep\n" {
		t.Fatalf("policy.json after refused write = %q, %v; want unchanged", b, err)
	}

	res, err = Write(dir, files, true)
	if err != nil {
		t.Fatalf("Write(force=true): %v", err)
	}
	if !res.WroteCandidates || !res.WrotePolicy {
		t.Fatalf("Write(force=true) wrote candidates=%v policy=%v, want both true",
			res.WroteCandidates, res.WrotePolicy)
	}
	if b, err := os.ReadFile(candidatesPath); err != nil || string(b) != "[]\n" {
		t.Fatalf("candidates.json after forced write = %q, %v", b, err)
	}
	if b, err := os.ReadFile(policyPath); err != nil || string(b) != "{}\n" {
		t.Fatalf("policy.json after forced write = %q, %v", b, err)
	}
	if res.CandidatesPath != candidatesPath || res.PolicyPath != policyPath {
		t.Fatalf("WriteResult paths = %q, %q; want %q, %q",
			res.CandidatesPath, res.PolicyPath, candidatesPath, policyPath)
	}
}
