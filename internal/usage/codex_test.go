package usage

import (
	"strings"
	"testing"
)

func TestCodexStream(t *testing.T) {
	got := codexStream(mustOpen(t, "testdata/codex-stream.jsonl"), "openai", "gpt-5.6-terra")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2", len(got))
	}
	if got[0].Tokens != (Tokens{In: 8541, CacheRead: 97024, CacheWrite: 0, Out: 419}) {
		t.Errorf("first = %+v", got[0].Tokens)
	}
	if got[1].Tokens != (Tokens{In: 600, CacheRead: 400, CacheWrite: 50, Out: 20}) {
		t.Errorf("second = %+v", got[1].Tokens)
	}
	for i, s := range got {
		if s.Provider != "openai" || s.Model != "gpt-5.6-terra" || s.HasCost {
			t.Errorf("sample %d = %+v, want Provider openai, Model gpt-5.6-terra, HasCost false", i, s)
		}
	}
}

func TestCodexStreamEmpty(t *testing.T) {
	got := codexStream(strings.NewReader(""), "openai", "gpt-5.6-terra")
	if len(got) != 0 {
		t.Errorf("samples = %d, want 0", len(got))
	}
}
