package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/store"
)

// GitFacts is the slice of git identity relay's ingest needs, matching
// relay.Git.RepoFacts structurally so a *relay.Runtime's Git can be passed
// through IngestDeps with no import of internal/relay here (that package
// is ingest's caller; the two must not import each other).
type GitFacts interface {
	RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error)
}

// SessionLocator mirrors relay.SessionLocator's signature for the same
// reason GitFacts mirrors relay.Git: internal/relay is ingest's caller.
// relay.IngestDeps converts a relay.SessionLocator to this type at the
// boundary.
type SessionLocator func(kind, sessionID string) (path string, ok bool)

// Deps are Ingest's optional collaborators. The zero value is usable: no
// repo is ever resolved from git, no planner transcript is ever located,
// Now is time.Now, and Logger is slog.Default().
type Deps struct {
	// Git resolves a live binding's repo identity when bind.json carries
	// no RepoRef. Nil means never resolve.
	Git GitFacts
	// Sessions locates a planner's own transcript file when bind.json
	// carries no locator. Nil means never locate.
	Sessions SessionLocator
	Now      func() time.Time
	Logger   *slog.Logger
}

// Stats summarises what one Ingest call wrote.
type Stats struct {
	Bindings, Rounds, Events, Artifacts, TranscriptRecords, Skipped int
}

// Add returns the field-wise sum of s and o.
func (s Stats) Add(o Stats) Stats {
	return Stats{
		Bindings:          s.Bindings + o.Bindings,
		Rounds:            s.Rounds + o.Rounds,
		Events:            s.Events + o.Events,
		Artifacts:         s.Artifacts + o.Artifacts,
		TranscriptRecords: s.TranscriptRecords + o.TranscriptRecords,
		Skipped:           s.Skipped + o.Skipped,
	}
}

// memberStore is a throwaway *store.Store used only for its path-helper
// methods, so every member basename Ingest looks for is derived from them
// and filepath.Base rather than hard-coded (the plan's own instruction).
var memberStore = store.New("/")

func donePathBase(round int) string     { return filepath.Base(memberStore.DonePath("x", round)) }
func planPathBase(round int) string     { return filepath.Base(memberStore.PlanPath("x", round)) }
func reportPathBase(round int) string   { return filepath.Base(memberStore.ReportPath("x", round)) }
func diffPathBase(round int) string     { return filepath.Base(memberStore.DiffPath("x", round)) }
func driftPathBase(round int) string    { return filepath.Base(memberStore.DriftPath("x", round)) }
func gateLogPathBase(round int) string  { return filepath.Base(memberStore.GateLogPath("x", round)) }
func questionPathBase(round int) string { return filepath.Base(memberStore.QuestionPath("x", round)) }
func builderLogPathBase(round int) string {
	return filepath.Base(memberStore.BuilderLogPath("x", round))
}
func builderStreamPathBase(round int) string {
	return filepath.Base(memberStore.BuilderStreamPath("x", round))
}

// roundArtifactMembers is every plain (non-consult) round file Ingest
// captures as an artifact, in the order the plan lists them.
var roundArtifactMembers = []struct {
	base func(round int) string
	kind string
}{
	{planPathBase, db.ArtifactPlan},
	{reportPathBase, db.ArtifactReport},
	{diffPathBase, db.ArtifactDiff},
	{driftPathBase, db.ArtifactDrift},
	{gateLogPathBase, db.ArtifactGateLog},
	{questionPathBase, db.ArtifactQuestion},
}

// roundFromMember returns the round number encoded in a round-scoped
// member's "NNN-" prefix -- the layout every roundFile and consultFile
// member shares (internal/store/store.go) -- false when name has no such
// prefix (bind.json, log.jsonl).
func roundFromMember(name string) (int, bool) {
	if len(name) < 4 || name[3] != '-' {
		return 0, false
	}
	n, err := strconv.Atoi(name[:3])
	if err != nil {
		return 0, false
	}
	return n, true
}

