package relay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relay/internal/store"
)

// addRepo makes a directory to stand in for the planner's repository.
func addRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAddRecordsBaseRef pins #136: a peer cut from a checkout records the
// branch that checkout had checked out, asked of the source repo -- not of
// the fresh worktree -- so `relay land` knows what to rebase onto.
// TestAddRecordsNoBaseRefWithoutBranch pins the other half: CurrentBranch
// failing (a detached source HEAD, or no repository at all) records ""
// rather than the literal "HEAD", so land asks for --onto instead of
// fetching a ref that does not exist.
// TestAddRecordsRepoFromCWDNotWorktree pins #172: the peer's RepoRef is
// captured from opts.Repo, the parent checkout the worktree is cut from --
// not from the fresh worktree directory (which, before AddWorktree runs,
// is nothing to capture facts about at all, and afterwards would merely
// report the same facts back over an extra git call).
// TestAddRefusesALongNameBeforeCuttingAWorktree pins #64: a 25-character name
// builds a 33-character builder agent name, and Add must refuse it before the
// worktree is cut -- a refused name leaves nothing behind.
// TestAddBranchLocalChecksOutWithoutCutting pins the local half of
// `add --branch`: a branch that already exists locally is checked out into
// relay's own worktree, with no worktree cut and no branch created.
// TestAddBranchOriginOnlyTracksFirst pins the origin half: when only
// origin/<branch> exists, relay first makes a local tracking branch.
// TestAddBranchMissingRefuses pins that a branch on neither the local repo nor
// origin is a refusal before any git write.
// TestAddBranchCheckedOutRefuses pins the refusal when the existing branch is
// checked out in another worktree, before any builder is resolved or started.
// TestAddBranchDrivenByLiveBindingRefuses pins the guard: a branch a live
// binding already drives cannot be adopted, while a DONE binding does not block.
// TestAddBranchWithCwdRefused pins that Add itself refuses the flag pair, not
// only the CLI.
// TestDefaultBindingName pins the derivation store.ValidName accepts, and that
// an underivable branch returns ValidName's own error.
func TestDefaultBindingName(t *testing.T) {
	cases := []struct {
		branch  string
		want    string
		wantErr bool
	}{
		{"feature/api-auth", "api-auth", false},
		{"v2", "v2", false},
		{"Fix/Login_Form", "login_form", false},
		{"//", "", true},
	}
	for _, c := range cases {
		got, err := DefaultBindingName(c.branch)
		if c.wantErr {
			if err == nil {
				t.Errorf("DefaultBindingName(%q) = %q, want an error", c.branch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("DefaultBindingName(%q): %v", c.branch, err)
			continue
		}
		if got != c.want {
			t.Errorf("DefaultBindingName(%q) = %q, want %q", c.branch, got, c.want)
		}
		if err := store.ValidName(got); err != nil {
			t.Errorf("store.ValidName(%q) = %v, want nil", got, err)
		}
	}
}

// TestUnbindExistingBranchNeverDeletes pins the invariant README states: relay
// deletes a branch in zero places, so neither unbind nor done+gc may remove an
// ExistingBranch binding's adopted branch.
// TestAddRefusesUnsupportedTierBeforeWorktree pins the early tier refusal
// (#303 §1): Add renders the candidate's headless launch through
// resolveBuilder's headlessLaunch before the round can start, so a harness
// that cannot honour the tier is refused and the worktree Add just cut is
// rolled back -- no binding, no kept tree.
