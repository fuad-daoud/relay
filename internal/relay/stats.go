package relay

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// StatsCount is one key and how many times it was seen.
type StatsCount struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// StatsSwitchFrom is the switches that left one provider, by reason. Provider
// is "unknown" when the previous token could not be attributed.
type StatsSwitchFrom struct {
	Provider string       `json:"provider"`
	Count    int          `json:"count"`
	Reasons  []StatsCount `json:"reasons"` // fixed SwitchReasons order, zeros included
}

// StatsSwitches counts the rounds that switched builder and where they came
// from.
type StatsSwitches struct {
	Total  int               `json:"total"`  // counted switch entries
	Rounds int               `json:"rounds"` // counted rounds with >= 1 switch
	From   []StatsSwitchFrom `json:"from"`   // sorted by Count desc, then Provider
}

// StatsBlocked is one provider's blocks over the 30-day availability window.
type StatsBlocked struct {
	Provider    string `json:"provider"`
	RateLimited int    `json:"rate_limited"` // ledger.RateLimited events in window
	SpawnFailed int    `json:"spawn_failed"` // ledger.SpawnFailed events in window
	Cleared     int    `json:"cleared"`      // history.Cleared events with a usable Since
	ClearedMS   int64  `json:"cleared_ms"`   // sum of those spans, clipped to the window
}

// StatsReport is what `relay stats` and `relay stats --json` report.
type StatsReport struct {
	Since         *time.Time     `json:"since"` // nil = all recorded
	Until         time.Time      `json:"until"` // the now passed in
	Bindings      int            `json:"bindings"`
	Rounds        int            `json:"rounds"`
	Builders      []StatsCount   `json:"builders"`
	Outcomes      []StatsCount   `json:"outcomes"`
	Unmarked      int            `json:"unmarked"`
	Switches      StatsSwitches  `json:"switches"`
	Gate          []StatsCount   `json:"gate"`
	Consults      []StatsCount   `json:"consults"`
	ConsultModels []StatsCount   `json:"consult_models"`
	VerifySkipped int            `json:"verify_skipped"`
	Blocked       []StatsBlocked `json:"blocked"`
}

// Fixed lists, in the order the report prints them.
var (
	// StatsOutcomes is every way a counted round can end.
	StatsOutcomes = []string{"done", "halted", "blocked", "deferred", "unstructured", "noreport", "stopped", "exited", "open"}
	// StatsGateResults is every gate result relay records.
	StatsGateResults = []string{"pass", "fail", "timeout", "error"}
	// SwitchReasons is every reason a counted switch is attributed to.
	SwitchReasons = []string{"rate-limited", "exited", "gated", "remote", "other"}
)

