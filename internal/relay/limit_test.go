package relay

import (
	"regexp"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
)

func testNow() time.Time {
	return time.Date(2026, 9, 13, 23, 13, 0, 0, time.FixedZone("EEST", 3*3600))
}

func TestParseReset(t *testing.T) {
	now := testNow()
	loc := now.Location()

	cases := []struct {
		name string
		line string
		want time.Time
		ok   bool
	}{
		{
			name: "agy fixture duration",
			line: "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.",
			want: now.Add(2*time.Hour + 48*time.Minute + 52*time.Second),
			ok:   true,
		},
		{
			name: "clock 7pm rolls to tomorrow",
			line: "limit · resets 7pm",
			want: time.Date(2026, 9, 14, 19, 0, 0, 0, loc),
			ok:   true,
		},
		{
			name: "clock tilde minute rolls to tomorrow",
			line: "individual quota reached (resets ~00:26)",
			want: time.Date(2026, 9, 14, 0, 26, 0, 0, loc),
			ok:   true,
		},
		{
			name: "clock at today",
			line: "Resets at 23:30",
			want: time.Date(2026, 9, 13, 23, 30, 0, 0, loc),
			ok:   true,
		},
		{
			name: "long duration within 7 days",
			line: "error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 95h4m16s.",
			want: now.Add(95*time.Hour + 4*time.Minute + 16*time.Second),
			ok:   true,
		},
		{
			name: "try again in minutes",
			line: "try again in 5 min",
			want: now.Add(5 * time.Minute),
			ok:   true,
		},
		{
			name: "retry after seconds",
			line: "retry after 30s",
			want: now.Add(30 * time.Second),
			ok:   true,
		},
		{
			name: "no reset time at all",
			line: "You've hit your limit",
			ok:   false,
		},
		{
			name: "duration outside 7 day window",
			line: "try again in 400h",
			ok:   false,
		},
		{
			name: "clock out of range",
			line: "resets 99:99",
			ok:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseReset(c.line, now)
			if ok != c.ok {
				t.Fatalf("parseReset(%q) ok = %v, want %v", c.line, ok, c.ok)
			}
			if !c.ok {
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("parseReset(%q) = %v, want %v", c.line, got, c.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseReset(%q) location = %v, want UTC", c.line, got.Location())
			}
		})
	}
}

func agyPatterns(t *testing.T) []*regexp.Regexp {
	t.Helper()
	h, ok := harness.Lookup("agy")
	if !ok {
		t.Fatal(`harness.Lookup("agy") not found`)
	}
	patterns := make([]*regexp.Regexp, 0, len(h.LimitPatterns))
	for _, p := range h.LimitPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	return patterns
}

func TestMatchLimit(t *testing.T) {
	now := testNow()
	fallback := time.Hour

	t.Run("last matching line wins", func(t *testing.T) {
		text := "individual quota reached: first\njust chatting\nindividual quota reached: last"
		got, ok := matchLimit(text, agyPatterns(t), now, fallback)
		if !ok {
			t.Fatal("matchLimit ok = false, want true")
		}
		want := "individual quota reached: last"
		if got.Line != want {
			t.Errorf("Line = %q, want %q", got.Line, want)
		}
	})

	t.Run("agy fixture parses reset", func(t *testing.T) {
		text := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."
		got, ok := matchLimit(text, agyPatterns(t), now, fallback)
		if !ok {
			t.Fatal("matchLimit ok = false, want true")
		}
		if !got.Parsed {
			t.Error("Parsed = false, want true")
		}
		want := now.Add(2*time.Hour + 48*time.Minute + 52*time.Second)
		if !got.Until.Equal(want) {
			t.Errorf("Until = %v, want %v", got.Until, want)
		}
	})

	t.Run("no reset time uses fallback", func(t *testing.T) {
		text := "individual quota reached"
		got, ok := matchLimit(text, agyPatterns(t), now, fallback)
		if !ok {
			t.Fatal("matchLimit ok = false, want true")
		}
		if got.Parsed {
			t.Error("Parsed = true, want false")
		}
		want := now.Add(fallback)
		if !got.Until.Equal(want) {
			t.Errorf("Until = %v, want %v", got.Until, want)
		}
	})

	t.Run("no line matches", func(t *testing.T) {
		text := "starting\nboom: out of tokens\nrelay-exit:3"
		_, ok := matchLimit(text, agyPatterns(t), now, fallback)
		if ok {
			t.Error("ok = true, want false")
		}
	})

	t.Run("nil patterns never match", func(t *testing.T) {
		text := "Individual quota reached. Resets in 2h48m52s."
		_, ok := matchLimit(text, nil, now, fallback)
		if ok {
			t.Error("ok = true, want false")
		}
	})

	t.Run("matched line capped at 200 runes", func(t *testing.T) {
		filler := ""
		for len(filler) < 300 {
			filler += "x"
		}
		text := "individual quota reached " + filler
		got, ok := matchLimit(text, agyPatterns(t), now, fallback)
		if !ok {
			t.Fatal("matchLimit ok = false, want true")
		}
		if n := len([]rune(got.Line)); n != 200 {
			t.Errorf("len([]rune(Line)) = %d, want 200", n)
		}
	})
}