// consultMemberID returns the consult id encoded in a member named
// "NNN-<id>-ask.md" or "NNN-<id>-findings.md" for round, when name has
// that suffix; false otherwise.
func consultMemberID(name string, round int, suffix string) (string, bool) {
	prefix := fmt.Sprintf("%03d-", round)
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if id == "" {
		return "", false
	}
	return id, true
}

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// repoRefFromGit resolves dir's repo identity via g, returning nil when dir
// is empty, RepoFacts errors (dir missing or not a git tree), or the facts
// carry neither an origin nor a common dir.
func repoRefFromGit(ctx context.Context, g GitFacts, dir string) *store.RepoRef {
	if dir == "" {
		return nil
	}
	originURL, commonDir, err := g.RepoFacts(ctx, dir)
	if err != nil {
		return nil
	}
	normalised := git.NormalizeOriginURL(originURL)
	if normalised == "" && commonDir == "" {
		return nil
	}
	return &store.RepoRef{OriginURL: normalised, CommonDir: commonDir}
}

// sourceOpener adapts a Source member to readAppendOnly's generic opener
// shape, and returns the member's cursor key alongside it.
func sourceOpener(src Source, member string) (func() (io.ReadCloser, error), string) {
	return func() (io.ReadCloser, error) {
		rc, _, err := src.Open(member)
		return rc, err
	}, cursorSourceKey(src, member)
}

