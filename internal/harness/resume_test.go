package harness

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestResumePerKind pins the resume argv for every kind that has one: the
// print form's shape, the harness's own session flag, and the tier's
// permission args appended after the base form exactly as Launch appends
// them. opencode has no read-only form, so a resume at read is refused and
// the caller asks at harness (--fork keeps the original session untouched);
// codex has no verified resume form at all.
//
// Mutation check: rendering the plain headless print form instead of the
// resume form leaves out --resume/--conversation/--session and fails here.
func TestResumePerKind(t *testing.T) {
	const prompt = "you built round 1"
	tests := []struct {
		name    string
		kind    string
		id      string
		tier    Tier
		want    []string
		wantErr error
		refuse  bool
	}{
		{
			name: "claude", kind: "claude", id: "sess-1", tier: TierRead,
			want: []string{"-p", prompt, "--resume", "sess-1", "--output-format", "stream-json", "--verbose",
				"--permission-mode", "plan"},
		},
		{
			name: "agy", kind: "agy", id: "conv-1", tier: TierRead,
			want: []string{"-p", prompt, "--conversation", "conv-1", "--output-format", "stream-json",
				"--mode", "plan"},
		},
		{
			name: "opencode", kind: "opencode", id: "ses-1", tier: TierHarness,
			want: []string{"run", prompt, "--session", "ses-1", "--fork", "--format", "json", "--standalone"},
		},
		{
			name: "opencode read", kind: "opencode", id: "ses-1", tier: TierRead,
			wantErr: ErrTierUnsupported,
		},
		{
			name: "codex", kind: "codex", id: "thread-1", tier: TierHarness,
			wantErr: ErrResumeUnsupported,
		},
		{
			name: "empty id", kind: "claude", id: "", tier: TierRead,
			refuse: true,
		},
		{
			name: "flag-shaped id", kind: "claude", id: "-x", tier: TierRead,
			refuse: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got, err := h.Resume(tt.id, prompt, tt.tier)
			switch {
			case tt.refuse:
				if err == nil {
					t.Fatalf("Resume(%q) = %v, want a refusal", tt.id, got)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Resume() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Resume() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Resume() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestResumeRefusalNamesTheKind keeps the refusal actionable: the caller
// passes ErrResumeUnsupported through with the session's kind in it, and a
// bare sentinel would say only that "some harness" cannot resume.
func TestResumeRefusalNamesTheKind(t *testing.T) {
	h, ok := Lookup("codex")
	if !ok {
		t.Fatal("Lookup(\"codex\") not found")
	}
	_, err := h.Resume("thread-1", "p", TierHarness)
	if !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("error = %v, want ErrResumeUnsupported", err)
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error = %q, want it to name codex", err)
	}
}

// TestResumeRefusesWhitespace keeps the session id out of any position where
// it could split into two argv elements: the id becomes one argument.
func TestResumeRefusesWhitespace(t *testing.T) {
	h, _ := Lookup("claude")
	for _, id := range []string{"a b", "a\tb", "a\nb"} {
		if _, err := h.Resume(id, "p", TierRead); err == nil {
			t.Errorf("Resume(%q) = nil error, want a refusal", id)
		}
	}
}

// TestResumeBuildPerKind pins the lost builder's resume argv (#370, spec
// §4.10): the builder-grade print form the round was started with, model flag
// included, followed by the harness's own selector. The Launch is built by
// Launch itself, so a selector that could not be combined with the round's
// own flags would show up here.
//
// Mutation check: returning the plain print form instead of PrintArgs plus
// the selector leaves --resume/--conversation/--session out and fails every
// ok case; claiming a selector for codex fails the codex row.
func TestResumeBuildPerKind(t *testing.T) {
	const (
		provider = "test"
		model    = "m"
		prompt   = "finish round 1"
		dir      = "/repo"
		state    = "/state"
	)
	budget := 2 * time.Hour
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}

	tests := []struct {
		name    string
		kind    string
		id      string
		want    []string
		wantErr error
		refuse  bool
	}{
		{
			name: "claude", kind: "claude", id: "sess-1",
			want: []string{"-p", prompt, "--model", model, "--agent", "plan-executor",
				"--output-format", "stream-json", "--verbose", "--resume", "sess-1"},
		},
		{
			name: "agy", kind: "agy", id: "conv-1",
			want: []string{"-p", prompt, "--model", model, "--agent", "plan-executor",
				"--output-format", "stream-json", "--print-timeout", "2h0m0s",
				"--add-dir", dir, "--conversation", "conv-1"},
		},
		{
			name: "opencode", kind: "opencode", id: "ses-1",
			want: []string{"run", prompt, "-m", provider + "/" + model, "--agent", "plan-executor",
				"--format", "json", "--standalone", "--session", "ses-1", "--fork"},
		},
		{
			name: "codex", kind: "codex", id: "thread-1",
			wantErr: ErrResumeUnsupported,
		},
		{
			name: "unknown kind", kind: "future", id: "id-1",
			wantErr: ErrResumeUnsupported,
		},
		{
			name: "empty id", kind: "claude", id: "",
			refuse: true,
		},
		{
			name: "whitespace id", kind: "claude", id: "a b",
			refuse: true,
		},
		{
			name: "flag-shaped id", kind: "claude", id: "-x",
			refuse: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Harness{Kind: tt.kind}
			if known, ok := Lookup(tt.kind); ok {
				h = known
			}
			l, err := h.Launch(provider, model, nil, builder, TierHarness)
			if err != nil {
				t.Fatalf("Launch(%s): %v", tt.kind, err)
			}
			got, err := h.ResumeBuild(tt.id, l, prompt, budget, dir, state)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResumeBuild() error = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("ResumeBuild() = %v, want nil alongside the refusal", got)
				}
			case tt.refuse:
				if err == nil {
					t.Fatalf("ResumeBuild(%q) = %v, want a refusal", tt.id, got)
				}
			default:
				if err != nil {
					t.Fatalf("ResumeBuild() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("ResumeBuild() = %v, want %v", got, tt.want)
				}
				// The result is the round's print form and then the
				// selector -- nothing else, and in that order.
				base := l.PrintArgs(prompt, budget, dir, state)
				if len(got) < len(base) || !reflect.DeepEqual(got[:len(base)], base) {
					t.Errorf("ResumeBuild() = %v, want it to start with PrintArgs %v", got, base)
				}
			}
		})
	}
}
