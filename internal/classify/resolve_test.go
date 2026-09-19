package classify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/policy"
)

func TestResolve(t *testing.T) {
	t.Run("nil cfg -> nil classifier, Configured false", func(t *testing.T) {
		cls, st := Resolve(nil, t.TempDir(), os.Getenv)
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
		cls, st := Resolve(cfg, t.TempDir(), getenv)
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

	t.Run("file key (t.TempDir as configDir, relay/typesafe.key with trailing newline) -> KeySource file, key trimmed", func(t *testing.T) {
		cfgDir := t.TempDir()
		relayDir := filepath.Join(cfgDir, "relay")
		if err := os.MkdirAll(relayDir, 0o755); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(relayDir, "typesafe.key")
		if err := os.WriteFile(keyPath, []byte("  file-secret-key  \n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg := &policy.Classify{Provider: "jev", Model: "custom-model"}
		getenv := func(string) string { return "" }

		cls, st := Resolve(cfg, cfgDir, getenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "file" {
			t.Errorf("KeySource = %q, want file", st.KeySource)
		}
		if st.KeyPath != keyPath {
			t.Errorf("KeyPath = %q, want %q", st.KeyPath, keyPath)
		}
		client, ok := cls.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls)
		}
		if client.Key != "file-secret-key" {
			t.Errorf("client.Key = %q, want file-secret-key", client.Key)
		}
		if client.Model != "custom-model" {
			t.Errorf("client.Model = %q, want custom-model", client.Model)
		}
	})

	t.Run("env wins over file when both", func(t *testing.T) {
		cfgDir := t.TempDir()
		relayDir := filepath.Join(cfgDir, "relay")
		if err := os.MkdirAll(relayDir, 0o755); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(relayDir, "typesafe.key")
		if err := os.WriteFile(keyPath, []byte("file-secret-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(key string) string {
			if key == "TYPESAFE_API_KEY" {
				return "env-secret-key"
			}
			return ""
		}

		cls, st := Resolve(cfg, cfgDir, getenv)
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

	t.Run("neither -> Unavailable, KeySource empty, KeyPath ends in relay/typesafe.key, Judge returns ErrUnavailable", func(t *testing.T) {
		cfgDir := t.TempDir()
		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(string) string { return "" }

		cls, st := Resolve(cfg, cfgDir, getenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "" {
			t.Errorf("KeySource = %q, want empty", st.KeySource)
		}
		if !strings.HasSuffix(st.KeyPath, filepath.Join("relay", "typesafe.key")) {
			t.Errorf("KeyPath %q does not end with relay/typesafe.key", st.KeyPath)
		}

		unavail, ok := cls.(Unavailable)
		if !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
		_, err := unavail.Judge(context.Background(), Request{Source: "report", Paragraphs: []Paragraph{{Index: 0, Kind: KindProse, Text: "t", Line: 1, Lines: 1}}})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected ErrUnavailable, got %v", err)
		}
	})

	t.Run("empty file -> treated as no key", func(t *testing.T) {
		cfgDir := t.TempDir()
		relayDir := filepath.Join(cfgDir, "relay")
		if err := os.MkdirAll(relayDir, 0o755); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(relayDir, "typesafe.key")
		if err := os.WriteFile(keyPath, []byte("   \n\t  \n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(string) string { return "" }

		cls, st := Resolve(cfg, cfgDir, getenv)
		if st.KeySource != "" {
			t.Errorf("KeySource = %q, want empty", st.KeySource)
		}
		if _, ok := cls.(Unavailable); !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
	})

	t.Run("0644 key file -> Unavailable, KeySource empty, KeyFileLoose true, reason names mode; 0600 -> file key", func(t *testing.T) {
		cfgDir := t.TempDir()
		relayDir := filepath.Join(cfgDir, "relay")
		if err := os.MkdirAll(relayDir, 0o755); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(relayDir, "typesafe.key")
		if err := os.WriteFile(keyPath, []byte("file-secret-key\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := &policy.Classify{Provider: "jev"}
		getenv := func(string) string { return "" }

		// Loose permissions (0644)
		cls, st := Resolve(cfg, cfgDir, getenv)
		if !st.Configured {
			t.Fatal("expected Configured true")
		}
		if st.KeySource != "" {
			t.Errorf("KeySource = %q, want empty", st.KeySource)
		}
		if !st.KeyFileLoose {
			t.Errorf("KeyFileLoose = %v, want true", st.KeyFileLoose)
		}
		if st.KeyFileMode != 0o644 {
			t.Errorf("KeyFileMode = 0%o, want 0644", st.KeyFileMode)
		}

		unavail, ok := cls.(Unavailable)
		if !ok {
			t.Fatalf("expected Unavailable, got %T", cls)
		}
		if !strings.Contains(unavail.Reason, "0644") {
			t.Errorf("reason %q does not contain 0644", unavail.Reason)
		}
		_, err := unavail.Judge(context.Background(), Request{Source: "report", Paragraphs: []Paragraph{{Index: 0, Kind: KindProse, Text: "t", Line: 1, Lines: 1}}})
		if err == nil || !strings.Contains(err.Error(), "readable by others") {
			t.Errorf("Judge error %v does not contain 'readable by others'", err)
		}

		// Fixed permissions (0600)
		if err := os.Chmod(keyPath, 0o600); err != nil {
			t.Fatal(err)
		}

		cls2, st2 := Resolve(cfg, cfgDir, getenv)
		if !st2.Configured {
			t.Fatal("expected Configured true")
		}
		if st2.KeySource != "file" {
			t.Errorf("KeySource = %q, want file", st2.KeySource)
		}
		if st2.KeyFileLoose {
			t.Errorf("KeyFileLoose = %v, want false", st2.KeyFileLoose)
		}
		client, ok := cls2.(*Client)
		if !ok {
			t.Fatalf("expected *Client, got %T", cls2)
		}
		if client.Key != "file-secret-key" {
			t.Errorf("client.Key = %q, want file-secret-key", client.Key)
		}
	})
}
