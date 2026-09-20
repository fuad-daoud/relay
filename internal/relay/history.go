package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/usage"
)

// ErrNoDatabase is Bindings's error when rt.DB is nil: the caller (the ui's
// "all" scope, today) has no database to read past bindings from and must
// fall back to live-only, not fail.
var ErrNoDatabase = errors.New("relay: no database")

// HistoryBinding is one binding, shaped for the ui's rail: enough to render
// a row (name, round count, last activity, feature, repo, final state,
// archived facts) without the caller touching internal/db directly. Live is
// left false here -- the ui sets it once it knows which names are also in
// the live report (docs/specs/2026-09-20-persistence-design.md §5.8).
type HistoryBinding struct {
	Name         string
	ID           string
	Rounds       int
	LastActivity time.Time
	Feature      string
	Repo         string
	FinalState   string
	Archived     bool
	ArchivedAt   time.Time
	Live         bool
}

// Bindings lists every binding rt.DB has recorded, newest activity first,
// optionally narrowed to the repo here belongs to. rt.DB == nil reports
// ErrNoDatabase. When here != "", it resolves through rt.Git.RepoFacts
// exactly as HistoryOptions.Filter does -- except here a cwd that is not a
// git repository (or a nil rt.Git) means no filter at all, not an error:
// the ui's "all" scope wants every binding when it cannot place the cwd in
// a repo, the way HistoryOptions.Filter's CLI caller instead wants to fail
// loud on an unresolvable --here.
func Bindings(ctx context.Context, rt Runtime, here string) ([]HistoryBinding, error) {
	if rt.DB == nil {
		return nil, ErrNoDatabase
	}

	f := db.Filter{}
	if here != "" && rt.Git != nil {
		if originURL, commonDir, err := rt.Git.RepoFacts(ctx, here); err == nil {
			normalised := git.NormalizeOriginURL(originURL)
			if normalised != "" {
				f.Repo = normalised
			} else {
				f.Repo = commonDir
			}
		}
	}

	rows, err := rt.DB.Bindings(f)
	if err != nil {
		return nil, err
	}

	out := make([]HistoryBinding, 0, len(rows))
	for _, r := range rows {
		hb := HistoryBinding{
			Name:         r.Name,
			ID:           r.ID,
			Rounds:       r.Rounds,
			LastActivity: r.LastActivity,
		}
		if r.Feature != nil {
			hb.Feature = *r.Feature
		}
		switch {
		case r.RepoOrigin != nil:
			hb.Repo = *r.RepoOrigin
		case r.RepoCommonDir != nil:
			hb.Repo = *r.RepoCommonDir
		}
		if r.FinalState != nil {
			hb.FinalState = *r.FinalState
		}
		if r.ArchivedAt != nil {
			hb.Archived = true
			hb.ArchivedAt = *r.ArchivedAt
		}
		out = append(out, hb)
	}
	return out, nil
}

// HistoryOptions is `relay history`'s flags, translated to a db.Filter by
// Filter. The zero value asks for every round, newest first.
type HistoryOptions struct {
	// Here is a cwd to resolve into a repo filter; "" means no such
	// resolution is wanted (Repo, if set, is used as-is).
	Here                                                                    string
	Repo                                                                    string
	Feature, Binding, Planner, Harness, Provider, Model, Candidate, Outcome string
	// Since/Until are relay.ParseSince forms: "", "24h", "7d", "YYYY-MM-DD".
	Since, Until string
	// Archived: nil means both; true archived only; false live only.
	Archived *bool
	// Limit: 0 means all rows; the CLI layer defaults it to 200.
	Limit int
}