// BuildStats walks the tab entries -- live logs and archives, in the order
// TabEntries returned them -- and reports the rounds that started since (zero
// = all), their builders, outcomes and gates, plus the switches, consults and
// blocks that go with them. Pure: it reads no disk, no clock and no
// environment; now is the report's Until.
func BuildStats(entries []TabEntry, hist history.History, since, now time.Time) StatsReport {
	rep := StatsReport{Until: now}
	if !since.IsZero() {
		s := since
		rep.Since = &s
	}

	builderCount := map[string]int{}
	outcomeCount := map[string]int{}
	gateCount := map[string]int{}
	fromCount := map[string]int{}
	fromReasons := map[string]map[string]int{}
	switchTotal := 0
	switchRounds := 0

	for _, seg := range statsSegments(entries) {
		// A round is (segment, Round) with a plan bound for the builder; its
		// start is the first such entry's TS.
		roundStart := map[int]time.Time{}
		for _, e := range seg {
			if e.Entry.Kind == store.KindPlan && e.Entry.Direction == store.DirToBuilder && e.Entry.Round >= 1 {
				if _, ok := roundStart[e.Entry.Round]; !ok {
					roundStart[e.Entry.Round] = e.Entry.TS
				}
			}
		}
		countedRound := func(r int) bool {
			start, ok := roundStart[r]
			return ok && (since.IsZero() || !start.Before(since))
		}

		type roundAcc struct {
			last      *store.LogEntry
			stop      bool
			exit      bool
			sw        bool
			curAtLast string
		}
		rounds := map[int]*roundAcc{}
		for r := range roundStart {
			rounds[r] = &roundAcc{}
		}

		cur := ""
		for _, e := range seg {
			acc := rounds[e.Entry.Round]
			if e.Entry.Kind == store.KindPick || e.Entry.Kind == store.KindSwitch {
				if e.Entry.Kind == store.KindSwitch && acc != nil && countedRound(e.Entry.Round) {
					note := e.Entry.Note
					// A relaunch is a switch-kind entry that replaces a lost
					// process with the same candidate: it updates cur and is
					// otherwise not a switch.
					if !strings.HasPrefix(note, "relaunched ") {
						var from, reason string
						if strings.HasPrefix(note, "switched on ") {
							from, _ = remoteSwitchFrom(note)
							reason = "remote"
						} else {
							from = cur
							reason = switchReason(note)
						}
						provider := providerOfToken(from)
						switchTotal++
						acc.sw = true
						fromCount[provider]++
						if fromReasons[provider] == nil {
							fromReasons[provider] = map[string]int{}
						}
						fromReasons[provider][reason]++
					}
				}
				if tok, ok := builderTokenFromNote(e.Entry.Note); ok {
					cur = tok
				}
			}
			if acc == nil {
				continue
			}
			switch e.Entry.Kind {
			case store.KindReport:
				ent := e.Entry
				acc.last = &ent
			case store.KindStop:
				acc.stop = true
			case store.KindExit:
				acc.exit = true
			}
			acc.curAtLast = cur
		}

		counted := 0
		for r, acc := range rounds {
			start := roundStart[r]
			if !since.IsZero() && start.Before(since) {
				continue
			}
			counted++

			key := "unknown"
			if acc.last != nil && acc.last.Usage != nil && acc.last.Usage.Harness != "" {
				key = statsUsageKey(acc.last.Usage)
			} else if acc.curAtLast != "" {
				key = refKey(acc.curAtLast)
			}
			builderCount[key]++

			outcomeCount[statsOutcome(acc.last, acc.stop, acc.exit)]++
			if acc.last != nil && strings.Contains(acc.last.Note, "unmarked") {
				rep.Unmarked++
			}
			if acc.last != nil && acc.last.Gate != nil && statsInList(StatsGateResults, acc.last.Gate.Result) {
				gateCount[acc.last.Gate.Result]++
			}
			if acc.sw {
				switchRounds++
			}
		}
		rep.Rounds += counted
		if counted > 0 {
			rep.Bindings++
		}
	}

	// Consults are not tied to counted rounds: every ask in the window counts,
	// whatever round it was filed under.
	consultCount := map[string]int{}
	consultModelCount := map[string]int{}
	for _, e := range entries {
		en := e.Entry
		if en.Kind == store.KindAsk && en.Direction == store.DirToConsult {
			if en.TS.Before(since) {
				continue
			}
			note := en.Note
			if strings.HasPrefix(note, "verify skipped:") {
				rep.VerifySkipped++
				continue
			}
			role := note
			if strings.HasPrefix(note, "round ") {
				role = "session"
			} else if i := strings.IndexByte(note, ' '); i >= 0 {
				role = note[:i]
			}
			if role == "" {
				role = "unknown"
			}
			consultCount[role]++
		}
		if en.Kind == store.KindFindings && en.Usage != nil && en.Usage.Harness != "" && !en.TS.Before(since) {
			consultModelCount[statsUsageKey(en.Usage)]++
		}
	}

	rep.Builders = statsSorted(builderCount)
	rep.Outcomes = statsFixed(StatsOutcomes, outcomeCount)
	rep.Gate = statsFixed(StatsGateResults, gateCount)
	rep.Switches = statsSwitches(switchTotal, switchRounds, fromCount, fromReasons)
	rep.Consults = statsSorted(consultCount)
	rep.ConsultModels = statsSorted(consultModelCount)
	rep.Blocked = blockedStats(hist, since, now)
	return rep
}

