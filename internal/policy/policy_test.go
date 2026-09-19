package policy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestLimitGateDefault(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     time.Duration
		wantErr  bool
		contains []string
	}{
		{name: "absent defaults", body: `{}`, want: DefaultLimitGate},
		{name: "custom duration", body: `{"limit_gate_default_ms":1800000}`, want: 30 * time.Minute},
		{
			name:     "zero",
			body:     `{"limit_gate_default_ms":0}`,
			wantErr:  true,
			contains: []string{"limit_gate_default_ms", "must be > 0"},
		},
		{
			name:     "negative",
			body:     `{"limit_gate_default_ms":-5}`,
			wantErr:  true,
			contains: []string{"limit_gate_default_ms", "must be > 0"},
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
			if got := p.LimitGateDefault(); got != tt.want {
				t.Fatalf("LimitGateDefault() = %v, want %v", got, tt.want)
			}
		})
	}

	if got := (Policy{}).LimitGateDefault(); got != DefaultLimitGate {
		t.Fatalf("Policy{}.LimitGateDefault() = %v, want %v", got, DefaultLimitGate)
	}
}

func TestScanPatterns(t *testing.T) {
	t.Run("bad regex returns ErrBadPolicy naming index 0", func(t *testing.T) {
		body := `{"scan_patterns": ["("]}`
		_, err := load(t, body)
		if err == nil {
			t.Fatalf("Load: got nil error, want ErrBadPolicy")
		}
		if !errors.Is(err, ErrBadPolicy) {
			t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
		}
		if !strings.Contains(err.Error(), "scan_patterns[0]") {
			t.Fatalf("Load error %q does not name index 0 (scan_patterns[0])", err.Error())
		}
	})

	t.Run("valid patterns load", func(t *testing.T) {
		body := `{"scan_patterns": ["foo.*bar", "(?i)baz"]}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		want := []string{"foo.*bar", "(?i)baz"}
		if !reflect.DeepEqual(p.ScanPatterns, want) {
			t.Fatalf("ScanPatterns = %v, want %v", p.ScanPatterns, want)
		}
	})
}

func TestClassifyPolicy(t *testing.T) {
	t.Run("nil receiver accessors return defaults", func(t *testing.T) {
		var c *Classify
		if got := c.ModelName(); got != DefaultClassifyModel {
			t.Errorf("ModelName() = %q, want %q", got, DefaultClassifyModel)
		}
		if got := c.Threshold(); got != DefaultInjectionThreshold {
			t.Errorf("Threshold() = %v, want %v", got, DefaultInjectionThreshold)
		}
		if got := c.Timeout(); got != DefaultClassifyTimeout {
			t.Errorf("Timeout() = %v, want %v", got, DefaultClassifyTimeout)
		}
	})

	t.Run("valid block with defaults", func(t *testing.T) {
		body := `{"classify":{"provider":"jev"}}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		if p.Classify == nil {
			t.Fatal("Classify is nil")
		}
		if got := p.Classify.ModelName(); got != "jev-latest" {
			t.Errorf("ModelName() = %q, want jev-latest", got)
		}
		if got := p.Classify.Threshold(); got != 0.7 {
			t.Errorf("Threshold() = %v, want 0.7", got)
		}
		if got := p.Classify.Timeout(); got != 4*time.Second {
			t.Errorf("Timeout() = %v, want 4s", got)
		}
	})

	t.Run("explicit values honoured", func(t *testing.T) {
		body := `{"classify":{"provider":"jev","model":"jev-v2","injection_threshold":0.85,"timeout_ms":2500}}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		if p.Classify == nil {
			t.Fatal("Classify is nil")
		}
		if got := p.Classify.ModelName(); got != "jev-v2" {
			t.Errorf("ModelName() = %q, want jev-v2", got)
		}
		if got := p.Classify.Threshold(); got != 0.85 {
			t.Errorf("Threshold() = %v, want 0.85", got)
		}
		if got := p.Classify.Timeout(); got != 2500*time.Millisecond {
			t.Errorf("Timeout() = %v, want 2.5s", got)
		}
	})

	t.Run("1.0 accepted", func(t *testing.T) {
		body := `{"classify":{"provider":"jev","injection_threshold":1.0}}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		if got := p.Classify.Threshold(); got != 1.0 {
			t.Errorf("Threshold() = %v, want 1.0", got)
		}
	})

	badCases := []struct {
		name     string
		body     string
		contains string
	}{
		{"provider missing", `{"classify":{}}`, "classify.provider: required"},
		{"provider other", `{"classify":{"provider":"other"}}`, "classify.provider: unknown \"other\" (known: jev)"},
		{"threshold 0", `{"classify":{"provider":"jev","injection_threshold":0}}`, "classify.injection_threshold: must be in (0, 1], got 0"},
		{"threshold negative", `{"classify":{"provider":"jev","injection_threshold":-0.1}}`, "classify.injection_threshold: must be in (0, 1], got -0.1"},
		{"threshold 1.5", `{"classify":{"provider":"jev","injection_threshold":1.5}}`, "classify.injection_threshold: must be in (0, 1], got 1.5"},
		{"timeout 0", `{"classify":{"provider":"jev","timeout_ms":0}}`, "classify.timeout_ms: must be > 0, got 0"},
		{"timeout negative", `{"classify":{"provider":"jev","timeout_ms":-10}}`, "classify.timeout_ms: must be > 0, got -10"},
		{"unknown key inside classify", `{"classify":{"provider":"jev","unknown_key":true}}`, "unknown_key"},
	}

	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}