// Ingest reads src -- a binding's directory, live or archived -- and
// upserts every fact it holds into d in one transaction: the repo, the
// planner, the binding, every round with its derived outcome and builder,
// every log event, every captured artifact, and every transcript record
// (docs/specs/2026-09-20-persistence-design.md §5.2). Cursors for every
// append-only and whole-file member it touched are saved in the same
// transaction, so a later call with unchanged files writes nothing.
//
// It returns ErrSource, unwrapped, when src's bind.json is missing or
// invalid, or its tarball cannot be read -- nothing is written in that
// case. A database error is wrapped.
func Ingest(ctx context.Context, src Source, d *db.DB, deps Deps) (Stats, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	b, err := src.Bind()
	if err != nil {
		return Stats{}, err
	}

	memberList, err := src.List()
	if err != nil {
		return Stats{}, fmt.Errorf("%w: %s: list: %v", ErrSource, src.Name(), err)
	}
	members := make(map[string]bool, len(memberList))
	for _, m := range memberList {
		members[m] = true
	}

	kind, _ := src.Origin()

	var stats Stats
	err = d.Tx(func(tx *db.Tx) error {
		// -- repo
		//
		// Order: bind.json's own RepoRef when set; else facts from the
		// binding's worktree; else facts from the source checkout it was
		// cut from (b.Repo), which still resolves after the worktree is
		// gc'd; else null. Applied for archive sources too -- an archived
		// binding's b.CWD is always gone, so the b.Repo fallback is its
		// only path to a repo row.
		ref := b.RepoRef
		if ref == nil && deps.Git != nil {
			ref = repoRefFromGit(ctx, deps.Git, b.CWD)
			if ref == nil && b.Repo != "" {
				ref = repoRefFromGit(ctx, deps.Git, b.Repo)
			}
		}
		var repoID *string
		if ref != nil && (ref.OriginURL != "" || ref.CommonDir != "") {
			id, rerr := tx.UpsertRepo(db.Repo{
				OriginURL: nonEmptyPtr(ref.OriginURL),
				CommonDir: nonEmptyPtr(ref.CommonDir),
			})
			if rerr != nil {
				return fmt.Errorf("upsert repo: %w", rerr)
			}
			repoID = &id
		}

		// -- planner
		var plannerID *string
		if b.Planner.SessionID != "" {
			id, perr := tx.UpsertPlanner(db.Planner{
				// b.PlannerID is the relay planner record's own id
				// (docs/specs/2026-09-22-drop-herdr-design.md §3.5). Nothing
				// writes the field yet -- step 1b sets it at bind/add/fork --
				// so today this is always "" and the natural-key path below
				// behaves exactly as before.
				ID:                b.PlannerID,
				HarnessKind:       b.Planner.Kind,
				SessionID:         b.Planner.SessionID,
				TranscriptLocator: nonEmptyPtr(b.Planner.TranscriptLocator),
			})
			if perr != nil {
				return fmt.Errorf("upsert planner: %w", perr)
			}
			plannerID = &id
		}

		// -- events: read log.jsonl past its cursor, decode the new lines,
		// and assemble "all" -- every entry this binding has ever logged --
		// from the db's own event rows plus the new lines, so round
		// derivation below sees the whole history even on an incremental
		// tick.
		var newEntries []store.LogEntry
		var newEntryLines [][]byte
		var logStartSeq int
		var logCursor db.Cursor
		if members["log.jsonl"] {
			opener, key := sourceOpener(src, "log.jsonl")
			cur, found, cerr := tx.Cursor(key)
			if cerr != nil {
				return fmt.Errorf("log cursor: %w", cerr)
			}
			lines, startSeq, next, reset, rerr := readAppendOnly(opener, key, cur, found)
			if rerr != nil {
				return fmt.Errorf("read log.jsonl: %w", rerr)
			}
			if reset {
				logger.Info("ingest: cursor reset", "binding", b.Name, "member", "log.jsonl")
			}
			logStartSeq = startSeq
			logCursor = next
			newEntryLines = lines
			for _, line := range lines {
				var e store.LogEntry
				if uerr := json.Unmarshal(line, &e); uerr != nil {
					logger.Warn("ingest: undecodable log entry", "binding", b.Name, "err", uerr)
					continue
				}
				newEntries = append(newEntries, e)
			}
		}

		// -- binding
		createdAt := b.CreatedAt
		if createdAt.IsZero() {
			if len(newEntries) > 0 {
				createdAt = newEntries[0].TS
			} else {
				createdAt = deps.Now()
			}
		}

		var forkedFromBindingID *string
		if b.ForkedFrom != "" {
			if fb, found, ferr := tx.Binding(b.ForkedFrom); ferr == nil && found {
				id := fb.ID
				forkedFromBindingID = &id
			}
		}
		var forkedFromRound *int
		if b.ForkedAtRound > 0 {
			n := b.ForkedAtRound
			forkedFromRound = &n
		}

		builderMode := string(b.Builder.Mode)
		if builderMode == "" {
			builderMode = string(store.ModePane)
		}

		var archivedAt *time.Time
		var archivePath *string
		if kind == "archive" {
			_, path := src.Origin()
			p := path
			archivePath = &p
			if ts, ok := src.(*tarSource); ok {
				if at, found := ts.ArchivedAt(); found {
					archivedAt = &at
				}
			}
		}

		bindingID, err := tx.UpsertBinding(db.Binding{
			Name:                b.Name,
			RepoID:              repoID,
			PlannerID:           plannerID,
			Feature:             nonEmptyPtr(b.Feature),
			ForkedFromBindingID: forkedFromBindingID,
			ForkedFromRound:     forkedFromRound,
			CWD:                 b.CWD,
			Worktree:            nonEmptyPtr(b.Worktree),
			Branch:              nonEmptyPtr(b.Branch),
			BaseCommit:          nonEmptyPtr(b.Base),
			Tier:                nonEmptyPtr(b.Tier),
			Gate:                nonEmptyPtr(b.Gate),
			BuilderMode:         builderMode,
			Server:              nonEmptyPtr(b.Builder.Server),
			CreatedAt:           createdAt,
			FinalState:          nonEmptyPtr(string(b.State)),
			ArchivedAt:          archivedAt,
			ArchivePath:         archivePath,
			IngestSource:        kind,
		})
		if err != nil {
			return fmt.Errorf("upsert binding: %w", err)
		}

		// Now that bindingID exists, assemble "all": every entry this
		// binding has ever logged. When the log cursor was fresh (no
		// saved cursor, or a rewrite reset it to 0), newEntries already
		// holds the complete file. Otherwise, fold in the db's own record
		// of earlier entries.
		all := newEntries
		if members["log.jsonl"] {
			if hadEvents, everr := tx.Events(bindingID, 0); everr == nil && len(hadEvents) > 0 && logStartSeq > 0 {
				merged := make([]store.LogEntry, 0, len(hadEvents)+len(newEntries))
				for _, ev := range hadEvents {
					var e store.LogEntry
					if uerr := json.Unmarshal([]byte(ev.EntryJSON), &e); uerr == nil {
						merged = append(merged, e)
					}
				}
				merged = append(merged, newEntries...)
				all = merged
			}

			if len(newEntryLines) > 0 {
				dbEvents := make([]db.Event, 0, len(newEntryLines))
				for i, line := range newEntryLines {
					var e store.LogEntry
					if json.Unmarshal(line, &e) != nil {
						continue
					}
					dbEvents = append(dbEvents, logEntryToEvent(bindingID, logStartSeq+i, string(line), e))
				}
				added, aerr := tx.AppendEvents(bindingID, dbEvents)
				if aerr != nil {
					return fmt.Errorf("append events: %w", aerr)
				}
				stats.Events += added
			}
			if err := tx.SaveCursor(logCursor); err != nil {
				return fmt.Errorf("save log cursor: %w", err)
			}
		}

		// -- rounds
		roundSet := map[int]bool{}
		for _, e := range all {
			if e.Round > 0 {
				roundSet[e.Round] = true
			}
		}
		for m := range members {
			if n, ok := roundFromMember(m); ok {
				roundSet[n] = true
			}
		}
		rounds := make([]int, 0, len(roundSet))
		for n := range roundSet {
			rounds = append(rounds, n)
		}
		sort.Ints(rounds)

		existingRounds, everr := tx.Rounds(bindingID)
		if everr != nil {
			return fmt.Errorf("existing rounds: %w", everr)
		}
		hadRound := make(map[int]bool, len(existingRounds))
		for _, r := range existingRounds {
			hadRound[r.Number] = true
		}

		now := deps.Now()

		roundIDs := make(map[int]string, len(rounds))

		for _, n := range rounds {
			r := roundFacts(all, n)
			r.BindingID = bindingID
			r.Number = n
			r.Outcome = deriveOutcome(all, n, b, members)
			r.Switches = switchesForRound(all, n)

			if cand, ref, ok := builderForRound(all, n, b); ok {
				candTok := cand
				r.BuilderCandidate = &candTok
				if ref.Harness != "" {
					h := ref.Harness
					r.BuilderHarness = &h
				}
				if ref.Provider != "" {
					p := ref.Provider
					r.BuilderProvider = &p
				}
				if ref.Model != "" {
					m := ref.Model
					r.BuilderModel = &m
				}
				mode := builderMode
				r.BuilderMode = &mode
			}

			roundID, rerr := tx.UpsertRound(r)
			if rerr != nil {
				return fmt.Errorf("upsert round %d: %w", n, rerr)
			}
			roundIDs[n] = roundID
			if !hadRound[n] {
				stats.Rounds++
			}

			// -- plain round-file artifacts
			for _, am := range roundArtifactMembers {
				member := am.base(n)
				if !members[member] {
					continue
				}
				changed, aerr := upsertArtifactIfChanged(tx, src, member, roundID, am.kind, nil, now)
				if aerr != nil {
					return fmt.Errorf("artifact %s: %w", member, aerr)
				}
				if changed {
					stats.Artifacts++
				}
			}

			// -- consult ask/findings artifacts
			for m := range members {
				if id, ok := consultMemberID(m, n, "-ask.md"); ok {
					cid := id
					changed, aerr := upsertArtifactIfChanged(tx, src, m, roundID, db.ArtifactAsk, &cid, now)
					if aerr != nil {
						return fmt.Errorf("artifact %s: %w", m, aerr)
					}
					if changed {
						stats.Artifacts++
					}
				}
				if id, ok := consultMemberID(m, n, "-findings.md"); ok {
					cid := id
					changed, aerr := upsertArtifactIfChanged(tx, src, m, roundID, db.ArtifactFindings, &cid, now)
					if aerr != nil {
						return fmt.Errorf("artifact %s: %w", m, aerr)
					}
					if changed {
						stats.Artifacts++
					}
				}
			}

			// -- answer artifact, from the log rather than a file
			if text, ok := lastAnswerPayload(all, n); ok {
				changed, aerr := upsertAnswerIfChanged(tx, roundID, text, now)
				if aerr != nil {
					return fmt.Errorf("artifact answer: %w", aerr)
				}
				if changed {
					stats.Artifacts++
				}
			}

			// -- transcript
			streamMember := builderStreamPathBase(n)
			logMember := builderLogPathBase(n)
			switch {
			case members[streamMember]:
				opener, key := sourceOpener(src, streamMember)
				cur, found, cerr := tx.Cursor(key)
				if cerr != nil {
					return fmt.Errorf("transcript cursor %s: %w", streamMember, cerr)
				}
				lines, startSeq, next, reset, rerr := readAppendOnly(opener, key, cur, found)
				if rerr != nil {
					return fmt.Errorf("read %s: %w", streamMember, rerr)
				}
				if reset {
					logger.Info("ingest: cursor reset", "binding", b.Name, "member", streamMember)
				}
				if len(lines) > 0 {
					recs, skipped := streamTranscriptRecords(b.Builder.Kind, lines, startSeq)
					added, terr := tx.AppendTranscript(db.OwnerRound, roundID, recs)
					if terr != nil {
						return fmt.Errorf("append transcript %s: %w", streamMember, terr)
					}
					stats.TranscriptRecords += added
					stats.Skipped += skipped
				}
				if err := tx.SaveCursor(next); err != nil {
					return fmt.Errorf("save transcript cursor %s: %w", streamMember, err)
				}
			case members[logMember]:
				opener, key := sourceOpener(src, logMember)
				cur, found, cerr := tx.Cursor(key)
				if cerr != nil {
					return fmt.Errorf("transcript cursor %s: %w", logMember, cerr)
				}
				lines, startSeq, next, reset, rerr := readAppendOnly(opener, key, cur, found)
				if rerr != nil {
					return fmt.Errorf("read %s: %w", logMember, rerr)
				}
				if reset {
					logger.Info("ingest: cursor reset", "binding", b.Name, "member", logMember)
				}
				if len(lines) > 0 {
					recs := logOnlyTranscriptRecords(lines, startSeq)
					added, terr := tx.AppendTranscript(db.OwnerRound, roundID, recs)
					if terr != nil {
						return fmt.Errorf("append transcript %s: %w", logMember, terr)
					}
					stats.TranscriptRecords += added
				}
				if err := tx.SaveCursor(next); err != nil {
					return fmt.Errorf("save transcript cursor %s: %w", logMember, err)
				}
			}
		}

		// -- link events to their rounds. Events are appended above before
		// any round row exists, so every event.round_id is null the moment
		// it is written; now that this tick's rounds all have ids, link
		// every still-unlinked event (from this tick or an earlier one that
		// predates this fix) whose decoded LogEntry.Round matches a round
		// number seen here.
		if err := linkEventsToRounds(tx, bindingID, roundIDs); err != nil {
			return fmt.Errorf("link events to rounds: %w", err)
		}

		// -- planner transcript
		if plannerID != nil && kind == "live" {
			locator := b.Planner.TranscriptLocator
			if locator == "" && deps.Sessions != nil {
				if p, ok := deps.Sessions(b.Planner.Kind, b.Planner.SessionID); ok {
					locator = p
				}
			}
			if locator != "" {
				if _, statErr := os.Stat(locator); statErr == nil {
					key := "planner::" + locator
					cur, found, cerr := tx.Cursor(key)
					if cerr != nil {
						return fmt.Errorf("planner transcript cursor: %w", cerr)
					}
					opener := func() (io.ReadCloser, error) { return os.Open(locator) }
					lines, startSeq, next, reset, rerr := readAppendOnly(opener, key, cur, found)
					if rerr != nil {
						return fmt.Errorf("read planner transcript: %w", rerr)
					}
					if reset {
						logger.Info("ingest: cursor reset", "binding", b.Name, "member", "planner transcript")
					}
					if len(lines) > 0 {
						recs := plannerTranscriptRecords(b.Planner.Kind, lines, startSeq)
						added, terr := tx.AppendTranscript(db.OwnerPlanner, *plannerID, recs)
						if terr != nil {
							return fmt.Errorf("append planner transcript: %w", terr)
						}
						stats.TranscriptRecords += added
					}
					if err := tx.SaveCursor(next); err != nil {
						return fmt.Errorf("save planner transcript cursor: %w", err)
					}
				}
			}
		}

		if stats.Rounds > 0 || stats.Events > 0 || stats.Artifacts > 0 || stats.TranscriptRecords > 0 {
			stats.Bindings = 1
		}

		return nil
	})
	if err != nil {
		return Stats{}, fmt.Errorf("ingest %s: %w", src.Name(), err)
	}

	return stats, nil
}

