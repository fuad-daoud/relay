package usage

import "testing"

func TestAgyStreamUsesResult(t *testing.T) {
	got := agyStream(mustOpen(t, "testdata/agy-stream.jsonl"), "google", "fallback-model")
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1", len(got))
	}
	s := got[0]
	if s.HasCost {
		t.Error("agy reports no dollars; HasCost must be false")
	}
	// Out = output 75 + thinking 15.
	if s.Tokens != (Tokens{In: 1500, CacheRead: 500, Out: 90}) {
		t.Errorf("tokens = %+v", s.Tokens)
	}
	if s.Model != "gemini-3.8-flash" || s.Provider != "google" {
		t.Errorf("model/provider = %q/%q, want the init event's model", s.Model, s.Provider)
	}
}

func TestAgyStreamKilledSumsSteps(t *testing.T) {
	got := agyStream(mustOpen(t, "testdata/agy-stream-killed.jsonl"), "google", "fallback-model")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 agent_response steps", len(got))
	}
	var sum Tokens
	for _, s := range got {
		sum = sum.Add(s.Tokens)
	}
	if sum != (Tokens{In: 1500, CacheRead: 500, Out: 90}) {
		t.Errorf("sum = %+v", sum)
	}
}

func TestAgyStreamNoInitUsesFallbackModel(t *testing.T) {
	f, _ := mustTemp(t, `{"event":"result","result":{"usage":{"input_tokens":1,"output_tokens":1,"thinking_tokens":0,"cache_read_tokens":0}}}`)
	got := agyStream(f, "google", "fallback-model")
	if len(got) != 1 || got[0].Model != "fallback-model" {
		t.Errorf("got %+v, want the candidate's model when the stream names none", got)
	}
}
