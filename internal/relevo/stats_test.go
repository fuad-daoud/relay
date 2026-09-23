package relevo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/history"
	"github.com/fuad-daoud/relevo/internal/ledger"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

var statsNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func statsPlan(binding string, seq, round int, ts time.Time) TabEntry {
	return TabEntry{Binding: binding, Entry: store.LogEntry{
		Seq: seq, TS: ts, Round: round, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true}}
}

func statsReport(binding string, seq, round int, ts time.Time) TabEntry {
	return TabEntry{Binding: binding, Entry: store.LogEntry{
		Seq: seq, TS: ts, Round: round, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true}}
}

// statsOneRound builds one segment: a plan, then a tail entry when there is
// one.
func statsOneRound(binding string, tail *store.LogEntry) []TabEntry {
	out := []TabEntry{statsPlan(binding, 1, 1, statsNow.Add(-time.Hour))}
	if tail != nil {
		e := *tail
		e.Seq = 2
		out = append(out, TabEntry{Binding: binding, Entry: e})
	}
	return out
}

func statsOutcomeCount(rep StatsReport, key string) int {
	for _, c := range rep.Outcomes {
		if c.Key == key {
			return c.Count
		}
	}
	return -1
}

func statsBuilderCount(rep StatsReport, key string) int {
	for _, c := range rep.Builders {
		if c.Key == key {
			return c.Count
		}
	}
	return -1
}

func statsGateCount(rep StatsReport, key string) int {
	for _, c := range rep.Gate {
		if c.Key == key {
			return c.Count
		}
	}
	return -1
}

// statsNote builds one relevo->log note entry of any kind.
func statsNote(binding string, seq, round int, ts time.Time, kind store.Kind, note string) TabEntry {
	return TabEntry{Binding: binding, Entry: store.LogEntry{
		Seq: seq, TS: ts, Round: round, Direction: store.DirToPlanner, Kind: kind, Confirmed: true, Note: note}}
}

func statsFrom(rep StatsReport, provider string) (StatsSwitchFrom, bool) {
	for _, f := range rep.Switches.From {
		if f.Provider == provider {
			return f, true
		}
	}
	return StatsSwitchFrom{}, false
}

func statsReasonCount(f StatsSwitchFrom, reason string) int {
	for _, c := range f.Reasons {
		if c.Key == reason {
			return c.Count
		}
	}
	return -1
}

func statsReportDone(binding string, seq, round int, ts time.Time) TabEntry {
	e := statsReport(binding, seq, round, ts)
	e.Entry.Outcome = "done"
	return e
}

