package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNoCompletedRound is Show's error when no --round is given and every
// round of the binding is still open.
var ErrNoCompletedRound = errors.New("no completed round yet; --round N to read an open round's plan")

// ShowSection is one of a round's readable parts.
type ShowSection string

const (
	ShowPlan       ShowSection = "plan"
	ShowReport     ShowSection = "report"
	ShowDiff       ShowSection = "diff"
	ShowDrift      ShowSection = "drift"
	ShowLog        ShowSection = "log"
	ShowTranscript ShowSection = "transcript"
)

// ValidShowSection reports whether s is one of the ShowSection values.
func ValidShowSection(s ShowSection) bool {
	switch s {
	case ShowPlan, ShowReport, ShowDiff, ShowDrift, ShowLog, ShowTranscript:
		return true
	}
	return false
}

// ShowOptions is `relevo show`'s flags.
type ShowOptions struct {
	Name string
	// Round is the round to read; 0 means the newest completed round.
	Round   int
	Section ShowSection
	JSON    bool
}

// ShowResult is one round's requested section, resolved from a live
// binding's files or a past binding's database rows.
type ShowResult struct {
	Name       string
	Round      int
	Rounds     int
	Live       bool
	Archived   bool
	ArchivedAt time.Time
	Section    ShowSection
	// Text is the section's content; "" when Missing.
	Text string
	// Missing is true when the round has no such section (the file or the
	// artifact row is absent) -- not an error.
	Missing bool
	// Events is filled for Section log: one entry per event, in order.
	Events []store.LogEntry
}

// Show resolves opts against a live binding's files, falling back to the
// database for anything not live (docs/specs/2026-09-20-persistence-design.md
// §5.7). It returns store.ErrNotFound, wrapped, when opts.Name is neither a
// live binding nor a row in rt.DB.
func Show(ctx context.Context, rt Runtime, opts ShowOptions) (ShowResult, error) {
	if opts.Name == "" {
		return ShowResult{}, fmt.Errorf("show: a binding name is required")
	}
	if !ValidShowSection(opts.Section) {
		return ShowResult{}, fmt.Errorf("show: %q: invalid section", opts.Section)
	}

	b, err := rt.Store.Load(opts.Name)
	if err == nil {
		return showLive(rt, b, opts)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return ShowResult{}, err
	}

	if rt.DB != nil {
		binding, found, derr := rt.DB.Binding(opts.Name)
		if derr != nil {
			return ShowResult{}, derr
		}
		if found {
			return showDB(rt, binding, opts)
		}
	}

	return ShowResult{}, fmt.Errorf("binding %q not found (live or in the database): %w", opts.Name, store.ErrNotFound)
}

// showLive resolves opts against b's files, as `relevo show` reads a live
// binding today.
func showLive(rt Runtime, b store.Binding, opts ShowOptions) (ShowResult, error) {
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return ShowResult{}, err
	}

	// Rounds is the highest round number with a plan log entry, not
	// b.Round: b.Round is the *next* round once a round has closed
	// (finishRound does Round++), so it overcounts by one for an idle
	// binding and undercounts nothing for one mid-round -- the round in
	// flight has already logged its plan entry.
	rounds := 0
	completed := 0
	for _, e := range entries {
		if e.Kind == store.KindPlan && e.Round > rounds {
			rounds = e.Round
		}
		if e.Kind == store.KindReport && e.Round > completed {
			completed = e.Round
		}
	}

	round := opts.Round
	if round == 0 {
		if completed == 0 {
			completed = b.Round - 1
		}
		if completed < 1 {
			return ShowResult{}, ErrNoCompletedRound
		}
		round = completed
	} else if round < 1 || round > rounds {
		return ShowResult{}, fmt.Errorf("round %d: binding has %d rounds", round, rounds)
	}

	res := ShowResult{
		Name:    b.Name,
		Round:   round,
		Rounds:  rounds,
		Live:    true,
		Section: opts.Section,
	}

	switch opts.Section {
	case ShowPlan:
		res.Text, res.Missing, err = readFileOrMissing(rt.Store.ReadFile, rt.Store.PlanPath(b.Name, round))
	case ShowReport:
		res.Text, res.Missing, err = readFileOrMissing(rt.Store.ReadFile, rt.Store.ReportPath(b.Name, round))
	case ShowDiff:
		res.Text, res.Missing, err = readFileOrMissing(rt.Store.ReadFile, rt.Store.DiffPath(b.Name, round))
	case ShowDrift:
		res.Text, res.Missing, err = readFileOrMissing(rt.Store.ReadFile, rt.Store.DriftPath(b.Name, round))
	case ShowTranscript:
		res.Text, res.Missing, err = readFileOrMissing(rt.Store.ReadFile, rt.Store.BuilderLogPath(b.Name, round))
	case ShowLog:
		for _, e := range entries {
			if e.Round == round {
				res.Events = append(res.Events, e)
			}
		}
	}
	if err != nil {
		return ShowResult{}, err
	}
	return res, nil
}

