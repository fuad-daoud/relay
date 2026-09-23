package relay

// The view tests for #374 §3.1-§3.3: relay policy, relay candidates, the
// post-bind consult-name note and the legacy-field warnings read the roles
// registry when a roles.json is loaded, and reproduce today's bytes when it is
// not. The legacy behaviour is pinned by the older tests, which stay unedited.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/history"
	"github.com/fuad-daoud/relay/internal/ledger"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/roles"
)

// rolesViewsCandidatesJSON is claude a on both roles, claude b and opencode m
// on builder. In file mode the rows, not these roles, decide who serves what.
const rolesViewsCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// rolesViewsSection returns the part of got between the start line and the
// next line named end, so a test can assert on one role's block alone.
func rolesViewsSection(t *testing.T, got, start, end string) string {
	t.Helper()
	i := strings.Index(got, start)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", start, got)
	}
	rest := got[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("no %q after %q in:\n%s", end, start, got)
	}
	return rest[:j]
}

// TestRolesViewsFormatPolicyForFileMode pins §3.1's FormatPolicyFor: the header
// names roles.json, the rows are the row's candidate list in order with an
// `order` tag and the pick marked, a role with nothing listed says so, and the
// legacy "no policy configured" line is never printed.
//
// Mutation check: make FormatPolicyFor print FormatPolicy's "  (no order set)"
// header instead of "  (roles.json)" and this test fails on its first line.
func TestRolesViewsFormatPolicyForFileMode(t *testing.T) {
	set := candidateSet(t, rolesViewsCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{"claude/test/b", "claude/test/a"}},
		"reviewer": {},
	})

	got := FormatPolicyFor(reg, set, policy.Policy{}, nil, history.History{}, baseTime, time.UTC)

	want := "builder  (roles.json)\n" +
		"  1  claude/test/b    order     <- would pick\n" +
		"  2  claude/test/a    order\n" +
		"reviewer  (roles.json)\n" +
		"  no candidate listed in roles.json reviewer.candidates\n" +
		"researcher  (roles.json)\n" +
		"  no candidate listed in roles.json researcher.candidates\n"
	if got != want {
		t.Errorf("FormatPolicyFor =\n%q\nwant:\n%q", got, want)
	}

	if !strings.Contains(got, "builder  (roles.json)") {
		t.Errorf("output must carry the file-mode header, got:\n%s", got)
	}
	pick := strings.Index(got, "1  claude/test/b    order     <- would pick")
	second := strings.Index(got, "2  claude/test/a")
	if pick < 0 || second < 0 || pick > second {
		t.Errorf("the row's first candidate must be row 1 with the pick, then the second:\n%s", got)
	}
	if !strings.Contains(got, "no candidate listed in roles.json reviewer.candidates") {
		t.Errorf("a role with nothing listed must say so, got:\n%s", got)
	}
	if strings.Contains(got, "no policy configured") {
		t.Errorf("file mode must not print the legacy no-policy line:\n%s", got)
	}
}

// TestRolesViewsFormatPolicyForLegacyMatches pins §3.1's legacy half: with a
// legacy registry the file-mode entry point is FormatPolicy byte for byte.
func TestRolesViewsFormatPolicyForLegacyMatches(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testClaudeRef, testAgyRef)
	gates := []ledger.Gate{{Token: testClaudeRef, Kind: ledger.RateLimited, Until: baseTime.Add(time.Hour)}}

	legacy, _ := roles.Build(nil, set, pol)
	want := FormatPolicy(set, pol, gates, history.History{}, baseTime, time.UTC)
	got := FormatPolicyFor(legacy, set, pol, gates, history.History{}, baseTime, time.UTC)
	if got != want {
		t.Errorf("FormatPolicyFor(legacy) =\n%q\nwant FormatPolicy's:\n%q", got, want)
	}
}

