package histq

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var parseNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func TestParseEveryKey(t *testing.T) {
	cases := []struct {
		token string
		check func(t *testing.T, q Query)
	}{
		{"binding:api", func(t *testing.T, q Query) { eqStr(t, "binding", q.Filter.Binding, "api") }},
		{"repo:https://github.com/o/r", func(t *testing.T, q Query) {
			eqStr(t, "repo", q.Filter.Repo, "https://github.com/o/r")
		}},
		{"feature:checkout", func(t *testing.T, q Query) { eqStr(t, "feature", q.Filter.Feature, "checkout") }},
		{"planner:sess-1", func(t *testing.T, q Query) { eqStr(t, "planner", q.Filter.Planner, "sess-1") }},
		{"harness:agy", func(t *testing.T, q Query) { eqStr(t, "harness", q.Filter.Harness, "agy") }},
		{"provider:anthropic", func(t *testing.T, q Query) { eqStr(t, "provider", q.Filter.Provider, "anthropic") }},
		{"model:sonnet", func(t *testing.T, q Query) { eqStr(t, "model", q.Filter.Model, "sonnet") }},
		{"candidate:agy/antigravity/sonnet", func(t *testing.T, q Query) {
			eqStr(t, "candidate", q.Filter.Candidate, "agy/antigravity/sonnet")
		}},
		{"outcome:halted", func(t *testing.T, q Query) { eqStr(t, "outcome", q.Filter.Outcome, "halted") }},
		{"report:done", func(t *testing.T, q Query) { eqStr(t, "report", q.Report, "done") }},
		{"state:needs_you", func(t *testing.T, q Query) { eqStr(t, "state", q.Filter.State, "needs_you") }},
		{"gate:pass", func(t *testing.T, q Query) { eqStr(t, "gate", q.Gate, "pass") }},
		{"basis:measured", func(t *testing.T, q Query) { eqStr(t, "basis", q.Basis, "measured") }},
		{"server:contabo", func(t *testing.T, q Query) { eqStr(t, "server", q.Server, "contabo") }},
		{"mode:remote", func(t *testing.T, q Query) { eqStr(t, "mode", q.Mode, "remote") }},
		{"round:2", func(t *testing.T, q Query) {
			if q.Filter.Round != 2 {
				t.Errorf("Filter.Round = %d, want 2", q.Filter.Round)
			}
		}},
		{"since:7d", func(t *testing.T, q Query) {
			if want := parseNow.Add(-7 * 24 * time.Hour); !q.Filter.Since.Equal(want) {
				t.Errorf("Filter.Since = %v, want %v", q.Filter.Since, want)
			}
			eqStr(t, "Since", q.Since, "7d")
		}},
		{"until:2026-09-01", func(t *testing.T, q Query) {
			if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !q.Filter.Until.Equal(want) {
				t.Errorf("Filter.Until = %v, want %v", q.Filter.Until, want)
			}
			eqStr(t, "Until", q.Until, "2026-09-01")
		}},
		{"archived:true", func(t *testing.T, q Query) {
			if q.Filter.Archived == nil || !*q.Filter.Archived {
				t.Errorf("Filter.Archived = %v, want true", q.Filter.Archived)
			}
		}},
		{"archived:false", func(t *testing.T, q Query) {
			if q.Filter.Archived == nil || *q.Filter.Archived {
				t.Errorf("Filter.Archived = %v, want false", q.Filter.Archived)
			}
		}},
		{"by:builder", func(t *testing.T, q Query) {
			if q.By != AxisBuilder {
				t.Errorf("By = %q, want %q", q.By, AxisBuilder)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.token, func(t *testing.T) {
			q, err := ParseAt(c.token, parseNow)
			if err != nil {
				t.Fatalf("ParseAt(%q) error: %v", c.token, err)
			}
			c.check(t, q)
		})
	}
}

func TestParseNumericOps(t *testing.T) {
	q, err := ParseAt("cost>1 tokens>=1000000 commits<3 duration<=30 round=2 round>2", parseNow)
	if err != nil {
		t.Fatalf("ParseAt: %v", err)
	}
	want := []NumCond{
		{Key: "cost", Op: ">", Value: 1},
		{Key: "tokens", Op: ">=", Value: 1000000},
		{Key: "commits", Op: "<", Value: 3},
		{Key: "duration", Op: "<=", Value: 30},
		{Key: "round", Op: "=", Value: 2},
		{Key: "round", Op: ">", Value: 2},
	}
	if !reflect.DeepEqual(q.Nums, want) {
		t.Errorf("Nums = %+v, want %+v", q.Nums, want)
	}
}

func TestParseWordsAndQuotes(t *testing.T) {
	q, err := ParseAt(`auth "api v2" harness:agy`, parseNow)
	if err != nil {
		t.Fatalf("ParseAt: %v", err)
	}
	if want := []string{"auth", "api v2"}; !reflect.DeepEqual(q.Words, want) {
		t.Errorf("Words = %q, want %q", q.Words, want)
	}
	if q.Filter.Harness != "agy" {
		t.Errorf("Filter.Harness = %q, want agy", q.Filter.Harness)
	}
}

func TestParseSinceUntil(t *testing.T) {
	q, err := ParseAt("since:7d until:2026-09-01", parseNow)
	if err != nil {
		t.Fatalf("ParseAt: %v", err)
	}
	if want := parseNow.Add(-7 * 24 * time.Hour); !q.Filter.Since.Equal(want) {
		t.Errorf("Filter.Since = %v, want %v", q.Filter.Since, want)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !q.Filter.Until.Equal(want) {
		t.Errorf("Filter.Until = %v, want %v", q.Filter.Until, want)
	}
	if q.Since != "7d" || q.Until != "2026-09-01" {
		t.Errorf("raw Since/Until = %q/%q, want 7d/2026-09-01", q.Since, q.Until)
	}
}

func TestParseBy(t *testing.T) {
	for input, want := range map[string]Axis{
		"by:builder": AxisBuilder,
		"by:day":     AxisDay,
		"by:none":    AxisNone,
	} {
		q, err := ParseAt(input, parseNow)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", input, err)
		}
		if q.By != want {
			t.Errorf("ParseAt(%q).By = %q, want %q", input, q.By, want)
		}
	}
}

func TestParseLastDuplicateWins(t *testing.T) {
	q, err := ParseAt("harness:agy harness:codex outcome:open outcome:halted by:day by:builder since:7d since:24h", parseNow)
	if err != nil {
		t.Fatalf("ParseAt: %v", err)
	}
	if q.Filter.Harness != "codex" {
		t.Errorf("Filter.Harness = %q, want codex (last wins)", q.Filter.Harness)
	}
	if q.Filter.Outcome != "halted" {
		t.Errorf("Filter.Outcome = %q, want halted (last wins)", q.Filter.Outcome)
	}
	if q.By != AxisBuilder {
		t.Errorf("By = %q, want builder (last wins)", q.By)
	}
	if q.Since != "24h" {
		t.Errorf("Since = %q, want 24h (last wins)", q.Since)
	}
	if want := parseNow.Add(-24 * time.Hour); !q.Filter.Since.Equal(want) {
		t.Errorf("Filter.Since = %v, want %v", q.Filter.Since, want)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		input string
		token string
	}{
		{"foo:bar", "foo:bar"},
		{"outcome:nope", "outcome:nope"},
		{"cost>abc", "cost>abc"},
		{`harness:"agy`, "harness:agy"},
		{"since:7x", "since:7x"},
		{"by:nope", "by:nope"},
		{"archived:maybe", "archived:maybe"},
		{"harness:", "harness:"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			_, err := ParseAt(c.input, parseNow)
			var eq ErrQuery
			if !errors.As(err, &eq) {
				t.Fatalf("ParseAt(%q) err = %v, want ErrQuery", c.input, err)
			}
			if eq.Token != c.token {
				t.Errorf("ErrQuery.Token = %q, want %q", eq.Token, c.token)
			}
			if want := "query: " + c.token + " at "; !strings.HasPrefix(eq.Error(), want) {
				t.Errorf("ErrQuery.Error() = %q, want prefix %q", eq.Error(), want)
			}
		})
	}
}

func TestStringRoundTrip(t *testing.T) {
	canonical := []string{
		"harness:agy outcome:halted since:30d",
		`harness:agy outcome:halted since:30d cost>1 cost>2 auth "api v2" by:builder`,
		"report:done state:needs_you gate:pass basis:estimated server:contabo mode:headless round:3 archived:true",
		"binding:api repo:https://github.com/o/r feature:checkout planner:sess-1 provider:anthropic model:sonnet candidate:agy/x/y",
		`"api v2" by:day`,
		"tokens>=1000000 duration<=30 commits<3",
	}
	for _, text := range canonical {
		t.Run(text, func(t *testing.T) {
			q, err := ParseAt(text, parseNow)
			if err != nil {
				t.Fatalf("ParseAt(%q): %v", text, err)
			}
			if got := q.String(); got != text {
				t.Errorf("String() = %q, want the canonical input %q", got, text)
			}
			again, err := ParseAt(q.String(), parseNow)
			if err != nil {
				t.Fatalf("re-ParseAt(%q): %v", q.String(), err)
			}
			if !reflect.DeepEqual(again, q) {
				t.Errorf("ParseAt(String()) = %+v, want %+v", again, q)
			}
		})
	}

	// One exact string pins the canonical key order: the filter keys sort
	// into a fixed order regardless of how they were typed, the numeric
	// conditions keep their input order, then the words, then by.
	q, err := ParseAt("mode:remote outcome:halted harness:agy auth cost>1 by:builder since:30d", parseNow)
	if err != nil {
		t.Fatalf("ParseAt: %v", err)
	}
	want := "harness:agy outcome:halted mode:remote since:30d cost>1 auth by:builder"
	if got := q.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func eqStr(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}
