package relevo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestCaptureRepoNormalisesOrigin(t *testing.T) {
	t.Parallel()

	rt := Runtime{Git: &fakeGit{repoFactsOrigin: "git@github.com:o/r.git", repoFactsCommonDir: "/repo/.git"}}

	got := captureRepo(context.Background(), rt, "/repo")

	want := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("captureRepo = %+v, want %+v", got, want)
	}
}

func TestCaptureRepoNilGitReturnsNil(t *testing.T) {
	t.Parallel()

	rt := Runtime{Git: nil}

	if got := captureRepo(context.Background(), rt, "/repo"); got != nil {
		t.Errorf("captureRepo with nil Git = %+v, want nil", got)
	}
}

func TestCaptureRepoErrorReturnsNil(t *testing.T) {
	t.Parallel()

	rt := Runtime{Git: &fakeGit{repoFactsErr: errors.New("not a repo")}}

	if got := captureRepo(context.Background(), rt, "/repo"); got != nil {
		t.Errorf("captureRepo on a RepoFacts error = %+v, want nil", got)
	}
}

func TestPlannerLocatorResolves(t *testing.T) {
	t.Parallel()

	rt := Runtime{Sessions: func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "S" {
			return "/home/x/.claude/projects/slug/S.jsonl", true
		}
		return "", false
	}}

	got := plannerLocator(rt, "claude", "S")
	if got != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("plannerLocator = %q, want the resolved path", got)
	}
}

func TestPlannerLocatorNilSessionsReturnsEmpty(t *testing.T) {
	t.Parallel()

	rt := Runtime{Sessions: nil}

	if got := plannerLocator(rt, "claude", "S"); got != "" {
		t.Errorf("plannerLocator with nil Sessions = %q, want empty", got)
	}
}

func TestPlannerLocatorNotFoundReturnsEmpty(t *testing.T) {
	t.Parallel()

	rt := Runtime{Sessions: func(kind, sessionID string) (string, bool) {
		return "", false
	}}

	if got := plannerLocator(rt, "claude", "S"); got != "" {
		t.Errorf("plannerLocator on a miss = %q, want empty", got)
	}
}