// TestRolesViewsPerRoleGateFiltering pins §3.1's per-role gate filtering in the
// legacy renderer: a roles-missing gate scoped to reviewer never shows on a
// builder row, and does show on reviewer's.
//
// Mutation check: render the rows from every gate instead of gatesForRole and
// the builder half fails.
func TestRolesViewsPerRoleGateFiltering(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	gates := []ledger.Gate{{
		Token: testClaudeRef, Kind: ledger.RolesMissing, Role: "reviewer",
		Note: "roles missing for reviewer",
	}}

	got := FormatPolicy(set, policy.Policy{}, gates, history.History{}, baseTime, time.UTC)

	builder := rolesViewsSection(t, got, "builder  (no order set)", "reviewer  (no order set)")
	if strings.Contains(builder, "roles missing") {
		t.Errorf("a reviewer-scoped gate must not show on builder rows:\n%s", builder)
	}
	reviewer := rolesViewsSection(t, got, "reviewer  (no order set)", "researcher  (no order set)")
	if !strings.Contains(reviewer, "roles missing") {
		t.Errorf("a reviewer-scoped gate must show on reviewer rows:\n%s", reviewer)
	}
}

// TestRolesViewsPolicyWarningsForFileMode pins §3.1's PolicyWarningsFor: a
// listed token that is not configured, and a listed token whose kind has no
// definition for the role, each give their exact text -- and nothing else,
// since in file mode the list is the assignment.
func TestRolesViewsPolicyWarningsForFileMode(t *testing.T) {
	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
	]`)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"scout": {Shape: ptr("reader"), Candidates: []string{"claude/test/m", "claude/test/ghost"}},
	})

	got := PolicyWarningsFor(reg, set, policy.Policy{})
	want := []PolicyWarning{
		{
			Role: "scout", Index: 0, Token: "claude/test/m",
			Text: `roles.json scout.candidates[0] "claude/test/m": scout has no definition for claude`,
		},
		{
			Role: "scout", Index: 1, Token: "claude/test/ghost",
			Text: `roles.json scout.candidates[1] "claude/test/ghost" is not a configured candidate`,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PolicyWarningsFor = %+v, want %+v", got, want)
	}
}

// TestRolesViewsRoleRefusalsForFileMode pins §3.1's RoleRefusalsFor: with every
// listed candidate gated for builder only, builder has a refusal and reviewer
// -- same candidates, same gates -- has none.
func TestRolesViewsRoleRefusalsForFileMode(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef, testOpencodeRef}},
		"reviewer": {Candidates: []string{testClaudeRef, testOpencodeRef}},
	})
	gates := []ledger.Gate{
		{Token: testClaudeRef, Kind: ledger.RateLimited, Role: "builder", Until: baseTime.Add(time.Hour)},
		{Token: testOpencodeRef, Kind: ledger.RateLimited, Role: "builder", Until: baseTime.Add(time.Hour)},
	}

	got := RoleRefusalsFor(reg, set, policy.Policy{}, gates)
	if len(got) != 1 {
		t.Fatalf("RoleRefusalsFor = %+v, want exactly the builder refusal", got)
	}
	if got[0].Role != "builder" {
		t.Errorf("refusal role = %q, want builder (the gate is builder-scoped)", got[0].Role)
	}
	if !strings.Contains(got[0].Text, "every candidate serving builder is gated") {
		t.Errorf("refusal text = %q, want the all-gated wording", got[0].Text)
	}
}

// TestRolesViewsFormatCandidatesLatencyForFileMode pins §3.2: the roles column
// is the registry's answer, "(no role)" when no role lists the candidate, and
// the tier segment is gone -- the role owns the tier in file mode.
func TestRolesViewsFormatCandidatesLatencyForFileMode(t *testing.T) {
	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"},
	  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
	]`)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef}},
		"reviewer": {Candidates: []string{testClaudeRef}},
	})

	got := FormatCandidatesLatencyFor(reg, set, nil, nil)
	want := "claude/test/m    builder, reviewer\n" +
		"opencode/test/m  (no role)\n"
	if got != want {
		t.Errorf("FormatCandidatesLatencyFor =\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "tier:") {
		t.Errorf("file mode must not print the candidate's tier:\n%s", got)
	}

	legacy, _ := roles.Build(nil, set, policy.Policy{})
	if a, b := FormatCandidatesLatencyFor(legacy, set, nil, nil), FormatCandidatesLatency(set, nil, nil); a != b {
		t.Errorf("FormatCandidatesLatencyFor(legacy) = %q, want FormatCandidatesLatency's %q", a, b)
	}
}

