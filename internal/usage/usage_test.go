package usage

import (
	"strings"
	"testing"
)

func testPrices() Prices {
	return Prices{AsOf: "2026-09-18", Models: map[string]ModelPrice{
		// USD per million tokens. Round numbers so the sums are exact.
		"anthropic/claude-sonnet-5": {In: 3, CacheRead: 0.3, CacheWrite: 3.75, Out: 15},
		"google/gemini-3.8-flash":   {In: 0.5, CacheRead: 0.05, CacheWrite: 0, Out: 2},
	}}
}

func TestTokensArithmetic(t *testing.T) {
	a := Tokens{In: 10, CacheRead: 20, CacheWrite: 30, Out: 40}
	b := Tokens{In: 1, CacheRead: 2, CacheWrite: 3, Out: 4}
	got := a.Add(b)
	if got != (Tokens{In: 11, CacheRead: 22, CacheWrite: 33, Out: 44}) {
		t.Errorf("Add = %+v", got)
	}
	if got.Total() != 110 {
		t.Errorf("Total = %d, want 110", got.Total())
	}
	// 22 cache-read of 66 prompt tokens.
	if r := got.CacheRatio(); r < 0.333 || r > 0.334 {
		t.Errorf("CacheRatio = %v, want 1/3", r)
	}
	if (Tokens{Out: 5}).CacheRatio() != 0 {
		t.Error("CacheRatio with no prompt tokens must be 0, not NaN")
	}
}

func TestFoldNoSamplesIsUnknown(t *testing.T) {
	u := Fold(nil, testPrices(), false, "no stream")
	if u.Cost.Basis != Unknown || u.Samples != 0 || u.Note != "no stream" {
		t.Errorf("Fold(nil) = %+v", u)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0", u.Cost.USD)
	}
}

func TestFoldAllMeasured(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 100, Out: 10}, USD: 0.02, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 50, Out: 5}, USD: 0.01, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Cost.Basis != Measured {
		t.Fatalf("basis = %q, want measured", u.Cost.Basis)
	}
	if u.Cost.USD < 0.0299 || u.Cost.USD > 0.0301 {
		t.Errorf("USD = %v, want 0.03", u.Cost.USD)
	}
	if u.Tokens != (Tokens{In: 150, Out: 15}) || u.Samples != 2 {
		t.Errorf("tokens/samples = %+v/%d", u.Tokens, u.Samples)
	}
	if u.Model != "claude-sonnet-5" || u.Provider != "anthropic" {
		t.Errorf("model/provider = %q/%q", u.Model, u.Provider)
	}
	if u.Note != "" {
		t.Errorf("Note = %q, want empty for a single model", u.Note)
	}
}

func TestFoldEstimatesFromPrices(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5",
		Tokens: Tokens{In: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, Out: 1_000_000}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated", u.Cost.Basis)
	}
	// 3 + 0.3 + 3.75 + 15
	if u.Cost.USD < 22.049 || u.Cost.USD > 22.051 {
		t.Errorf("USD = %v, want 22.05", u.Cost.USD)
	}
}

func TestFoldMixedMeasuredAndEstimated(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}, USD: 5, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}},
	}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated when any sample is estimated", u.Cost.Basis)
	}
	if u.Cost.USD < 7.999 || u.Cost.USD > 8.001 { // 5 measured + 3 estimated
		t.Errorf("USD = %v, want 8", u.Cost.USD)
	}
}

func TestFoldUnpricedModelIsUnknownNeverZero(t *testing.T) {
	s := []Sample{{Provider: "google", Model: "gemini-9-ultra", Tokens: Tokens{In: 500, Out: 20}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Unknown {
		t.Fatalf("basis = %q, want unknown for an unpriced model", u.Cost.Basis)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0 under unknown", u.Cost.USD)
	}
	if !strings.Contains(u.Note, "no price for google/gemini-9-ultra") {
		t.Errorf("Note = %q, want the missing row named", u.Note)
	}
	if u.Tokens != (Tokens{In: 500, Out: 20}) {
		t.Errorf("tokens must still be recorded under unknown: %+v", u.Tokens)
	}
}

func TestFoldPicksModelWithMostOutput(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 10}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-opus-5", Tokens: Tokens{Out: 100}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 20}, USD: 1, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the model with most Out tokens", u.Model)
	}
	if u.Note != "2 models" {
		t.Errorf("Note = %q, want \"2 models\"", u.Note)
	}
}

func TestFoldCarriesPlanFlag(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1}, USD: 0, HasCost: true}}
	u := Fold(s, Prices{}, true, "")
	if !u.Cost.Plan {
		t.Error("Plan must be copied through")
	}
	if u.Cost.Basis != Measured {
		t.Errorf("basis = %q; a plan lane with a measured 0 stays measured, the printer says plan", u.Cost.Basis)
	}
}

func TestFoldNegativeCountIsZeroAndNoted(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: -5, Out: 3}, USD: 1, HasCost: true}}
	u := Fold(s, Prices{}, false, "")
	if u.Tokens.In != 0 || u.Tokens.Out != 3 {
		t.Errorf("tokens = %+v, want the negative clamped", u.Tokens)
	}
	if !strings.Contains(u.Note, "negative") {
		t.Errorf("Note = %q, want the clamp mentioned", u.Note)
	}
}
