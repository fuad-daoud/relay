package policy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, body string) (Policy, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadMissingFileIsZeroPolicy(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Order) != 0 {
		t.Fatalf("Order = %v, want empty", p.Order)
	}
	if got := p.OrderFor("builder"); got != nil {
		t.Fatalf("OrderFor(%q) = %v, want nil", "builder", got)
	}
}

func TestLoadValidPolicy(t *testing.T) {
	body := `{"order":{"builder":["agy/google/m","claude/anthropic/sonnet"],"reviewer":["claude/anthropic/opus"]}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantBuilder := []string{"agy/google/m", "claude/anthropic/sonnet"}
	if got := p.OrderFor("builder"); !reflect.DeepEqual(got, wantBuilder) {
		t.Fatalf("OrderFor(%q) = %v, want %v", "builder", got, wantBuilder)
	}

	wantReviewer := []string{"claude/anthropic/opus"}
	if got := p.OrderFor("reviewer"); !reflect.DeepEqual(got, wantReviewer) {
		t.Fatalf("OrderFor(%q) = %v, want %v", "reviewer", got, wantReviewer)
	}

	if got := p.OrderFor("researcher"); got != nil {
		t.Fatalf("OrderFor(%q) = %v, want nil", "researcher", got)
	}
}

func TestOrderReturnsACopy(t *testing.T) {
	body := `{"order":{"builder":["agy/google/m","claude/anthropic/sonnet"],"reviewer":["claude/anthropic/opus"]}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := p.OrderFor("builder")
	got[0] = "x"

	if again := p.OrderFor("builder"); again[0] != "agy/google/m" {
		t.Fatalf("OrderFor(%q)[0] = %q after mutating a prior copy, want %q", "builder", again[0], "agy/google/m")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "bad json",
			body:     `{`,
			contains: []string{"policy.json"},
		},
		{
			name:     "unknown top-level key",
			body:     `{"orders":{}}`,
			contains: []string{"orders"},
		},
		{
			name:     "unknown role",
			body:     `{"order":{"reviwer":["a/b/c"]}}`,
			contains: []string{"order.reviwer", "unknown role", "builder reviewer researcher"},
		},
		{
			name:     "null list",
			body:     `{"order":{"builder":null}}`,
			contains: []string{"order.builder", "must be an array"},
		},
		{
			name:     "bad token",
			body:     `{"order":{"builder":["claude/sonnet"]}}`,
			contains: []string{"order.builder[0]", "harness/provider/model"},
		},
		{
			name:     "empty token",
			body:     `{"order":{"builder":[""]}}`,
			contains: []string{"order.builder[0]"},
		},
		{
			name:     "duplicate",
			body:     `{"order":{"builder":["a/b/c","x/y/z","a/b/c"]}}`,
			contains: []string{"order.builder[2]", "duplicate", "a/b/c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.body)
			if err == nil {
				t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Load error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestSwitchLimit(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     int
		wantErr  bool
		contains []string
	}{
		{name: "absent defaults", body: `{}`, want: DefaultMaxSwitches},
		{name: "zero disables switching", body: `{"max_switches":0}`, want: 0},
		{name: "positive", body: `{"max_switches":5}`, want: 5},
		{
			name:     "negative",
			body:     `{"max_switches":-1}`,
			wantErr:  true,
			contains: []string{"max_switches", "must be >= 0"},
		},
		{
			name:    "wrong type",
			body:    `{"max_switches":"two"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := load(t, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
				}
				for _, want := range tt.contains {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("Load error %q does not contain %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := p.SwitchLimit(); got != tt.want {
				t.Fatalf("SwitchLimit() = %d, want %d", got, tt.want)
			}
		})
	}

	if got := (Policy{}).SwitchLimit(); got != DefaultMaxSwitches {
		t.Fatalf("Policy{}.SwitchLimit() = %d, want %d", got, DefaultMaxSwitches)
	}
}

func TestLoadEmptyOrderIsValid(t *testing.T) {
	for _, body := range []string{`{"order":{}}`, `{}`} {
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if got := p.OrderFor("builder"); got != nil {
			t.Fatalf("Load(%q): OrderFor(%q) = %v, want nil", body, "builder", got)
		}
	}
}
