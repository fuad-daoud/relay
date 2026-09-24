package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

func ptr[T any](v T) *T { return &v }

func openTestHistoryDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestHistoryLineColumns(t *testing.T) {
	commits := 2
	tree := "clean"
	cost := 0.42
	basisMeasured := "measured"
	basisEstimated := "estimated"
	basisUnknown := "unknown"
	candidate := "agy/antigravity/opus"

	base := db.RoundRow{
		BindingName:      "api-auth",
		Number:           3,
		StartedAt:        time.Date(2026, 9, 15, 14, 2, 0, 0, time.UTC),
		Outcome:          "reported",
		BuilderCandidate: &candidate,
		Commits:          &commits,
		Tree:             &tree,
		CostUSD:          &cost,
		CostBasis:        &basisMeasured,
		Archived:         true,
	}

	want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)"
	if got := HistoryLine(base, time.UTC); got != want {
		t.Errorf("HistoryLine(measured, archived) =\n%q\nwant\n%q", got, want)
	}

	t.Run("nil cost", func(t *testing.T) {
		r := base
		r.CostUSD = nil
		r.CostBasis = nil
		r.Archived = false
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  -"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(nil cost) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("estimated basis", func(t *testing.T) {
		r := base
		r.CostBasis = &basisEstimated
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  ~$0.42  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(estimated) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("unknown basis", func(t *testing.T) {
		r := base
		r.CostBasis = &basisUnknown
		want := "2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  unknown  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(unknown basis) =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("long name truncated", func(t *testing.T) {
		r := base
		r.BindingName = "a-very-long-binding-name-indeed"
		want := "2026-09-15 14:02  a-very-long…  r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)"
		if got := HistoryLine(r, time.UTC); got != want {
			t.Errorf("HistoryLine(long name) =\n%q\nwant\n%q", got, want)
		}
	})
}

func TestFormatHistoryEmpty(t *testing.T) {
	if got := FormatHistory(nil, time.UTC); got != "no rounds\n" {
		t.Errorf("FormatHistory(nil) = %q, want %q", got, "no rounds\n")
	}
	if got := FormatHistory([]db.RoundRow{}, time.UTC); got != "no rounds\n" {
		t.Errorf("FormatHistory(empty) = %q, want %q", got, "no rounds\n")
	}
}

func TestHistoryOptionsFilterHere(t *testing.T) {
	g := &fakeGit{repoFactsOrigin: "git@github.com:o/r.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, _, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "https://github.com/o/r" {
		t.Errorf("Repo = %q, want the normalised origin", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
	if !f.Newest {
		t.Error("Newest = false, want true")
	}
}

func TestHistoryOptionsFilterHereNoRemote(t *testing.T) {
	g := &fakeGit{repoFactsCommonDir: "/work/repo/.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, _, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "/work/repo/.git" {
		t.Errorf("Repo = %q, want the common dir", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
}

func TestBindingsNoDatabase(t *testing.T) {
	rt := Runtime{}
	_, err := Bindings(context.Background(), rt, "")
	if !errors.Is(err, ErrNoDatabase) {
		t.Errorf("err = %v, want ErrNoDatabase", err)
	}
}

func TestBindingsHereResolvesRepo(t *testing.T) {
	d := openTestHistoryDB(t)

	repoA, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/a"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo a: %v", err)
	}
	repoB, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/b"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo b: %v", err)
	}

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha-old", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: older, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha-old: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha-new", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: newer, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha-new: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "beta", RepoID: &repoB, CWD: "/work/b", BuilderMode: "pane",
		CreatedAt: newer, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding beta: %v", err)
	}

	g := &fakeGit{repoFactsOrigin: "git@github.com:o/a.git"}
	rt := Runtime{DB: d, Git: g}

	got, err := Bindings(context.Background(), rt, "/work/repo")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2 (only repo a's rows)", len(got))
	}
	if got[0].Name != "alpha-new" || got[1].Name != "alpha-old" {
		t.Errorf("order = [%s, %s], want [alpha-new, alpha-old] (newest first)", got[0].Name, got[1].Name)
	}
}

func TestBindingsHereNotARepoMeansAll(t *testing.T) {
	d := openTestHistoryDB(t)

	repoA, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/a"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo a: %v", err)
	}
	repoB, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/b"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo b: %v", err)
	}
	now := time.Now()
	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: now, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "beta", RepoID: &repoB, CWD: "/work/b", BuilderMode: "pane",
		CreatedAt: now, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding beta: %v", err)
	}

	g := &fakeGit{repoFactsErr: errors.New("not a git repository")}
	rt := Runtime{DB: d, Git: g}

	got, err := Bindings(context.Background(), rt, "/tmp/not-a-repo")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2 (a non-repo cwd means no filter)", len(got))
	}
}

func TestHistoryBindingArchivedFacts(t *testing.T) {
	d := seedShowArchiveDB(t)
	rt := Runtime{DB: d}

	got, err := Bindings(context.Background(), rt, "")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Bindings) = %d, want 1", len(got))
	}
	hb := got[0]
	if hb.Name != "fixture" {
		t.Errorf("Name = %q, want fixture", hb.Name)
	}
	if !hb.Archived {
		t.Error("Archived = false, want true")
	}
	if hb.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the tarball's stamp")
	}
}

