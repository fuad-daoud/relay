package relay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
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

// gateOnLimitSetup is TestReconcileHeadlessGatedKillsAndSwitches's own setup
// (two-provider set, headless bind on agy/other/m, the three-builder order,
// one Send), without the Unavailable call gateOnLimit is meant to replace.
func gateOnLimitSetup(t *testing.T, f *fakeHerdr, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	f.agents = []herdr.Agent{plannerAgent()}
	rt := newRuntime(t, f)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", "agy/other/m", testClaudeRef, testOpencodeRef)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/other/m", PlannerPane: "w2:p3", CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

// gateHeadless runs gateOnLimit on b inside the lock, the way switchHeadless
// runs switchBuilder.
func gateHeadless(t *testing.T, rt Runtime, b store.Binding, text string, closeOld bool) (store.Binding, LimitMatch, bool, error) {
	t.Helper()
	var next store.Binding
	var m LimitMatch
	var handled bool
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, m, handled, err = gateOnLimit(context.Background(), rt, tx, b, text, closeOld)
		return err
	})
	return next, m, handled, err
}

func rateLimitedEntries(l ledger.Ledger) []ledger.Entry {
	var out []ledger.Entry
	for _, e := range l.Entries {
		if e.Kind == ledger.RateLimited {
			out = append(out, e)
		}
	}
	return out
}

const gateFixtureLine = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."

func TestGateOnLimit(t *testing.T) {
	t.Run("match, no report", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, f, fr)
		rt = at(rt, time.Minute)

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if !handled {
			t.Fatal("handled = false, want true")
		}

		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1: %+v", len(rl), rl)
		}
		e := rl[0]
		if e.Subject != "other" || e.Source != "relay" || e.Binding != "webshop" || e.Note != gateFixtureLine {
			t.Errorf("entry = %+v, want Subject=other Source=relay Binding=webshop Note=%q", e, gateFixtureLine)
		}
		wantUntil := rt.Now().Add(2*time.Hour + 48*time.Minute + 52*time.Second).UTC()
		if !e.Until.Equal(wantUntil) {
			t.Errorf("Until = %v, want %v", e.Until, wantUntil)
		}
		if !m.Until.Equal(wantUntil) || !m.Parsed {
			t.Errorf("m = %+v, want Until=%v Parsed=true", m, wantUntil)
		}

		sw := switches(t, rt)
		if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (rate-limited: Individual quota reached") {
			t.Errorf("switch entries = %+v", sw)
		}
		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
		}
		if got.BuilderCandidate != testClaudeRef {
			t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
		}

		data, err := os.ReadFile(b.Builder.LogPath)
		if err != nil {
			t.Fatalf("ReadFile log: %v", err)
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, "rate-limited: Individual quota reached") {
			t.Errorf("last log line = %q, want it to contain the rate-limited marker", last)
		}
	})

	t.Run("no match", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, f, fr)

		got, m, handled, err := gateHeadless(t, rt, b, "boom: out of tokens", false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != "" {
			t.Errorf("m.Line = %q, want empty", m.Line)
		}
		if l := loadLedger(t, rt); len(l.Entries) != 0 {
			t.Errorf("ledger entries = %+v, want none", l.Entries)
		}
		if sw := switches(t, rt); len(sw) != 0 {
			t.Errorf("switch entries = %+v, want none", sw)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("got = %+v, want unchanged b %+v", got, b)
		}
	})

	t.Run("match with a report on disk", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, f, fr)
		rt = at(rt, time.Minute)
		reportPath := rt.Store.ReportPath("webshop", 1)
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reportPath, []byte("done"), 0o644); err != nil {
			t.Fatal(err)
		}
		specsBefore := len(fr.specs)

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != gateFixtureLine {
			t.Errorf("m.Line = %q, want %q", m.Line, gateFixtureLine)
		}
		rl := rateLimitedEntries(loadLedger(t, rt))
		if len(rl) != 1 {
			t.Fatalf("rate_limited entries = %d, want 1", len(rl))
		}
		if sw := switches(t, rt); len(sw) != 0 {
			t.Errorf("switch entries = %+v, want none", sw)
		}
		if len(fr.specs) != specsBefore {
			t.Errorf("fr.specs grew from %d to %d, want no new process", specsBefore, len(fr.specs))
		}
		if got.BuilderCandidate != b.BuilderCandidate {
			t.Errorf("BuilderCandidate changed to %q, want unchanged %q", got.BuilderCandidate, b.BuilderCandidate)
		}
	})

	t.Run("not switchable", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, f, fr)
		b.BuilderCandidate = ""

		got, m, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if handled {
			t.Error("handled = true, want false")
		}
		if m.Line != "" {
			t.Errorf("m.Line = %q, want empty", m.Line)
		}
		if l := loadLedger(t, rt); len(l.Entries) != 0 {
			t.Errorf("ledger entries = %+v, want none", l.Entries)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("got = %+v, want unchanged b %+v", got, b)
		}
	})

	t.Run("ledger write failure", func(t *testing.T) {
		f := &fakeHerdr{}
		fr := newFakeRunner()
		rt, b := gateOnLimitSetup(t, f, fr)
		rt.LedgerPath = t.TempDir() // a directory, so ledger.Save fails

		_, _, handled, err := gateHeadless(t, rt, b, gateFixtureLine, false)
		if err != nil {
			t.Fatalf("gateOnLimit: %v", err)
		}
		if !handled {
			t.Error("handled = false, want true -- a ledger write failure must not block the switch")
		}
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Kind == store.KindSwitch {
				found = true
			}
		}
		if !found {
			t.Error("no switch log entry, want the switch to have happened despite the ledger write failure")
		}
	})
}
