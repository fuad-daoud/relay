package classify

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/policy"
)

func TestResolve(t *testing.T) {
	t.Run("nil cfg -> nil classifier, Configured false", func(t *testing.T) {
		cls, st := Resolve(nil, "", osGetenv)
		if cls != nil {
			t.Errorf("expected nil classifier, got %v", cls)
		}
		if st.Configured {
			t.Errorf("expected Configured false, got true")
		}
	})

	t.Run("env key -> *Client with Key, Model from cfg, KeySource env", func(t *testing.T) {
		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(key string) string {
			if key == "TYPESAFE_API_KEY" {
				return "  test-env-key  \n"
			}
			return ""
		}
		cls, st := Resolve(cfg, "", getenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "env" {
			t.Errorf("KeySource = %q, want env", st.KeySource)
		}
		if st.Model != "jev-latest" {
			t.Errorf("Model = %q, want jev-latest", st.Model)
		}
		client, ok := cls.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls)
		}
		if client.Key != "test-env-key" {
			t.Errorf("client.Key = %q, want test-env-key", client.Key)
		}
		if client.Model != "jev-latest" {
			t.Errorf("client.Model = %q, want jev-latest", client.Model)
		}
	})

	t.Run("db key (trailing newline) -> KeySource db, key trimmed", func(t *testing.T) {
		cfg := &policy.Classify{Provider: "jev", Model: "custom-model"}
		cls, st := Resolve(cfg, "  db-secret-key  \n", osGetenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "db" {
			t.Errorf("KeySource = %q, want db", st.KeySource)
		}
		client, ok := cls.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls)
		}
		if client.Key != "db-secret-key" {
			t.Errorf("client.Key = %q, want db-secret-key", client.Key)
		}
		if client.Model != "custom-model" {
			t.Errorf("client.Model = %q, want custom-model", client.Model)
		}
	})

	t.Run("env wins over the db key when both", func(t *testing.T) {
		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(key string) string {
			if key == "TYPESAFE_API_KEY" {
				return "env-secret-key"
			}
			return ""
		}
		cls, st := Resolve(cfg, "db-secret-key", getenv)
		if st.KeySource != "env" {
			t.Errorf("KeySource = %q, want env", st.KeySource)
		}
		client, ok := cls.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls)
		}
		if client.Key != "env-secret-key" {
			t.Errorf("client.Key = %q, want env-secret-key", client.Key)
		}
	})

	t.Run("neither -> Unavailable, KeySource empty, Judge returns ErrUnavailable", func(t *testing.T) {
		cfg := &policy.Classify{Provider: "jev"}
		cls, st := Resolve(cfg, "", osGetenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "" {
			t.Errorf("KeySource = %q, want empty", st.KeySource)
		}

		unavail, ok := cls.(Unavailable)
		if !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
		if unavail.Reason != "no classifier key" {
			t.Errorf("Reason = %q, want %q", unavail.Reason, "no classifier key")
		}
		_, err := unavail.Judge(context.Background(), Request{Source: "report", Paragraphs: []Paragraph{{Index: 0, Kind: KindProse, Text: "t", Line: 1, Lines: 1}}})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected ErrUnavailable, got %v", err)
		}
	})

	t.Run("blank db key -> treated as no key", func(t *testing.T) {
		cfg := &policy.Classify{Provider: "jev"}
		cls, st := Resolve(cfg, "   \n\t  \n", osGetenv)
		if st.KeySource != "" {
			t.Errorf("KeySource = %q, want empty", st.KeySource)
		}
		if _, ok := cls.(Unavailable); !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
	})
}

func osGetenv(string) string { return "" }