// logEntryToEvent projects a decoded LogEntry into a db.Event row, keeping
// entryJSON as the exact line read from the source.
func logEntryToEvent(bindingID string, seq int, entryJSON string, e store.LogEntry) db.Event {
	ev := db.Event{
		BindingID: bindingID,
		Seq:       seq,
		TS:        e.TS,
		Kind:      string(e.Kind),
		Direction: string(e.Direction),
		Note:      nonEmptyPtr(e.Note),
		Path:      nonEmptyPtr(e.Path),
		Confirmed: e.Confirmed,
		Late:      e.Late,
		EntryJSON: entryJSON,
	}
	if e.DeliveredAt != nil {
		t := *e.DeliveredAt
		ev.DeliveredAt = &t
	}
	if e.Flagged > 0 {
		f := e.Flagged
		ev.Flagged = &f
	}
	if e.FlaggedBy != "" {
		fb := e.FlaggedBy
		ev.FlaggedBy = &fb
	}
	return ev
}

// linkEventsToRounds sets round_id on every event of bindingID that is
// still unlinked, for every round number in roundIDs, by decoding each
// event's entry_json to recover the LogEntry.Round the live path filters
// on. It queries the db rather than relying on this tick's in-memory
// entries alone, so it also repairs events left unlinked by an ingest
// before this fix existed.
func linkEventsToRounds(tx *db.Tx, bindingID string, roundIDs map[int]string) error {
	if len(roundIDs) == 0 {
		return nil
	}

	events, err := tx.Events(bindingID, 0)
	if err != nil {
		return err
	}

	seqsByRound := make(map[int][]int)
	for _, e := range events {
		if e.RoundID != nil {
			continue
		}
		var entry store.LogEntry
		if json.Unmarshal([]byte(e.EntryJSON), &entry) != nil {
			continue
		}
		if entry.Round <= 0 {
			continue
		}
		seqsByRound[entry.Round] = append(seqsByRound[entry.Round], e.Seq)
	}

	for n, seqs := range seqsByRound {
		roundID, ok := roundIDs[n]
		if !ok {
			continue
		}
		if err := tx.LinkEvents(bindingID, roundID, seqs); err != nil {
			return err
		}
	}
	return nil
}

