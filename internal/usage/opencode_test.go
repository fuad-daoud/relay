package usage

import "testing"

func TestOpencodeStreamOneSamplePerStep(t *testing.T) {
	got := opencodeStream(mustOpen(t, "testdata/opencode-stream.jsonl"), "openrouter", "z-ai/glm-5.3-flash")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 step_finish parts", len(got))
	}
	// Out = output + reasoning.
	if got[0].Tokens != (Tokens{In: 1000, CacheRead: 10, CacheWrite: 20, Out: 106}) {
		t.Errorf("first = %+v", got[0].Tokens)
	}
	if !got[0].HasCost || got[0].USD != 0.001 {
		t.Errorf("first cost = %v/%v, want measured 0.001", got[0].USD, got[0].HasCost)
	}
	if got[1].Model != "z-ai/glm-5.3-flash" || got[1].Provider != "openrouter" {
		t.Errorf("model/provider = %q/%q, want the candidate's (the stream has none)", got[1].Model, got[1].Provider)
	}
}