// TestBuilderTokenFromNote covers one row per note shape relevo writes: a local
// pick, a local switch (whose note contains the pick ExplainResolution wrote),
// a relaunch (a switch-kind entry that is not a switch), a remote switch, and
// a pick for another role.
func TestBuilderTokenFromNote(t *testing.T) {
	cases := []struct {
		name string
		note string
		want string
		ok   bool
	}{
		{
			name: "local pick",
			note: "picked claude/test/m for builder: order #2; skipped agy/other/m (rate-limited until cleared)",
			want: "claude/test/m",
			ok:   true,
		},
		{
			name: "local switch",
			note: "switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2",
			want: "claude/test/m",
			ok:   true,
		},
		{
			name: "relaunch",
			note: "relaunched builder (lost to a daemon restart at 2026-09-18T12:00:00Z): picked opencode/test/m for builder: same candidate, not counted",
			want: "opencode/test/m",
			ok:   true,
		},
		{
			name: "reviewer pick is not a builder token",
			note: "picked claude/test/m for reviewer: sole candidate",
			want: "",
			ok:   false,
		},
		{
			name: "verify pick is not a builder token",
			note: "picked claude/test/m for verify: sole candidate",
			want: "",
			ok:   false,
		},
		{
			name: "remote switch",
			note: "switched on contabo: codex/openai/gpt-5.6-terra:high -> opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
			want: "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
			ok:   true,
		},
		{
			name: "remote pick by the server",
			note: "picked opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high on contabo: server's pick",
			want: "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
			ok:   true,
		},
		{
			name: "remote pick named explicitly",
			note: "picked claude/anthropic/sonnet on contabo: explicit",
			want: "claude/anthropic/sonnet",
			ok:   true,
		},
		{
			name: "remote pick with no how clause is not a token",
			note: "picked claude/anthropic/sonnet on",
			want: "",
			ok:   false,
		},
		{
			name: "unrelated note",
			note: "started after 2m0s queued",
			want: "",
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := builderTokenFromNote(tc.note)
			if got != tc.want || ok != tc.ok {
				t.Errorf("builderTokenFromNote(%q) = (%q, %v), want (%q, %v)", tc.note, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRemoteSwitchFrom(t *testing.T) {
	const note = "switched on contabo: codex/openai/gpt-5.6-terra:high -> opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"
	got, ok := remoteSwitchFrom(note)
	if !ok || got != "codex/openai/gpt-5.6-terra:high" {
		t.Errorf("remoteSwitchFrom(%q) = (%q, %v), want the previous token", note, got, ok)
	}

	for _, bad := range []string{
		"switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2",
		"switched on contabo without an arrow",
		"switched on contabo: codex/openai/m",
	} {
		if got, ok := remoteSwitchFrom(bad); ok || got != "" {
			t.Errorf("remoteSwitchFrom(%q) = (%q, %v), want not ok", bad, got, ok)
		}
	}
}

func TestSwitchReason(t *testing.T) {
	cases := []struct {
		note string
		want string
	}{
		{"switched builder (rate-limited: 5h window): picked claude/test/m for builder: order #2", "rate-limited"},
		{"switched builder (exited (code 1) without a report): picked claude/test/m for builder: order #2", "exited"},
		{"switched builder (gated while queued): picked claude/test/m for builder: order #2", "gated"},
		{"switched builder (lost to a daemon restart): picked claude/test/m for builder: order #2", "other"},
		{"relaunched builder (lost to a daemon restart at 2026-09-18T12:00:00Z): picked claude/test/m for builder: same candidate, not counted", "other"},
		{"picked claude/test/m for builder: order #1", "other"},
	}
	for _, tc := range cases {
		if got := switchReason(tc.note); got != tc.want {
			t.Errorf("switchReason(%q) = %q, want %q", tc.note, got, tc.want)
		}
	}
}

func TestRefKeyAndProviderOfToken(t *testing.T) {
	if got := refKey("opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"); got != "opencode/cline-pass/cline-pass/deepseek-v4.1-flash" {
		t.Errorf("refKey with an effort suffix = %q, want the suffix cut", got)
	}
	if got := refKey("codex/openai/gpt-5.6-terra:high"); got != "codex/openai/gpt-5.6-terra:high" {
		t.Errorf("refKey keeps a model's :effort suffix, got %q", got)
	}
	if got := refKey("bad"); got != "unknown" {
		t.Errorf("refKey(%q) = %q, want unknown", "bad", got)
	}
	if got := providerOfToken("opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"); got != "cline-pass" {
		t.Errorf("providerOfToken = %q, want cline-pass", got)
	}
	if got := providerOfToken("bad"); got != "unknown" {
		t.Errorf("providerOfToken(%q) = %q, want unknown", "bad", got)
	}
}

// TestBuildStatsOutcomes pins one row per outcome in StatsOutcomes, the fixed
// order with zeros included, and no unmarked reports.
func TestBuildStatsOutcomes(t *testing.T) {
	report := func(outcome, note string) *store.LogEntry {
		return &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
			Outcome: outcome, Note: note}
	}
	rows := []struct {
		binding string
		tail    *store.LogEntry
	}{
		{"done", report("done", "")},
		{"halted", report("halted", "")},
		{"blocked", report("blocked", "")},
		{"deferred", report("deferred", "")},
		{"unstructured", report("", "")},
		{"noreport", report("done", "noreport")},
		{"stopped", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindStop, Confirmed: true}},
		{"exited", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindExit, Confirmed: true}},
		{"open", nil},
	}
	var entries []TabEntry
	for _, r := range rows {
		entries = append(entries, statsOneRound(r.binding, r.tail)...)
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Rounds != 9 || rep.Bindings != 9 {
		t.Fatalf("Rounds, Bindings = %d, %d, want 9, 9", rep.Rounds, rep.Bindings)
	}
	for _, key := range StatsOutcomes {
		if got := statsOutcomeCount(rep, key); got != 1 {
			t.Errorf("outcome %q = %d, want 1", key, got)
		}
	}
	if len(rep.Outcomes) != len(StatsOutcomes) {
		t.Fatalf("Outcomes has %d keys, want every key in fixed order", len(rep.Outcomes))
	}
	for i, key := range StatsOutcomes {
		if rep.Outcomes[i].Key != key {
			t.Errorf("Outcomes[%d] = %q, want %q", i, rep.Outcomes[i].Key, key)
		}
	}
	if rep.Unmarked != 0 {
		t.Errorf("Unmarked = %d, want 0", rep.Unmarked)
	}
	// The one row whose report also says noreport still reads noreport, and
	// its Outcome field is not consulted.
	if got := statsOutcomeCount(rep, "done"); got != 1 {
		t.Errorf("the noreport row must not also count as done: done = %d", got)
	}
}

// TestBuildStatsUnmarked pins that a report noted unmarked increments Unmarked
// and still takes its tail outcome, and that noreport wins on the outcome.
func TestBuildStatsUnmarked(t *testing.T) {
	unmarked := &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
		Outcome: "done", Note: "---\nstatus: done\nnotes unmarked"}
	unmarkedNoReport := &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
		Outcome: "done", Note: "no report came\nunmarked noreport"}
	entries := append(statsOneRound("u1", unmarked), statsOneRound("u2", unmarkedNoReport)...)

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Unmarked != 2 {
		t.Errorf("Unmarked = %d, want 2", rep.Unmarked)
	}
	if got := statsOutcomeCount(rep, "done"); got != 1 {
		t.Errorf("done = %d, want 1 (the unmarked report keeps its tail outcome)", got)
	}
	if got := statsOutcomeCount(rep, "noreport"); got != 1 {
		t.Errorf("noreport = %d, want 1", got)
	}
}

// TestBuildStatsBuilderKey pins the three rules: the report's Usage key first,
// then the pick's token, then unknown.
func TestBuildStatsBuilderKey(t *testing.T) {
	withUsage := &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
		Outcome: "done", Usage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "deepseek-v4.1-flash"}}
	usageKey := statsOneRound("usage", withUsage)

	partial := &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
		Outcome: "done", Usage: &usage.Usage{Harness: "claude", Model: "sonnet"}}
	partialKey := statsOneRound("partial", partial)

	pick := []TabEntry{
		statsPlan("pickb", 1, 1, statsNow.Add(-time.Hour)),
		{Binding: "pickb", Entry: store.LogEntry{Seq: 2, TS: statsNow.Add(-time.Hour), Round: 1, Direction: store.DirToPlanner,
			Kind: store.KindPick, Confirmed: true, Note: "picked opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high for builder: order #1"}},
		statsReport("pickb", 3, 1, statsNow.Add(-time.Minute)),
	}

	none := statsOneRound("none", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"})

	entries := append(append(append(usageKey, partialKey...), pick...), none...)
	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)

	if got := statsBuilderCount(rep, "opencode/cline-pass/deepseek-v4.1-flash"); got != 1 {
		t.Errorf("usage key = %d, want 1", got)
	}
	if got := statsBuilderCount(rep, "claude/sonnet"); got != 1 {
		t.Errorf("empty parts must be dropped: claude/sonnet = %d, want 1", got)
	}
	if got := statsBuilderCount(rep, "opencode/cline-pass/cline-pass/deepseek-v4.1-flash"); got != 1 {
		t.Errorf("pick key (effort hash cut) = %d, want 1", got)
	}
	if got := statsBuilderCount(rep, "unknown"); got != 1 {
		t.Errorf("unknown = %d, want 1", got)
	}
}