// lastAnswerPayload is the Payload of the last answer-kind entry for round
// n, when any exists.
func lastAnswerPayload(events []store.LogEntry, n int) (string, bool) {
	text, found := "", false
	for _, e := range events {
		if e.Round == n && e.Kind == store.KindAnswer {
			text, found = e.Payload, true
		}
	}
	return text, found
}

// upsertArtifactIfChanged reads member from src and upserts it as an
// artifact of kind (with consultID when non-nil) when its content differs
// from what a saved whole-file cursor recorded, or when there is no such
// cursor yet. It reports whether it wrote anything.
func upsertArtifactIfChanged(tx *db.Tx, src Source, member, roundID, kind string, consultID *string, now time.Time) (bool, error) {
	rc, _, err := src.Open(member)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return false, err
	}
	sha := sha256Hex(data)

	key := cursorSourceKey(src, member)
	cur, found, err := tx.Cursor(key)
	if err != nil {
		return false, err
	}
	if found && cur.WholeSHA != nil && *cur.WholeSHA == sha {
		return false, nil
	}

	if err := tx.UpsertArtifact(db.Artifact{
		RoundID:    roundID,
		Kind:       kind,
		ConsultID:  consultID,
		Text:       string(data),
		Bytes:      int64(len(data)),
		SHA256:     sha,
		CapturedAt: now,
	}); err != nil {
		return false, err
	}
	if err := tx.SaveCursor(db.Cursor{Source: key, WholeSHA: &sha, UpdatedAt: now}); err != nil {
		return false, err
	}
	return true, nil
}

// upsertAnswerIfChanged upserts round roundID's answer artifact (built
// from the log's Payload, not a file) when text differs from what is
// already recorded. It reports whether it wrote anything.
func upsertAnswerIfChanged(tx *db.Tx, roundID, text string, now time.Time) (bool, error) {
	existing, found, err := tx.Artifact(roundID, db.ArtifactAnswer)
	if err != nil {
		return false, err
	}
	if found && existing.Text == text {
		return false, nil
	}
	sha := sha256Hex([]byte(text))
	if err := tx.UpsertArtifact(db.Artifact{
		RoundID:    roundID,
		Kind:       db.ArtifactAnswer,
		Text:       text,
		Bytes:      int64(len(text)),
		SHA256:     sha,
		CapturedAt: now,
	}); err != nil {
		return false, err
	}
	return true, nil
}
