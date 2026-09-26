package stats

import (
	"testing"
)

// TestFitKey pins FitKey's padding, right-clipping, and left-clipping (ported from TestRenderKeyFitting, D7.2).
func TestFitKey(t *testing.T) {
	t.Parallel()

	if got := FitKey("abc", 5, false); got != "abc  " {
		t.Errorf("FitKey pad = %q, want %q", got, "abc  ")
	}
	if got := FitKey("abcdefgh", 5, false); got != "abcd…" {
		t.Errorf("FitKey right clip = %q, want %q", got, "abcd…")
	}
	if got := FitKey("abcdefgh", 5, true); got != "…efgh" {
		t.Errorf("FitKey left clip = %q, want %q", got, "…efgh")
	}
}

// TestShortTokens covers F2's k/M/B table, including the stripped ".0".
func TestShortTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		n    int64
		want string
	}{
		{999, "999"},
		{1500, "1.5k"},
		{2_000_000, "2M"},
		{1_195_000_000, "1.2B"},
		{1_000_000_000, "1B"},
	}
	for _, c := range cases {
		if got := ShortTokens(c.n); got != c.want {
			t.Errorf("ShortTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestPctText(t *testing.T) {
	t.Parallel()

	if got := PctText(85.4, 0); got != "-" {
		t.Errorf("PctText(closed=0) = %q, want -", got)
	}
	if got := PctText(85.4, 10); got != "85%" {
		t.Errorf("PctText(85.4, 10) = %q, want 85%%", got)
	}
}

func TestTTFTText(t *testing.T) {
	t.Parallel()

	if got := TTFTText(ScoreRow{HasTTFT: false}); got != "-" {
		t.Errorf("TTFTText(false) = %q, want -", got)
	}
	if got := TTFTText(ScoreRow{HasTTFT: true, TTFTMS: 2500}); got != "2.5s" {
		t.Errorf("TTFTText(2500) = %q, want 2.5s", got)
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()

	if got := Duration(150_000); got != "2m" {
		t.Errorf("Duration(150000) = %q, want 2m", got)
	}
	if got := Duration(3_900_000); got != "1h05m" {
		t.Errorf("Duration(3900000) = %q, want 1h05m", got)
	}
}

func TestMonthDay(t *testing.T) {
	t.Parallel()

	if got := MonthDay("2026-09-25"); got != "09-25" {
		t.Errorf("MonthDay(2026-09-25) = %q, want 09-25", got)
	}
	if got := MonthDay("short"); got != "short" {
		t.Errorf("MonthDay(short) = %q, want short", got)
	}
}
