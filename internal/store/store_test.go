package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newBinding(name, cwd string) Binding {
	return Binding{
		Name:           name,
		CWD:            cwd,
		Planner:        Endpoint{PaneID: "w2:p3", SessionID: "abc", Kind: "claude"},
		Builder:        Endpoint{AgentName: name + "-builder", PaneID: "w2:p4", Kind: "opencode"},
		BuilderAlias:   "builder",
		Round:          1,
		State:          StateActive,
		RoundCap:       20,
		RoundTimeoutMS: 1800000,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	want := newBinding("upjo", "/home/fuad/projects/uniqueperfumesjo")

	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("upjo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CWD != want.CWD || got.Builder.AgentName != want.Builder.AgentName {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("Save must stamp CreatedAt and UpdatedAt")
	}
}

func TestSaveRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("upjo", "/repo")); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	err := s.Save(newBinding("upjo2", "/repo"))
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestSaveAllowsRewritingSameBinding(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("upjo", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("rewriting the same name must not trip ErrCWDTaken: %v", err)
	}
}

func TestLoadMissingIsErrNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestPathsAreZeroPaddedUnderBindingDir(t *testing.T) {
	s := New("/state")
	if got, want := s.PlanPath("upjo", 3), filepath.Join("/state", "upjo", "003-plan.md"); got != want {
		t.Errorf("PlanPath = %q, want %q", got, want)
	}
	if got, want := s.ReportPath("upjo", 12), filepath.Join("/state", "upjo", "012-report.md"); got != want {
		t.Errorf("ReportPath = %q, want %q", got, want)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"upjo", "a", "money-ai", "x_1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "1abc", "Upjo", "has space", "way-too-long-a-binding-name-for-herdr"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", bad)
		}
	}
}