// TestBuildStatsRemotePickBuilderKey pins that a remote pick names the builder:
// a segment whose only builder evidence is a remote pick, with a report that
// carries no usage, attributes its round to the picked token's key, not
// "unknown".
func TestBuildStatsRemotePickBuilderKey(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("r", 1, 1, ts),
		statsNote("r", 2, 1, ts, store.KindPick, "picked opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high on contabo: server's pick"),
		statsReportDone("r", 3, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if got := statsBuilderCount(rep, "opencode/cline-pass/cline-pass/deepseek-v4.1-flash"); got != 1 {
		t.Errorf("remote pick builder = %d, want 1", got)
	}
	if got := statsBuilderCount(rep, "unknown"); got != -1 {
		t.Errorf("unknown = %d, want no unknown row for a remote pick", got)
	}
}

// TestStatsUsageKey pins the normalisation: the model's "#" effort suffix is
// cut as refKey cuts it, a ":effort" suffix is kept, and empty parts are
// dropped.
func TestStatsUsageKey(t *testing.T) {
	cases := []struct {
		name string
		u    *usage.Usage
		want string
	}{
		{
			name: "hash effort is cut",
			u:    &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"},
			want: "opencode/cline-pass/cline-pass/deepseek-v4.1-flash",
		},
		{
			name: "colon effort is kept",
			u:    &usage.Usage{Harness: "codex", Provider: "openai", Model: "gpt-5.6-terra:high"},
			want: "codex/openai/gpt-5.6-terra:high",
		},
		{
			name: "empty provider is dropped",
			u:    &usage.Usage{Harness: "claude", Model: "sonnet"},
			want: "claude/sonnet",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statsUsageKey(tc.u); got != tc.want {
				t.Errorf("statsUsageKey(%+v) = %q, want %q", tc.u, got, tc.want)
			}
		})
	}
}

