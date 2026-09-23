package doctor

import (
	"errors"
	"strings"
	"testing"
)

// errReadEnv is fakeEnv with ReadFile failing: the controllers file exists on
// a real host but relay cannot read it, the ProbeFailed case.
type errReadEnv struct {
	Env
	err error
}

func (e errReadEnv) ReadFile(string) ([]byte, error) { return nil, e.err }

func TestUserManagerControllersPath(t *testing.T) {
	want := "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"
	if got := UserManagerControllersPath(1000); got != want {
		t.Errorf("UserManagerControllersPath(1000) = %q, want %q", got, want)
	}
}

// TestScopeChecks is #314's doctor table: only blocks that set allowed_cpus get
// a row, and the row's precedence is unreadable, not delegated, a core this
// host lacks, then ok.
func TestScopeChecks(t *testing.T) {
	const path = "/fake/cgroup.controllers"
	envWith := func(contents string) *fakeEnv {
		return &fakeEnv{fileContents: map[string]string{path: contents}}
	}
	block := func(key, cpus string, max int) []ScopeBlock {
		return []ScopeBlock{{Key: key, AllowedCPUs: cpus, MaxCPU: max}}
	}

	t.Run("no blocks gives nil", func(t *testing.T) {
		if got := ScopeChecks(envWith("cpuset cpu io memory pids\n"), nil, path, 4); got != nil {
			t.Errorf("ScopeChecks(nil) = %+v, want nil", got)
		}
	})

	t.Run("empty blocks give nil", func(t *testing.T) {
		blocks := block("scope.allowed_cpus", "", 0)
		blocks = append(blocks, block("serve.scope.allowed_cpus", "", 0)...)
		if got := ScopeChecks(envWith("cpuset cpu io memory pids\n"), blocks, path, 4); got != nil {
			t.Errorf("ScopeChecks(empty blocks) = %+v, want nil", got)
		}
	})

	t.Run("delegated is ok", func(t *testing.T) {
		got := ScopeChecks(envWith("cpuset cpu io memory pids\n"), block("scope.allowed_cpus", "0-2", 2), path, 4)
		if len(got) != 1 {
			t.Fatalf("checks = %+v, want one", got)
		}
		if got[0].Group != "scope" || got[0].Name != "allowed_cpus" {
			t.Errorf("row = %s/%s, want scope/allowed_cpus", got[0].Group, got[0].Name)
		}
		if got[0].Severity != SevOK {
			t.Errorf("Severity = %v, want ok", got[0].Severity)
		}
		if !strings.Contains(got[0].Detail, "cpuset delegated; rounds pinned one per core from 0-2") {
			t.Errorf("Detail = %q, want the delegated sentence naming the pool", got[0].Detail)
		}
		if got[0].Fix != "" {
			t.Errorf("Fix = %q, want empty on ok", got[0].Fix)
		}
	})

	t.Run("not delegated warns with the drop-in fix", func(t *testing.T) {
		got := ScopeChecks(envWith("cpu io memory pids"), block("scope.allowed_cpus", "0-2", 2), path, 4)
		if len(got) != 1 {
			t.Fatalf("checks = %+v, want one", got)
		}
		if got[0].Severity != SevWarn {
			t.Errorf("Severity = %v, want warn", got[0].Severity)
		}
		if !strings.Contains(got[0].Detail, "scope.allowed_cpus = 0-2 is set but cpuset is not delegated") {
			t.Errorf("Detail = %q, want the not-delegated sentence", got[0].Detail)
		}
		if !strings.Contains(got[0].Fix, "Delegate=cpu cpuset") {
			t.Errorf("Fix = %q, want it to contain Delegate=cpu cpuset", got[0].Fix)
		}
	})

	t.Run("unreadable is a failed probe", func(t *testing.T) {
		env := errReadEnv{Env: &fakeEnv{}, err: errors.New("permission denied")}
		got := ScopeChecks(env, block("scope.allowed_cpus", "0-2", 2), path, 4)
		if len(got) != 1 {
			t.Fatalf("checks = %+v, want one", got)
		}
		if !got[0].ProbeFailed {
			t.Error("ProbeFailed = false, want true")
		}
		if !strings.Contains(got[0].Detail, "cannot read "+path) {
			t.Errorf("Detail = %q, want the unreadable sentence naming the path", got[0].Detail)
		}
		if got[0].Fix != "" {
			t.Errorf("Fix = %q, want empty", got[0].Fix)
		}
	})

	t.Run("a core this host lacks warns", func(t *testing.T) {
		got := ScopeChecks(envWith("cpuset cpu io memory pids\n"), block("scope.allowed_cpus", "0-7", 7), path, 4)
		if len(got) != 1 {
			t.Fatalf("checks = %+v, want one", got)
		}
		if got[0].Severity != SevWarn {
			t.Errorf("Severity = %v, want warn", got[0].Severity)
		}
		if !strings.Contains(got[0].Detail, "scope.allowed_cpus names cpu 7 but this host has 4 (0-3)") {
			t.Errorf("Detail = %q, want the too-many-cores sentence", got[0].Detail)
		}
		if !strings.Contains(got[0].Fix, "narrow scope.allowed_cpus in policy.json") {
			t.Errorf("Fix = %q, want the narrowing command", got[0].Fix)
		}
	})

	t.Run("two blocks give two rows", func(t *testing.T) {
		blocks := block("scope.allowed_cpus", "0-2", 2)
		blocks = append(blocks, block("serve.scope.allowed_cpus", "0-3", 3)...)
		got := ScopeChecks(envWith("cpuset cpu io memory pids\n"), blocks, path, 8)
		if len(got) != 2 {
			t.Fatalf("checks = %+v, want two", got)
		}
		if got[0].Name != "allowed_cpus" || got[1].Name != "serve.allowed_cpus" {
			t.Errorf("names = %q/%q, want allowed_cpus/serve.allowed_cpus", got[0].Name, got[1].Name)
		}
		if got[0].Detail == got[1].Detail {
			t.Errorf("two blocks share the detail %q; each must name its own pool", got[0].Detail)
		}
	})
}