// TestRolesViewsConsultRolesTooLongFor pins §3.3: the note reads the registry,
// so a reader role only roles.json knows is reported, while the legacy wrapper
// keeps reporting only the built-in consult roles.
func TestRolesViewsConsultRolesTooLongFor(t *testing.T) {
	const newReader = "abcdefghijklmnopqrstuvwxyz0123" // 30 characters
	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		newReader: {Shape: ptr("reader")},
	})

	got := ConsultRolesTooLongFor(reg, "ab")
	want := []string{newReader}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConsultRolesTooLongFor = %v, want %v", got, want)
	}

	legacy, _ := roles.Build(nil, set, policy.Policy{})
	if a, b := ConsultRolesTooLong("ab"), ConsultRolesTooLongFor(legacy, "ab"); !reflect.DeepEqual(a, b) || len(a) != 0 {
		t.Errorf("ConsultRolesTooLong(\"ab\") = %v, want the legacy registry's empty result (got %v)", a, b)
	}
}

// TestRolesViewsLegacyRoleFieldWarnings pins §3.1's LegacyRoleFieldWarnings:
// file mode names each legacy field still set, in order, and legacy mode says
// nothing -- without roles.json those fields are the source, not stale copies.
func TestRolesViewsLegacyRoleFieldWarnings(t *testing.T) {
	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"}
	]`)
	pol := policy.Policy{
		Order: map[string][]string{"builder": {testClaudeRef}},
		Tier:  map[string]string{"builder": "edit"},
	}
	reg := rolesFileRegistry(t, set, pol, map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef}},
	})

	got := LegacyRoleFieldWarnings(reg, set, pol)
	want := []string{
		"candidates.json: claude/test/m: roles is ignored; roles.json assigns candidates to roles",
		"candidates.json: claude/test/m: tier is ignored; set the role's tier in roles.json",
		"policy.json: order is ignored; roles.json <role>.candidates orders them",
		"policy.json: tier is ignored; set the role's tier in roles.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyRoleFieldWarnings =\n%q\nwant:\n%q", got, want)
	}

	legacy, _ := roles.Build(nil, set, pol)
	if got := LegacyRoleFieldWarnings(legacy, set, pol); got != nil {
		t.Errorf("LegacyRoleFieldWarnings(legacy) = %q, want nil", got)
	}
}

// TestRolesViewsMergedGateTexts pins §3.3: a candidate gated for two roles
// prints one merged roles-missing part naming both, in sorted order, beside the
// role-free gate rendered as it always was.
//
// Mutation check: render one part per gate instead of grouping by kind and
// until, and the merged roles-missing part is gone.
func TestRolesViewsMergedGateTexts(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	until := baseTime.Add(time.Hour)
	gates := []ledger.Gate{
		{Token: testClaudeRef, Kind: ledger.RolesMissing, Role: "reviewer"},
		{Token: testClaudeRef, Kind: ledger.RolesMissing, Role: "builder"},
		{Token: testClaudeRef, Kind: ledger.RateLimited, Until: until},
	}

	got := FormatCandidates(set, gates)

	want := "   unavailable: roles missing (builder, reviewer) until cleared; " +
		GateKindText(ledger.RateLimited) + " " + GateUntilText(until)
	if !strings.Contains(got, want) {
		t.Errorf("FormatCandidates =\n%q\nwant it to contain:\n%q", got, want)
	}
	if n := strings.Count(got, "roles missing"); n != 1 {
		t.Errorf("roles missing appears %d times, want the merged part once:\n%s", n, got)
	}
}

// TestRolesViewsResolveRoleFileModeNothingServes pins §3.4: in file mode a role
// whose row lists nothing gives the roles.json wording, and the error still
// matches ErrRoleNotServed.
func TestRolesViewsResolveRoleFileModeNothingServes(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{}},
	})

	_, err := resolveRole(reg, set, nil, "", "builder")
	if err == nil {
		t.Fatal("resolveRole with an empty roles.json builder.candidates = nil, want an error")
	}
	if !strings.Contains(err.Error(), "roles.json builder.candidates") {
		t.Errorf("err = %q, want it to name roles.json builder.candidates", err)
	}
	if !errors.Is(err, ErrRoleNotServed) {
		t.Errorf("err = %q, want errors.Is(err, ErrRoleNotServed)", err)
	}
}