func TestHistoryOptionsSinceUntil(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Since: "24h", Until: "2026-09-01"}

	f, _, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	wantSince := now.Add(-24 * time.Hour)
	if !f.Since.Equal(wantSince) {
		t.Errorf("Since = %v, want %v", f.Since, wantSince)
	}
	wantUntil := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !f.Until.Equal(wantUntil) {
		t.Errorf("Until = %v, want %v", f.Until, wantUntil)
	}
}

// TestHistoryOptionsQueryMergesWithFlagNote pins the merge rule: -q is
// parsed first, the explicit flag wins, and one note naming the override
// comes back for the CLI to print to stderr.
func TestHistoryOptionsQueryMergesWithFlagNote(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Query: "harness:agy", Harness: "codex"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Harness != "codex" {
		t.Errorf("Harness = %q, want codex (the flag wins)", f.Harness)
	}
	if !f.Newest {
		t.Error("Newest = false, want true")
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --harness overrides harness:agy from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
}

// TestHistoryOptionsByOverridesQuery pins that --by overrides a by: in -q
// the same way, both in the note and in the Query the caller reads back.
func TestHistoryOptionsByOverridesQuery(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Query: "by:day harness:agy", By: "builder"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Harness != "agy" {
		t.Errorf("Harness = %q, want agy (no flag overrode it)", f.Harness)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisBuilder {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisBuilder)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --by overrides by:day from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
}

// TestHistoryOptionsQueryError pins that a bad -q comes back as histq's own
// ErrQuery, which the CLI maps to exit 2.
func TestHistoryOptionsQueryError(t *testing.T) {
	opts := HistoryOptions{Query: "outcome:nope"}
	_, _, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	var eq histq.ErrQuery
	if !errors.As(err, &eq) {
		t.Fatalf("Filter error = %v, want histq.ErrQuery", err)
	}
	if eq.Token != "outcome:nope" {
		t.Errorf("ErrQuery.Token = %q, want outcome:nope", eq.Token)
	}
}

// TestHistoryOptionsFilterDefaultsAxisNone pins that a Filter with no -q
// query still names an axis: the parsed query starts at AxisNone rather than
// the zero value, so a caller reading ParsedQuery().By never sees "" (which
// cmdHistory read as a regroup axis and turned into "no rounds").
func TestHistoryOptionsFilterDefaultsAxisNone(t *testing.T) {
	opts := HistoryOptions{}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisNone {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisNone)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none", notes)
	}
}

// TestHistoryOptionsByAloneGivesNoNote pins the second symptom: --by with no
// -q query has nothing to conflict with, so it must not print the spurious
// `note: --by overrides by: from -q`.
func TestHistoryOptionsByAloneGivesNoNote(t *testing.T) {
	opts := HistoryOptions{By: "builder"}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none: --by has no -q by: to override", notes)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisBuilder {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisBuilder)
	}
}

// TestHistoryOptionsByOverridesQueryByNote pins that a real conflict is
// kept: -q named by:binding and --by builder still notes the override.
func TestHistoryOptionsByOverridesQueryByNote(t *testing.T) {
	opts := HistoryOptions{Query: "by:binding", By: "builder"}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --by overrides by:binding from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisBuilder {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisBuilder)
	}
}

// TestFormatGroupsColumns pins the exact header and one group row, the axis
// column padded to 40, tokens and cost in their short forms, and the empty
// view.
func TestFormatGroupsColumns(t *testing.T) {
	groups := []histq.GroupRow{{
		Key:      "agy/antigravity/claude-sonnet-4-6",
		Rounds:   31,
		Reported: 27,
		Halted:   3,
		Commits:  58,
		Tokens:   22_100_000,
		CostUSD:  9.10,
		Unknown:  3,
		Last:     time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
	}}

	got := FormatGroups(groups, histq.AxisBuilder, time.UTC)
	want := "builder" + strings.Repeat(" ", 35) +
		"rounds  reported  halted  commits  tokens   cost  last      \n" +
		"agy/antigravity/claude-sonnet-4-6         " +
		"    31" + "  " + "      27" + "  " + "     3" + "  " + "     58" +
		"  " + " 22.1M" + "  " + "$9.10 (3 unknown)" + "  " + "2026-09-20\n"
	if got != want {
		t.Errorf("FormatGroups =\n%q\nwant\n%q", got, want)
	}

	if got := FormatGroups(nil, histq.AxisBuilder, time.UTC); got != "no rounds\n" {
		t.Errorf("FormatGroups(nil) = %q, want %q", got, "no rounds\n")
	}

	long := FormatGroups([]histq.GroupRow{{Key: strings.Repeat("x", 45) + "end"}}, histq.AxisDay, time.UTC)
	if !strings.Contains(long, "…") {
		t.Errorf("FormatGroups(long key) = %q, want a “…” truncation", long)
	}
}

// TestHistoryFilterResolvesName pins A1 §4.2: a --candidate value with no "/"
// resolves to its canonical token, and an unresolved value is left as typed.
func TestHistoryFilterResolvesName(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	o := HistoryOptions{Candidate: "claude-m", Names: set}
	f, _, err := o.Filter(context.Background(), Runtime{}, baseTime)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Candidate != testClaudeRef {
		t.Errorf("Candidate = %q, want %q", f.Candidate, testClaudeRef)
	}

	unresolved := HistoryOptions{Candidate: "nope", Names: set}
	f, _, err = unresolved.Filter(context.Background(), Runtime{}, baseTime)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Candidate != "nope" {
		t.Errorf("Candidate = %q, want it left as typed", f.Candidate)
	}
}