// TestBuildStatsOneKeyPerModel pins that a round with usage and a round without
// it, both after the same pick, land on one builder key: the usage path cuts
// the model's "#" effort suffix as the pick path does.
func TestBuildStatsOneKeyPerModel(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	withUsage := store.LogEntry{Seq: 3, TS: ts, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport,
		Confirmed: true, Outcome: "done",
		Usage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"}}
	noUsage := store.LogEntry{Seq: 5, TS: ts, Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport,
		Confirmed: true, Outcome: "done"}
	entries := []TabEntry{
		statsPlan("one", 1, 1, ts),
		statsNote("one", 2, 1, ts, store.KindPick, "picked opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high for builder: order #1"),
		{Binding: "one", Entry: withUsage},
		statsPlan("one", 4, 2, ts),
		{Binding: "one", Entry: noUsage},
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2", rep.Rounds)
	}
	if len(rep.Builders) != 1 {
		t.Fatalf("Builders = %+v, want exactly one key", rep.Builders)
	}
	if rep.Builders[0].Key != "opencode/cline-pass/cline-pass/deepseek-v4.1-flash" || rep.Builders[0].Count != 2 {
		t.Errorf("Builders = %+v, want the cut key with count 2", rep.Builders)
	}
}

// TestBuildStatsConsultModelsEffort pins that a findings entry's model is cut
// at its "#" effort suffix on the ConsultModels key too.
func TestBuildStatsConsultModelsEffort(t *testing.T) {
	inWindow := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		{Entry: store.LogEntry{TS: inWindow, Direction: store.DirToPlanner, Kind: store.KindFindings,
			Usage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"}}},
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if got := statsConsultModelCount(rep, "opencode/cline-pass/cline-pass/deepseek-v4.1-flash"); got != 1 {
		t.Errorf("consult model = %d, want the cut key", got)
	}
	if len(rep.ConsultModels) != 1 {
		t.Errorf("ConsultModels = %+v, want one cut key", rep.ConsultModels)
	}
}

// TestBuildStatsPlanRules pins what is and is not a counted round: a plan bound
// for the builder is required, and a round that started before since is not
// counted.
func TestBuildStatsPlanRules(t *testing.T) {
	old := statsNow.Add(-72 * time.Hour)
	newer := statsNow.Add(-time.Hour)
	report := &store.LogEntry{TS: newer, Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"}

	// A report with no plan is not a round.
	noPlan := []TabEntry{{Binding: "noplan", Entry: *report}}

	// A round whose plan predates the cut is not counted; one after it is.
	oldRound := []TabEntry{
		statsPlan("old", 1, 1, old),
		{Binding: "old", Entry: store.LogEntry{Seq: 2, TS: old.Add(time.Minute), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"}},
	}
	newRound := []TabEntry{
		statsPlan("new", 1, 1, newer),
		{Binding: "new", Entry: store.LogEntry{Seq: 2, TS: newer.Add(time.Minute), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"}},
	}

	entries := append(append(noPlan, oldRound...), newRound...)
	rep := BuildStats(entries, history.History{}, newer.Add(-time.Minute), statsNow)
	if rep.Rounds != 1 || rep.Bindings != 1 {
		t.Fatalf("Rounds, Bindings = %d, %d, want 1, 1 (only the round after since)", rep.Rounds, rep.Bindings)
	}
	if rep.Since == nil || !rep.Since.Equal(newer.Add(-time.Minute)) {
		t.Errorf("Since = %v, want the cut", rep.Since)
	}
}

// TestBuildStatsSegments pins the segment rule: two logs that share a binding
// name but restart their Seq each have their own round 1.
func TestBuildStatsSegments(t *testing.T) {
	one := statsOneRound("x", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"})
	two := statsOneRound("x", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"})
	entries := append(one, two...)

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Rounds != 2 || rep.Bindings != 2 {
		t.Errorf("Rounds, Bindings = %d, %d, want 2, 2", rep.Rounds, rep.Bindings)
	}
	if got := statsOutcomeCount(rep, "done"); got != 2 {
		t.Errorf("done = %d, want 2", got)
	}
}

// TestBuildStatsGate pins the per-result counts, the fixed order with zeros
// included, and that a result outside StatsGateResults is ignored.
func TestBuildStatsGate(t *testing.T) {
	gate := func(result string) *store.LogEntry {
		return &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
			Outcome: "done", Gate: &store.GateRecord{Result: result}}
	}
	var entries []TabEntry
	for _, r := range []string{"pass", "fail", "timeout", "error", "weird"} {
		entries = append(entries, statsOneRound(r, gate(r))...)
	}
	entries = append(entries, statsOneRound("nogate", &store.LogEntry{Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true, Outcome: "done"})...)

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	for _, key := range StatsGateResults {
		if got := statsGateCount(rep, key); got != 1 {
			t.Errorf("gate %q = %d, want 1", key, got)
		}
	}
	if len(rep.Gate) != len(StatsGateResults) {
		t.Fatalf("Gate has %d keys, want every key in fixed order", len(rep.Gate))
	}
	for i, key := range StatsGateResults {
		if rep.Gate[i].Key != key {
			t.Errorf("Gate[%d] = %q, want %q", i, rep.Gate[i].Key, key)
		}
	}
	// The out-of-list round still counts as a round, but its result is not a
	// gate key.
	if rep.Rounds != 6 {
		t.Errorf("Rounds = %d, want 6", rep.Rounds)
	}
}

// TestBuildStatsSwitchFromPick pins the switch from a picked provider: the pick
// sets cur, and a later rate-limited switch attributes to it.
func TestBuildStatsSwitchFromPick(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("a", 1, 1, ts),
		statsNote("a", 2, 1, ts, store.KindPick, "picked codex/openai/gpt-5.6-terra:high for builder: order #1"),
		statsNote("a", 3, 1, ts, store.KindSwitch, "switched builder (rate-limited: 5h window): picked claude/anthropic/sonnet for builder: order #2"),
		statsReportDone("a", 4, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Switches.Total != 1 || rep.Switches.Rounds != 1 {
		t.Fatalf("Switches = %+v, want total 1 in 1 round", rep.Switches)
	}
	from, ok := statsFrom(rep, "openai")
	if !ok {
		t.Fatalf("From = %+v, want an openai row", rep.Switches.From)
	}
	if from.Count != 1 || statsReasonCount(from, "rate-limited") != 1 {
		t.Errorf("openai from = %+v, want 1 rate-limited", from)
	}
	if got := statsReasonCount(from, "exited"); got != 0 {
		t.Errorf("openai exited reasons = %d, want 0 (fixed order keeps the zero)", got)
	}
	if len(from.Reasons) != len(SwitchReasons) {
		t.Errorf("reasons = %+v, want every SwitchReasons key", from.Reasons)
	}
}

// TestBuildStatsRelaunchNotCounted pins that a relaunch note is not a switch:
// it updates cur and nothing else.
func TestBuildStatsRelaunchNotCounted(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("b", 1, 1, ts),
		statsNote("b", 2, 1, ts, store.KindPick, "picked claude/anthropic/sonnet for builder: order #1"),
		statsNote("b", 3, 1, ts, store.KindSwitch, "relaunched builder (lost to a daemon restart at 2026-09-23T10:00:00Z): picked claude/anthropic/sonnet for builder: same candidate, not counted"),
		statsReportDone("b", 4, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Switches.Total != 0 || rep.Switches.Rounds != 0 {
		t.Errorf("Switches = %+v, want a relaunch not counted", rep.Switches)
	}
	if len(rep.Switches.From) != 0 {
		t.Errorf("From = %+v, want none", rep.Switches.From)
	}
	// The relaunch still updates cur: the round's builder comes from it.
	if got := statsBuilderCount(rep, "claude/anthropic/sonnet"); got != 1 {
		t.Errorf("builder = %d, want the relaunched candidate", got)
	}
}

// TestBuildStatsRemoteSwitch pins that a remote switch attributes to the
// previous token's provider with reason remote.
func TestBuildStatsRemoteSwitch(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("c", 1, 1, ts),
		statsNote("c", 2, 1, ts, store.KindSwitch, "switched on contabo: codex/openai/gpt-5.6-terra:high -> opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"),
		statsReportDone("c", 3, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Switches.Total != 1 {
		t.Fatalf("Switches = %+v, want 1", rep.Switches)
	}
	from, ok := statsFrom(rep, "openai")
	if !ok {
		t.Fatalf("From = %+v, want the previous token's provider", rep.Switches.From)
	}
	if statsReasonCount(from, "remote") != 1 {
		t.Errorf("openai from = %+v, want 1 remote", from)
	}
}

// TestBuildStatsTwoSwitchesOneRound pins that two switch entries in one round
// are two switches but one round.
func TestBuildStatsTwoSwitchesOneRound(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("d", 1, 1, ts),
		statsNote("d", 2, 1, ts, store.KindPick, "picked claude/anthropic/sonnet for builder: order #1"),
		statsNote("d", 3, 1, ts, store.KindSwitch, "switched builder (exited (code 1) without a report): picked codex/openai/gpt-5.6-terra:high for builder: order #2"),
		statsNote("d", 4, 1, ts, store.KindSwitch, "switched builder (gated while queued): picked claude/anthropic/sonnet for builder: order #1"),
		statsReportDone("d", 5, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	if rep.Switches.Total != 2 || rep.Switches.Rounds != 1 {
		t.Fatalf("Switches = %+v, want 2 in 1 round", rep.Switches)
	}
	if rep.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", rep.Rounds)
	}
	anthropic, ok := statsFrom(rep, "anthropic")
	if !ok || statsReasonCount(anthropic, "exited") != 1 {
		t.Errorf("anthropic from = %+v, want 1 exited", anthropic)
	}
	openai, ok := statsFrom(rep, "openai")
	if !ok || statsReasonCount(openai, "gated") != 1 {
		t.Errorf("openai from = %+v, want 1 gated", openai)
	}
}

// TestBuildStatsSwitchBeforePick pins that a switch with no pick before it
// attributes to "unknown".
func TestBuildStatsSwitchBeforePick(t *testing.T) {
	ts := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsPlan("e", 1, 1, ts),
		statsNote("e", 2, 1, ts, store.KindSwitch, "switched builder (rate-limited: 5h window): picked claude/anthropic/sonnet for builder: order #1"),
		statsReportDone("e", 3, 1, ts),
	}

	rep := BuildStats(entries, history.History{}, time.Time{}, statsNow)
	from, ok := statsFrom(rep, "unknown")
	if !ok || from.Count != 1 || statsReasonCount(from, "rate-limited") != 1 {
		t.Errorf("From = %+v, want one unknown rate-limited switch", rep.Switches.From)
	}
}

// statsAsk builds one ask entry with the given note.
func statsAsk(ts time.Time, note string) TabEntry {
	return TabEntry{Entry: store.LogEntry{TS: ts, Round: 1, Direction: store.DirToConsult, Kind: store.KindAsk, Confirmed: true, Note: note}}
}

func statsConsultCount(rep StatsReport, role string) int {
	for _, c := range rep.Consults {
		if c.Key == role {
			return c.Count
		}
	}
	return -1
}

func statsConsultModelCount(rep StatsReport, key string) int {
	for _, c := range rep.ConsultModels {
		if c.Key == key {
			return c.Count
		}
	}
	return -1
}

// TestBuildStatsConsults pins the role an ask note names, the verify-skipped
// special case, the round-session form, and the findings usage keys.
func TestBuildStatsConsults(t *testing.T) {
	inWindow := statsNow.Add(-time.Hour)
	entries := []TabEntry{
		statsAsk(inWindow, "reviewer 7f2a3c1d"),
		statsAsk(inWindow, "verify abcd1234"),
		statsAsk(inWindow, "verify skipped: no diff"),
		statsAsk(inWindow, "round 2 session claude:abc"),
		statsAsk(statsNow.Add(-48*time.Hour), "reviewer deadbeef"),
		{Entry: store.LogEntry{TS: inWindow, Direction: store.DirToPlanner, Kind: store.KindFindings,
			Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "sonnet"}}},
		{Entry: store.LogEntry{TS: inWindow, Direction: store.DirToPlanner, Kind: store.KindFindings}},
	}

	rep := BuildStats(entries, history.History{}, statsNow.Add(-24*time.Hour), statsNow)
	if got := statsConsultCount(rep, "reviewer"); got != 1 {
		t.Errorf("reviewer = %d, want 1 (the 48h-old ask is outside the window)", got)
	}
	if got := statsConsultCount(rep, "verify"); got != 1 {
		t.Errorf("verify = %d, want 1", got)
	}
	if got := statsConsultCount(rep, "session"); got != 1 {
		t.Errorf("session = %d, want 1", got)
	}
	if rep.VerifySkipped != 1 {
		t.Errorf("VerifySkipped = %d, want 1", rep.VerifySkipped)
	}
	if got := statsConsultCount(rep, "verify skipped:"); got != -1 {
		t.Errorf("the skipped note must not be a role, got %d", got)
	}
	if got := statsConsultModelCount(rep, "claude/anthropic/sonnet"); got != 1 {
		t.Errorf("consult model = %d, want 1", got)
	}
	if len(rep.ConsultModels) != 1 {
		t.Errorf("ConsultModels = %+v, want only the entry with usage", rep.ConsultModels)
	}
}

// TestBlockedStats pins the window, the clip to [since, now], the zero-Since
// skip and the omission of a provider whose every count is zero.
func TestBlockedStats(t *testing.T) {
	since := statsNow.Add(-24 * time.Hour)
	h := history.History{Events: []history.Event{
		{At: since.Add(-time.Hour), Kind: ledger.RateLimited, Provider: "openai"},
		{At: since.Add(time.Hour), Kind: ledger.RateLimited, Provider: "openai"},
		{At: since.Add(time.Hour), Kind: ledger.SpawnFailed, Provider: "openai"},
		{At: since.Add(2 * time.Hour), Kind: history.Cleared, Provider: "openai", Since: since.Add(-time.Hour)},
		{At: since.Add(3 * time.Hour), Kind: history.Cleared, Provider: "openai"},
		{At: since.Add(-2 * time.Hour), Kind: ledger.RateLimited, Provider: "google"},
		{At: since.Add(time.Hour), Kind: ledger.RateLimited, Provider: ""},
	}}

	rep := BuildStats(nil, h, since, statsNow)
	if len(rep.Blocked) != 1 || rep.Blocked[0].Provider != "openai" {
		t.Fatalf("Blocked = %+v, want only openai", rep.Blocked)
	}
	b := rep.Blocked[0]
	if b.RateLimited != 1 || b.SpawnFailed != 1 {
		t.Errorf("openai counts = %+v, want 1 rate-limited and 1 spawn-failed in the window", b)
	}
	// The clear's span is [since, At] = 2h, because its Since is before the cut;
	// the second clear has no Since and is skipped.
	if b.Cleared != 1 || b.ClearedMS != int64((2*time.Hour)/time.Millisecond) {
		t.Errorf("openai clear = %d, %dms, want 1 clear of 2h clipped at since", b.Cleared, b.ClearedMS)
	}
}

// TestBlockedStatsClipsToNow pins that a clear's span never runs past now, and
// that a zero since means no lower clip.
func TestBlockedStatsClipsToNow(t *testing.T) {
	h := history.History{Events: []history.Event{
		{At: statsNow.Add(2 * time.Hour), Kind: history.Cleared, Provider: "zen", Since: statsNow.Add(-3 * time.Hour)},
	}}

	rep := BuildStats(nil, h, time.Time{}, statsNow)
	if len(rep.Blocked) != 1 {
		t.Fatalf("Blocked = %+v, want zen", rep.Blocked)
	}
	if got := rep.Blocked[0].ClearedMS; got != int64((3*time.Hour)/time.Millisecond) {
		t.Errorf("ClearedMS = %d, want 3h clipped at now", got)
	}
}

// statsGoldenReport is the fixture of §4's layout: one round count that covers
// every section, with zeros in the fixed orders to prove they are omitted.
func statsGoldenReport() StatsReport {
	since := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	return StatsReport{
		Since:    &since,
		Until:    statsNow,
		Bindings: 9,
		Rounds:   42,
		Builders: []StatsCount{
			{Key: "opencode/cline-pass/cline-pass/deepseek-v4.1-flash", Count: 18},
			{Key: "claude/anthropic/sonnet", Count: 12},
		},
		Outcomes: []StatsCount{
			{Key: "done", Count: 30},
			{Key: "halted", Count: 4},
			{Key: "blocked", Count: 0},
			{Key: "deferred", Count: 0},
			{Key: "unstructured", Count: 3},
			{Key: "noreport", Count: 0},
			{Key: "stopped", Count: 0},
			{Key: "exited", Count: 2},
			{Key: "open", Count: 1},
		},
		Unmarked: 2,
		Switches: StatsSwitches{
			Total:  7,
			Rounds: 5,
			From: []StatsSwitchFrom{{
				Provider: "openai",
				Count:    4,
				Reasons: []StatsCount{
					{Key: "rate-limited", Count: 3},
					{Key: "exited", Count: 1},
					{Key: "gated", Count: 0},
					{Key: "remote", Count: 0},
					{Key: "other", Count: 0},
				},
			}},
		},
		Gate: []StatsCount{
			{Key: "pass", Count: 25},
			{Key: "fail", Count: 5},
			{Key: "timeout", Count: 0},
			{Key: "error", Count: 1},
		},
		Consults:      []StatsCount{{Key: "reviewer", Count: 3}, {Key: "verify", Count: 2}},
		ConsultModels: []StatsCount{{Key: "claude/anthropic/sonnet", Count: 5}},
		VerifySkipped: 1,
		Blocked: []StatsBlocked{{
			Provider:    "openai",
			RateLimited: 3,
			SpawnFailed: 1,
			Cleared:     1,
			ClearedMS:   int64((5*time.Hour + 12*time.Minute) / time.Millisecond),
		}},
	}
}

func TestRenderStatsGolden(t *testing.T) {
	want := `relevo stats: since 2026-09-16 00:00 UTC
rounds     42 in 9 bindings
builders   opencode/cline-pass/cline-pass/deepseek-v4.1-flash  18
           claude/anthropic/sonnet  12
outcomes   done 30, halted 4, unstructured 3, exited 2, open 1; 2 unmarked
switches   7 in 5 of 42 rounds (12%)
           openai  4 (rate-limited 3, exited 1)
gate       pass 25, fail 5, error 1 of 31 gated rounds (81% pass)
consults   reviewer 3, verify 2; 1 verify skipped
           claude/anthropic/sonnet  5
blocked    openai  rate-limited 3, spawn failed 1, 1 cleared by hand (5h12m)
           (availability history keeps 30 days; an expired gate has no recorded length)
`
	if got := RenderStats(statsGoldenReport()); got != want {
		t.Errorf("RenderStats =\n%s\nwant\n%s", got, want)
	}
}

// statsBaseReport has one of everything non-empty and each empty form for the
// sections the base golden exercises.
func statsBaseReport() StatsReport {
	return StatsReport{
		Rounds:        1,
		Bindings:      1,
		Builders:      []StatsCount{{Key: "z/test/m", Count: 1}},
		Outcomes:      []StatsCount{{Key: "done", Count: 1}},
		Switches:      StatsSwitches{From: []StatsSwitchFrom{}},
		Consults:      []StatsCount{},
		ConsultModels: []StatsCount{},
		Blocked:       []StatsBlocked{},
	}
}

// TestRenderStatsEmptySections pins the empty form of every section: the four
// literal ones, and builders/outcomes, which print nothing.
func TestRenderStatsEmptySections(t *testing.T) {
	want := `relevo stats: all recorded rounds
rounds     1 in 1 binding
builders   z/test/m  1
outcomes   done 1
switches   none
gate       no gated rounds
consults   none
blocked    none
           (availability history keeps 30 days; an expired gate has no recorded length)
`
	if got := RenderStats(statsBaseReport()); got != want {
		t.Errorf("base =\n%s\nwant\n%s", got, want)
	}

	noBuilders := statsBaseReport()
	noBuilders.Builders = []StatsCount{}
	if got := RenderStats(noBuilders); strings.Contains(got, "builders") {
		t.Errorf("an empty builders section must print nothing, got:\n%s", got)
	}

	noOutcomes := statsBaseReport()
	noOutcomes.Outcomes = []StatsCount{}
	if got := RenderStats(noOutcomes); strings.Contains(got, "outcomes") {
		t.Errorf("an empty outcomes section must print nothing, got:\n%s", got)
	}
}

// TestRenderStatsNoRounds pins that a report with no rounds, no consults and no
// blocks is one line.
func TestRenderStatsNoRounds(t *testing.T) {
	rep := BuildStats(nil, history.History{}, time.Time{}, statsNow)
	if got := RenderStats(rep); got != "no rounds\n" {
		t.Errorf("RenderStats(empty) = %q, want %q", got, "no rounds\n")
	}
}

// TestBuildStatsJSONShape pins the --json skeleton: every slice is [] and never
// null, and since is the only null.
func TestBuildStatsJSONShape(t *testing.T) {
	rep := BuildStats(nil, history.History{}, time.Time{}, statsNow)
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"builders":[]`) {
		t.Errorf("empty builders must be [], got %s", s)
	}
	if !strings.Contains(s, `"since":null`) {
		t.Errorf("an all-recorded report must carry since:null, got %s", s)
	}
	if n := strings.Count(s, "null"); n != 1 {
		t.Errorf("null appears %d times, want 1 (since): %s", n, s)
	}
}