// blockedStats sums the availability history's blocks per provider: rate-limit
// and spawn-failure events inside the window, and the spans of the clears a
// human did by hand, clipped to [since, now]. A provider whose every count is
// zero is omitted.
func blockedStats(h history.History, since, now time.Time) []StatsBlocked {
	byProvider := map[string]*StatsBlocked{}
	for _, e := range h.Events {
		if e.Provider == "" {
			continue
		}
		b := byProvider[e.Provider]
		if b == nil {
			b = &StatsBlocked{Provider: e.Provider}
			byProvider[e.Provider] = b
		}
		switch e.Kind {
		case ledger.RateLimited:
			if !e.At.Before(since) {
				b.RateLimited++
			}
		case ledger.SpawnFailed:
			if !e.At.Before(since) {
				b.SpawnFailed++
			}
		case history.Cleared:
			// Only a clear with a usable span has a duration; an expired gate
			// keeps no expiry time to measure.
			if e.Since.IsZero() || e.Since.After(e.At) {
				continue
			}
			start, end := e.Since, e.At
			if !since.IsZero() && start.Before(since) {
				start = since
			}
			if end.After(now) {
				end = now
			}
			if d := end.Sub(start); d > 0 {
				b.Cleared++
				b.ClearedMS += int64(d / time.Millisecond)
			}
		}
	}

	out := make([]StatsBlocked, 0, len(byProvider))
	for _, b := range byProvider {
		if b.RateLimited == 0 && b.SpawnFailed == 0 && b.Cleared == 0 {
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// statsSwitches builds the switch tallies: one From per provider that had a
// counted switch, its reasons in SwitchReasons order with zeroes included, and
// the list sorted by Count desc, then Provider.
func statsSwitches(total, rounds int, fromCount map[string]int, fromReasons map[string]map[string]int) StatsSwitches {
	from := make([]StatsSwitchFrom, 0, len(fromCount))
	for provider, count := range fromCount {
		reasons := make([]StatsCount, 0, len(SwitchReasons))
		for _, r := range SwitchReasons {
			reasons = append(reasons, StatsCount{Key: r, Count: fromReasons[provider][r]})
		}
		from = append(from, StatsSwitchFrom{Provider: provider, Count: count, Reasons: reasons})
	}
	sort.Slice(from, func(i, j int) bool {
		if from[i].Count != from[j].Count {
			return from[i].Count > from[j].Count
		}
		return from[i].Provider < from[j].Provider
	})
	return StatsSwitches{Total: total, Rounds: rounds, From: from}
}

// statsSegments splits the entries into binding logs: a new segment starts at
// the first entry and at every entry whose Binding changed or whose Seq did not
// increase, which is what separates two archives, or an archive and a live log,
// that share a binding name.
func statsSegments(entries []TabEntry) [][]TabEntry {
	var segs [][]TabEntry
	for i, e := range entries {
		if i == 0 || e.Binding != entries[i-1].Binding || e.Entry.Seq <= entries[i-1].Entry.Seq {
			segs = append(segs, []TabEntry{})
		}
		segs[len(segs)-1] = append(segs[len(segs)-1], e)
	}
	return segs
}

// statsOutcome classifies one counted round from its last report entry, or
// from the stop/exit entries when no report arrived.
func statsOutcome(R *store.LogEntry, stop, exit bool) string {
	if R != nil {
		if strings.Contains(R.Note, "noreport") {
			return "noreport"
		}
		switch R.Outcome {
		case "done", "halted", "blocked", "deferred":
			return R.Outcome
		default:
			return "unstructured"
		}
	}
	if stop {
		return "stopped"
	}
	if exit {
		return "exited"
	}
	return "open"
}

// statsUsageKey is a usage record's harness/provider/model key with empty parts
// dropped and the model cut at its first "#", the effort suffix, exactly as
// refKey cuts a candidate token. Both paths therefore agree on one builder key.
func statsUsageKey(u *usage.Usage) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{u.Harness, u.Provider, modelSansEffort(u.Model)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

// statsFixed returns counts in keys' order, zeroes included.
func statsFixed(keys []string, counts map[string]int) []StatsCount {
	out := make([]StatsCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, StatsCount{Key: k, Count: counts[k]})
	}
	return out
}

// statsSorted returns counts sorted by Count desc, then Key asc.
func statsSorted(counts map[string]int) []StatsCount {
	out := make([]StatsCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, StatsCount{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// statsInList reports whether key is one of list's keys.
func statsInList(list []string, key string) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

// RenderStats is `relay stats`' text layout. Labels are left-aligned in an
// 11-column field; a list in one of the fixed orders omits its zero items, and
// a section with nothing to show prints its own empty form instead.
func RenderStats(r StatsReport) string {
	if r.Rounds == 0 && len(r.Consults) == 0 && len(r.Blocked) == 0 {
		return "no rounds\n"
	}

	var sb strings.Builder
	if r.Since == nil {
		sb.WriteString("relay stats: all recorded rounds\n")
	} else {
		sb.WriteString("relay stats: since " + r.Since.UTC().Format("2006-01-02 15:04") + " UTC\n")
	}

	bindings := "bindings"
	if r.Bindings == 1 {
		bindings = "binding"
	}
	statsLine(&sb, "rounds", fmt.Sprintf("%d in %d %s", r.Rounds, r.Bindings, bindings))

	for i, b := range r.Builders {
		label := ""
		if i == 0 {
			label = "builders"
		}
		statsLine(&sb, label, fmt.Sprintf("%s  %d", b.Key, b.Count))
	}

	if items := statsNonZero(r.Outcomes); len(items) > 0 || r.Unmarked > 0 {
		body := strings.Join(items, ", ")
		if r.Unmarked > 0 {
			if body != "" {
				body += "; "
			}
			body += fmt.Sprintf("%d unmarked", r.Unmarked)
		}
		statsLine(&sb, "outcomes", body)
	}

	if r.Switches.Total == 0 {
		statsLine(&sb, "switches", "none")
	} else {
		body := fmt.Sprintf("%d in %d of %d rounds", r.Switches.Total, r.Switches.Rounds, r.Rounds)
		if r.Rounds > 0 {
			body += fmt.Sprintf(" (%d%%)", statsPct(r.Switches.Rounds, r.Rounds))
		}
		statsLine(&sb, "switches", body)
		for _, f := range r.Switches.From {
			statsLine(&sb, "", fmt.Sprintf("%s  %d (%s)", f.Provider, f.Count, strings.Join(statsNonZero(f.Reasons), ", ")))
		}
	}

	if items := statsNonZero(r.Gate); len(items) == 0 {
		statsLine(&sb, "gate", "no gated rounds")
	} else {
		gated, pass := 0, 0
		for _, c := range r.Gate {
			gated += c.Count
			if c.Key == "pass" {
				pass = c.Count
			}
		}
		statsLine(&sb, "gate", fmt.Sprintf("%s of %d gated rounds (%d%% pass)", strings.Join(items, ", "), gated, statsPct(pass, gated)))
	}

	consults := strings.Join(statsNonZero(r.Consults), ", ")
	if r.VerifySkipped > 0 {
		if consults != "" {
			consults += "; "
		}
		consults += fmt.Sprintf("%d verify skipped", r.VerifySkipped)
	}
	if consults == "" {
		statsLine(&sb, "consults", "none")
	} else {
		statsLine(&sb, "consults", consults)
	}
	for _, m := range r.ConsultModels {
		statsLine(&sb, "", fmt.Sprintf("%s  %d", m.Key, m.Count))
	}

	if len(r.Blocked) == 0 {
		statsLine(&sb, "blocked", "none")
	} else {
		for i, b := range r.Blocked {
			label := ""
			if i == 0 {
				label = "blocked"
			}
			statsLine(&sb, label, statsBlockedBody(b))
		}
	}
	statsLine(&sb, "", "(availability history keeps 30 days; an expired gate has no recorded length)")

	return sb.String()
}

// statsLine writes one report line: label left-aligned in an 11-column field,
// then body.
func statsLine(sb *strings.Builder, label, body string) {
	fmt.Fprintf(sb, "%-11s%s\n", label, body)
}

// statsNonZero renders the counts above zero as "key n", in the order given.
func statsNonZero(items []StatsCount) []string {
	out := make([]string, 0, len(items))
	for _, c := range items {
		if c.Count > 0 {
			out = append(out, fmt.Sprintf("%s %d", c.Key, c.Count))
		}
	}
	return out
}

// statsBlockedBody renders one provider's blocked row.
func statsBlockedBody(b StatsBlocked) string {
	parts := make([]string, 0, 3)
	if b.RateLimited > 0 {
		parts = append(parts, fmt.Sprintf("rate-limited %d", b.RateLimited))
	}
	if b.SpawnFailed > 0 {
		parts = append(parts, fmt.Sprintf("spawn failed %d", b.SpawnFailed))
	}
	if b.Cleared > 0 {
		parts = append(parts, fmt.Sprintf("%d cleared by hand (%s)", b.Cleared, blockedText(time.Duration(b.ClearedMS)*time.Millisecond)))
	}
	return fmt.Sprintf("%s  %s", b.Provider, strings.Join(parts, ", "))
}

// statsPct is round(100*a/b); b == 0 is 0.
func statsPct(a, b int) int {
	if b == 0 {
		return 0
	}
	return int(math.Round(100 * float64(a) / float64(b)))
}

// builderTokenFromNote returns the builder token a pick or switch note names,
// and whether the note named one at all.
//
// A remote switch note names the new token after its last " -> ". Otherwise
// the note is a pick: the token is the text after "picked " up to the next
// space, and it counts when the text right after it is " for builder:" or a
// remote pick's " on <server>: <how>". A remote binding has only a builder, so
// its pick names a builder token. A pick for reviewer, verify or any other
// role is not a builder token.
func builderTokenFromNote(note string) (string, bool) {
	if strings.HasPrefix(note, "switched on ") {
		if i := strings.LastIndex(note, " -> "); i >= 0 {
			return strings.TrimSpace(note[i+len(" -> "):]), true
		}
		return "", false
	}

	i := strings.Index(note, "picked ")
	if i < 0 {
		return "", false
	}
	rest := note[i+len("picked "):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return "", false
	}
	if strings.HasPrefix(rest[sp:], " for builder:") {
		return rest[:sp], true
	}
	// A remote pick reads "picked <tok> on <server>: <how>": the token is
	// followed by the server and then the how clause.
	if strings.HasPrefix(rest[sp:], " on ") && strings.Contains(rest[sp:], ": ") {
		return rest[:sp], true
	}
	return "", false
}

// remoteSwitchFrom returns the previous candidate in a remote switch note,
// "switched on <server>: <prev> -> <new>": the text between the first ": "
// after the prefix and the last " -> ", trimmed. Anything else is not a remote
// switch and returns "", false.
func remoteSwitchFrom(note string) (string, bool) {
	const prefix = "switched on "
	if !strings.HasPrefix(note, prefix) {
		return "", false
	}
	rest := note[len(prefix):]
	ci := strings.Index(rest, ": ")
	if ci < 0 {
		return "", false
	}
	rest = rest[ci+len(": "):]
	li := strings.LastIndex(rest, " -> ")
	if li < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:li]), true
}

// switchReason classifies a local switch note's reason from the text after
// "switched builder (". Everything unrecognised, a relaunch note included, is
// "other".
func switchReason(note string) string {
	const prefix = "switched builder ("
	if !strings.HasPrefix(note, prefix) {
		return "other"
	}
	rest := note[len(prefix):]
	switch {
	case strings.HasPrefix(rest, "rate-limited"):
		return "rate-limited"
	case strings.HasPrefix(rest, "exited"):
		return "exited"
	case strings.HasPrefix(rest, "gated"):
		return "gated"
	default:
		return "other"
	}
}

// refKey turns a candidate token into its harness/provider/model key, with the
// model cut at its first "#" (the effort suffix). A token that does not parse
// is "unknown".
func refKey(tok string) string {
	ref, err := candidate.ParseRef(tok)
	if err != nil {
		return "unknown"
	}
	return ref.Harness + "/" + ref.Provider + "/" + modelSansEffort(ref.Model)
}

// providerOfToken returns the provider of a candidate token, or "unknown" when
// the token does not parse.
func providerOfToken(tok string) string {
	ref, err := candidate.ParseRef(tok)
	if err != nil {
		return "unknown"
	}
	return ref.Provider
}