// Filter resolves o into a db.Filter. Here, when set, is resolved via
// rt.Git.RepoFacts into the repo's normalised origin URL when it has a
// remote, else its common dir -- errors when Here is not a git repository.
// Since/Until go through ParseSince. Newest is always true: `relay history`
// prints newest first.
func (o HistoryOptions) Filter(ctx context.Context, rt Runtime, now time.Time) (db.Filter, error) {
	f := db.Filter{
		Repo:      o.Repo,
		Feature:   o.Feature,
		Binding:   o.Binding,
		Planner:   o.Planner,
		Harness:   o.Harness,
		Provider:  o.Provider,
		Model:     o.Model,
		Candidate: o.Candidate,
		Outcome:   o.Outcome,
		Archived:  o.Archived,
		Limit:     o.Limit,
		Newest:    true,
	}

	if o.Here != "" {
		if rt.Git == nil {
			return db.Filter{}, fmt.Errorf("--here: %s: not a git repository", o.Here)
		}
		originURL, commonDir, err := rt.Git.RepoFacts(ctx, o.Here)
		if err != nil {
			return db.Filter{}, fmt.Errorf("--here: %s: not a git repository: %w", o.Here, err)
		}
		normalised := git.NormalizeOriginURL(originURL)
		if normalised != "" {
			f.Repo = normalised
		} else {
			f.Repo = commonDir
		}
	}

	since, err := ParseSince(o.Since, now)
	if err != nil {
		return db.Filter{}, err
	}
	until, err := ParseSince(o.Until, now)
	if err != nil {
		return db.Filter{}, err
	}
	f.Since = since
	f.Until = until

	return f, nil
}

// HistoryLine formats one round exactly as `relay history` prints it:
//
//	2026-09-15 14:02  api-auth      r3  agy/antigravity/opus         reported   +2 commits  clean  $0.42   (archived)
//
// started local time (in loc); the binding name, padded to 12 and
// truncated with "…" past it; the round number as "rN"; the builder
// candidate, padded to 40; the outcome, padded to 14; commits ("+N
// commits", "-" when nil); tree state ("-" when nil); cost ("$0.42",
// "~$0.42" when the basis is estimated, "unknown" when the basis is
// unknown, "-" when there is no cost at all); "(archived)" appended when
// the binding is archived.
func HistoryLine(r db.RoundRow, loc *time.Location) string {
	started := r.StartedAt.In(loc).Format("2006-01-02 15:04")
	nameCol := padTrunc(r.BindingName, 12)
	candidateCol := padWidth(derefStr(r.BuilderCandidate), 40)
	outcomeCol := padWidth(r.Outcome, 14)

	commits := "-"
	if r.Commits != nil {
		commits = fmt.Sprintf("+%d commits", *r.Commits)
	}

	tree := "-"
	if r.Tree != nil {
		tree = *r.Tree
	}

	cost := formatHistoryCost(r.CostUSD, r.CostBasis)

	line := fmt.Sprintf("%s  %s  r%d  %s  %s  %s  %s  %s",
		started, nameCol, r.Number, candidateCol, outcomeCol, commits, tree, cost)
	if r.Archived {
		line += "  (archived)"
	}
	return line
}

// FormatHistory renders rows one HistoryLine per line (the caller controls
// order via Filter.Newest), or "no rounds" when rows is empty.
func FormatHistory(rows []db.RoundRow, loc *time.Location) string {
	if len(rows) == 0 {
		return "no rounds\n"
	}
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString(HistoryLine(r, loc))
		sb.WriteString("\n")
	}
	return sb.String()
}

// padTrunc left-justifies s to width w, truncating with a trailing "…" when
// s is longer than w.
func padTrunc(s string, w int) string {
	r := []rune(s)
	if len(r) > w && w > 0 {
		s = string(r[:w-1]) + "…"
	}
	return fmt.Sprintf("%-*s", w, s)
}

// padWidth left-justifies s to width w with no truncation.
func padWidth(s string, w int) string {
	return fmt.Sprintf("%-*s", w, s)
}

func derefStr(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// formatHistoryCost renders a round's cost the way `relay tab` renders
// money (internal/usage.Money), except a round with no usage at all prints
// "-" rather than an empty cell.
func formatHistoryCost(usd *float64, basis *string) string {
	if usd == nil {
		return "-"
	}
	b := usage.Measured
	if basis != nil {
		b = usage.Basis(*basis)
	}
	return usage.Money(usage.Cost{USD: *usd, Basis: b})
}
