package usage

import "testing"

func TestMoney(t *testing.T) {
	cases := []struct {
		c    Cost
		want string
	}{
		{Cost{USD: 0.41, Basis: Measured}, "$0.41"},
		{Cost{USD: 0.41, Basis: Estimated}, "~$0.41"},
		{Cost{USD: 0.41, Basis: Measured, Plan: true}, "plan"},
		{Cost{USD: 0, Basis: Unknown}, "unknown"},
		{Cost{USD: 0, Basis: Measured}, "$0.00"},
		{Cost{USD: 0.004, Basis: Measured}, "<$0.01"},
		{Cost{USD: 0.005, Basis: Estimated}, "~$0.01"},
		{Cost{USD: 12.346, Basis: Measured}, "$12.35"},
	}
	for _, c := range cases {
		if got := Money(c.c); got != c.want {
			t.Errorf("Money(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestShortTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1k", 1500: "2k", 182_400: "182k", 999_499: "999k", 999_500: "1.0M", 2_340_000: "2.3M"}
	for in, want := range cases {
		if got := ShortTokens(in); got != want {
			t.Errorf("ShortTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[int64]string{0: "", 1: "<1m", 59_999: "<1m", 60_000: "1m", 14 * 60_000: "14m", 65 * 60_000: "1h05m", 3 * 3_600_000: "3h00m"}
	for in, want := range cases {
		if got := ShortDuration(in); got != want {
			t.Errorf("ShortDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLine(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want string
	}{
		{
			name: "estimated, the issue's example",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 14 * 60_000,
				Tokens: Tokens{In: 16_000, CacheRead: 166_000, CacheWrite: 400, Out: 12_000},
				Cost:   Cost{USD: 0.41, Basis: Estimated}, Samples: 3},
			want: "claude/anthropic/claude-sonnet-5  14m  in 182k (cache 91%)  out 12k  ~$0.41",
		},
		{
			name: "measured",
			u: Usage{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash", DurationMS: 90_000,
				Tokens: Tokens{In: 465_339, CacheRead: 6_656_256, Out: 57_612}, Cost: Cost{USD: 0.2983, Basis: Measured}, Samples: 95},
			want: "opencode/openrouter/z-ai/glm-5.3-flash  1m  in 7.1M (cache 93%)  out 58k  $0.30",
		},
		{
			name: "unknown with tokens",
			u: Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000,
				Tokens: Tokens{In: 680_097, CacheRead: 2_333_950, Out: 41_527}, Cost: Cost{Basis: Unknown}, Samples: 1,
				Note: "no price for google/gemini-3.8-flash-high"},
			want: "agy/google/gemini-3.8-flash-high  9m  in 3.0M (cache 77%)  out 42k  unknown (no price for google/gemini-3.8-flash-high)",
		},
		{
			name: "unknown, no samples",
			u:    Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000, Cost: Cost{Basis: Unknown}, Note: "agy keeps no usage record"},
			want: "agy/google/gemini-3.8-flash-high  9m  unknown (agy keeps no usage record)",
		},
		{
			name: "adopted builder, nothing known",
			u:    Usage{Harness: "claude", Cost: Cost{Basis: Unknown}, Note: "no stream"},
			want: "claude  unknown (no stream)",
		},
		{
			name: "plan lane",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5", DurationMS: 60_000,
				Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 1, Basis: Estimated, Plan: true}, Samples: 1},
			want: "claude/anthropic/claude-opus-5  1m  in 100 (cache 0%)  out 50  plan",
		},
		{
			name: "no prompt tokens, no cache parenthetical",
			u:    Usage{Harness: "claude", Provider: "anthropic", Model: "m", Tokens: Tokens{Out: 5}, Cost: Cost{USD: 0.01, Basis: Measured}, Samples: 1},
			want: "claude/anthropic/m  in 0  out 5  $0.01",
		},
	}
	for _, c := range cases {
		if got := Line(c.u); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