// showDB resolves opts against binding's rows in rt.DB, for anything not
// live: an archived binding, or one the live store never held (adopted by
// name only after the fact, in tests).
func showDB(rt Runtime, binding db.BindingRow, opts ShowOptions) (ShowResult, error) {
	allRounds, err := rt.DB.Rounds(binding.ID)
	if err != nil {
		return ShowResult{}, err
	}
	total := len(allRounds)

	round := opts.Round
	if round == 0 {
		completed := 0
		for _, r := range allRounds {
			if r.Outcome != db.OutcomeOpen && r.Number > completed {
				completed = r.Number
			}
		}
		if completed < 1 {
			return ShowResult{}, ErrNoCompletedRound
		}
		round = completed
	} else if round < 1 || round > total {
		return ShowResult{}, fmt.Errorf("round %d: binding has %d rounds", round, total)
	}

	var target *db.Round
	for i := range allRounds {
		if allRounds[i].Number == round {
			target = &allRounds[i]
			break
		}
	}
	if target == nil {
		return ShowResult{}, fmt.Errorf("round %d: binding has %d rounds", round, total)
	}

	res := ShowResult{
		Name:    binding.Name,
		Round:   round,
		Rounds:  total,
		Live:    false,
		Section: opts.Section,
	}
	if binding.ArchivedAt != nil {
		res.Archived = true
		res.ArchivedAt = *binding.ArchivedAt
	}

	switch opts.Section {
	case ShowPlan, ShowReport, ShowDiff, ShowDrift:
		var a db.Artifact
		var found bool
		a, found, err = rt.DB.Artifact(target.ID, string(opts.Section))
		if err == nil {
			if found {
				res.Text = a.Text
			} else {
				res.Missing = true
			}
		}
	case ShowLog:
		// internal/ingest links event.round_id to its round once that
		// round's row exists (Task 0(a)), so db.Events(bindingID, round)
		// itself scopes to round -- no client-side re-filtering needed.
		var events []db.Event
		events, err = rt.DB.Events(binding.ID, round)
		for _, e := range events {
			var entry store.LogEntry
			if jerr := json.Unmarshal([]byte(e.EntryJSON), &entry); jerr == nil {
				res.Events = append(res.Events, entry)
			}
		}
	case ShowTranscript:
		var recs []db.TranscriptRecord
		recs, err = rt.DB.Transcript(db.OwnerRound, target.ID, 0, 0)
		if err == nil {
			lines := make([]string, 0, len(recs))
			for _, r := range recs {
				lines = append(lines, r.Rendered)
			}
			res.Text = strings.Join(lines, "\n")
			res.Missing = len(recs) == 0
		}
	}
	if err != nil {
		return ShowResult{}, err
	}
	return res, nil
}

// readFileOrMissing reads path through read, reporting Missing rather than an
// error when it does not exist. read is rt.Store.ReadFile, so a sealed
// round's file is found in the database exactly as a present one is.
func readFileOrMissing(read func(string) ([]byte, error), path string) (text string, missing bool, err error) {
	data, err := read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, err
	}
	return string(data), false, nil
}
