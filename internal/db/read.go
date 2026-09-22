package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Query builds one SELECT over round JOIN binding LEFT JOIN repo, with a
// WHERE clause assembled from the non-zero Filter fields, every value
// parameterised.
func (d *DB) Query(f Filter) ([]RoundRow, error) {
	return queryRounds(context.Background(), d.sqlDB, f)
}
func (t *Tx) Query(f Filter) ([]RoundRow, error) { return queryRounds(t.ctx, t.conn, f) }

func queryRounds(ctx context.Context, q queryer, f Filter) ([]RoundRow, error) {
	if f.Here != "" {
		return nil, fmt.Errorf("db: query: Here must be resolved: %w", ErrInvalid)
	}

	var where []string
	var args []any

	add := func(clause string, arg any) {
		where = append(where, clause)
		args = append(args, arg)
	}

	if f.Repo != "" {
		where = append(where, `(repo.origin_url = ? OR repo.common_dir = ?)`)
		args = append(args, f.Repo, f.Repo)
	}
	if f.Feature != "" {
		add(`binding.feature = ?`, f.Feature)
	}
	if f.Binding != "" {
		add(`binding.name = ?`, f.Binding)
	}
	if f.Planner != "" {
		add(`binding.planner_id IN (SELECT id FROM planner WHERE session_id = ?)`, f.Planner)
	}
	if f.Harness != "" {
		add(`round.builder_harness = ?`, f.Harness)
	}
	if f.Provider != "" {
		add(`round.builder_provider = ?`, f.Provider)
	}
	if f.Model != "" {
		add(`round.builder_model = ?`, f.Model)
	}
	if f.Candidate != "" {
		add(`round.builder_candidate = ?`, f.Candidate)
	}
	if f.Outcome != "" {
		add(`round.outcome = ?`, f.Outcome)
	}
	if f.ReportOutcome != "" {
		add(`round.report_outcome = ?`, f.ReportOutcome)
	}
	if f.GateResult != "" {
		add(`round.gate_result = ?`, f.GateResult)
	}
	if f.CostBasis != "" {
		add(`round.cost_basis = ?`, f.CostBasis)
	}
	if f.State != "" {
		add(`binding.final_state = ?`, f.State)
	}
	if f.Round != 0 {
		add(`round.number = ?`, f.Round)
	}
	if !f.Since.IsZero() {
		add(`round.started_at >= ?`, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		add(`round.started_at < ?`, formatTime(f.Until))
	}
	if f.Archived != nil {
		if *f.Archived {
			where = append(where, `binding.archived_at IS NOT NULL`)
		} else {
			where = append(where, `binding.archived_at IS NULL`)
		}
	}

	query := `SELECT round.binding_id, binding.name, repo.origin_url, repo.common_dir, binding.feature,
			round.number, round.started_at, round.closed_at, round.outcome,
			round.builder_candidate, round.builder_harness, round.builder_provider, round.builder_model,
			round.commits, round.tree, round.gate_result, round.cost_usd, round.cost_basis,
			round.in_tokens, round.cache_tokens, round.write_tokens, round.out_tokens,
			round.report_outcome, round.builder_mode, binding.server,
			binding.archived_at
		FROM round
		JOIN binding ON binding.id = round.binding_id
		LEFT JOIN repo ON repo.id = binding.repo_id`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	if f.Newest {
		query += ` ORDER BY round.started_at DESC`
	} else {
		query += ` ORDER BY round.started_at ASC`
	}
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query: %w", err)
	}
	defer rows.Close()

	var out []RoundRow
	for rows.Next() {
		row, err := scanRoundRow(rows)
		if err != nil {
			return nil, fmt.Errorf("db: query: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: query: %w", err)
	}
	return out, nil
}

func scanRoundRow(rows *sql.Rows) (RoundRow, error) {
	var row RoundRow
	var origin, commonDir, feature sql.NullString
	var startedAt string
	var closedAt sql.NullString
	var candidate, harness, provider, model sql.NullString
	var commits sql.NullInt64
	var tree, gateResult sql.NullString
	var costUSD sql.NullFloat64
	var costBasis sql.NullString
	var inTokens, cacheTokens, writeTokens, outTokens sql.NullInt64
	var reportOutcome, builderMode, server sql.NullString
	var archivedAt sql.NullString

	if err := rows.Scan(&row.BindingID, &row.BindingName, &origin, &commonDir, &feature,
		&row.Number, &startedAt, &closedAt, &row.Outcome,
		&candidate, &harness, &provider, &model,
		&commits, &tree, &gateResult, &costUSD, &costBasis,
		&inTokens, &cacheTokens, &writeTokens, &outTokens,
		&reportOutcome, &builderMode, &server,
		&archivedAt); err != nil {
		return RoundRow{}, fmt.Errorf("scan: %w", err)
	}

	st, err := parseTime(startedAt)
	if err != nil {
		return RoundRow{}, fmt.Errorf("parse started_at: %w", err)
	}
	row.StartedAt = st

	switch {
	case origin.Valid:
		v := origin.String
		row.Repo = &v
	case commonDir.Valid:
		v := commonDir.String
		row.Repo = &v
	}
	if feature.Valid {
		v := feature.String
		row.Feature = &v
	}
	if closedAt.Valid {
		ct, err := parseTime(closedAt.String)
		if err != nil {
			return RoundRow{}, fmt.Errorf("parse closed_at: %w", err)
		}
		row.ClosedAt = &ct
	}
	if row.ClosedAt != nil {
		ms := row.ClosedAt.Sub(row.StartedAt).Milliseconds()
		row.DurationMS = &ms
	}
	if candidate.Valid {
		v := candidate.String
		row.BuilderCandidate = &v
	}
	if harness.Valid {
		v := harness.String
		row.BuilderHarness = &v
	}
	if provider.Valid {
		v := provider.String
		row.BuilderProvider = &v
	}
	if model.Valid {
		v := model.String
		row.BuilderModel = &v
	}
	if commits.Valid {
		n := int(commits.Int64)
		row.Commits = &n
	}
	if tree.Valid {
		v := tree.String
		row.Tree = &v
	}
	if gateResult.Valid {
		v := gateResult.String
		row.GateResult = &v
	}
	if costUSD.Valid {
		v := costUSD.Float64
		row.CostUSD = &v
	}
	if costBasis.Valid {
		v := costBasis.String
		row.CostBasis = &v
	}
	if inTokens.Valid {
		v := inTokens.Int64
		row.InTokens = &v
	}
	if cacheTokens.Valid {
		v := cacheTokens.Int64
		row.CacheTokens = &v
	}
	if writeTokens.Valid {
		v := writeTokens.Int64
		row.WriteTokens = &v
	}
	if outTokens.Valid {
		v := outTokens.Int64
		row.OutTokens = &v
	}
	if reportOutcome.Valid {
		v := reportOutcome.String
		row.ReportOutcome = &v
	}
	if builderMode.Valid {
		v := builderMode.String
		row.BuilderMode = &v
	}
	if server.Valid {
		v := server.String
		row.Server = &v
	}
	if archivedAt.Valid {
		row.Archived = true
		at, err := parseTime(archivedAt.String)
		if err != nil {
			return RoundRow{}, fmt.Errorf("parse archived_at: %w", err)
		}
		row.ArchivedAt = &at
	}

	return row, nil
}

// bindingColumns is the column list shared by Bindings and Binding, kept in
// one place so both stay in sync with scanBindingRow.
const bindingColumns = `binding.id, binding.name, binding.repo_id, binding.planner_id, binding.feature,
	binding.forked_from_binding_id, binding.forked_from_round, binding.cwd, binding.worktree,
	binding.branch, binding.base_commit, binding.tier, binding.gate, binding.builder_mode,
	binding.server, binding.created_at, binding.final_state, binding.archived_at, binding.archive_path,
	binding.ingest_source, repo.origin_url, repo.common_dir,
	(SELECT COUNT(*) FROM round WHERE round.binding_id = binding.id),
	COALESCE((SELECT MAX(round.started_at) FROM round WHERE round.binding_id = binding.id), binding.created_at) AS last_activity`

func scanBindingRow(rows *sql.Rows) (BindingRow, error) {
	var br BindingRow
	var repoID, plannerID, feature, forkedFromBindingID sql.NullString
	var forkedFromRound sql.NullInt64
	var worktree, branch, baseCommit, tier, gate, server sql.NullString
	var createdAt string
	var finalState, archivedAt, archivePath sql.NullString
	var originURL, commonDir sql.NullString
	var lastActivity string

	if err := rows.Scan(&br.ID, &br.Name, &repoID, &plannerID, &feature,
		&forkedFromBindingID, &forkedFromRound, &br.CWD, &worktree,
		&branch, &baseCommit, &tier, &gate, &br.BuilderMode,
		&server, &createdAt, &finalState, &archivedAt, &archivePath,
		&br.IngestSource, &originURL, &commonDir,
		&br.Rounds, &lastActivity); err != nil {
		return BindingRow{}, fmt.Errorf("scan: %w", err)
	}

	if repoID.Valid {
		br.RepoID = &repoID.String
	}
	if plannerID.Valid {
		br.PlannerID = &plannerID.String
	}
	if feature.Valid {
		br.Feature = &feature.String
	}
	if forkedFromBindingID.Valid {
		br.ForkedFromBindingID = &forkedFromBindingID.String
	}
	if forkedFromRound.Valid {
		n := int(forkedFromRound.Int64)
		br.ForkedFromRound = &n
	}
	if worktree.Valid {
		br.Worktree = &worktree.String
	}
	if branch.Valid {
		br.Branch = &branch.String
	}
	if baseCommit.Valid {
		br.BaseCommit = &baseCommit.String
	}
	if tier.Valid {
		br.Tier = &tier.String
	}
	if gate.Valid {
		br.Gate = &gate.String
	}
	if server.Valid {
		br.Server = &server.String
	}
	ct, err := parseTime(createdAt)
	if err != nil {
		return BindingRow{}, fmt.Errorf("parse created_at: %w", err)
	}
	br.CreatedAt = ct
	if finalState.Valid {
		br.FinalState = &finalState.String
	}
	if archivedAt.Valid {
		at, err := parseTime(archivedAt.String)
		if err != nil {
			return BindingRow{}, fmt.Errorf("parse archived_at: %w", err)
		}
		br.ArchivedAt = &at
	}
	if archivePath.Valid {
		br.ArchivePath = &archivePath.String
	}
	if originURL.Valid {
		br.RepoOrigin = &originURL.String
	}
	if commonDir.Valid {
		br.RepoCommonDir = &commonDir.String
	}
	la, err := parseTime(lastActivity)
	if err != nil {
		return BindingRow{}, fmt.Errorf("parse last_activity: %w", err)
	}
	br.LastActivity = la

	return br, nil
}

// Bindings lists bindings matching the binding-scoped fields of f (Repo,
// Feature, Binding, Planner, State, Archived), newest LastActivity first.
func (d *DB) Bindings(f Filter) ([]BindingRow, error) {
	return queryBindings(context.Background(), d.sqlDB, f)
}
func (t *Tx) Bindings(f Filter) ([]BindingRow, error) { return queryBindings(t.ctx, t.conn, f) }

func queryBindings(ctx context.Context, q queryer, f Filter) ([]BindingRow, error) {
	if f.Here != "" {
		return nil, fmt.Errorf("db: bindings: Here must be resolved: %w", ErrInvalid)
	}

	var where []string
	var args []any

	if f.Repo != "" {
		where = append(where, `(repo.origin_url = ? OR repo.common_dir = ?)`)
		args = append(args, f.Repo, f.Repo)
	}
	if f.Feature != "" {
		where = append(where, `binding.feature = ?`)
		args = append(args, f.Feature)
	}
	if f.Binding != "" {
		where = append(where, `binding.name = ?`)
		args = append(args, f.Binding)
	}
	if f.Planner != "" {
		where = append(where, `binding.planner_id IN (SELECT id FROM planner WHERE session_id = ?)`)
		args = append(args, f.Planner)
	}
	if f.State != "" {
		where = append(where, `binding.final_state = ?`)
		args = append(args, f.State)
	}
	if f.Archived != nil {
		if *f.Archived {
			where = append(where, `binding.archived_at IS NOT NULL`)
		} else {
			where = append(where, `binding.archived_at IS NULL`)
		}
	}

	query := `SELECT ` + bindingColumns + `
		FROM binding
		LEFT JOIN repo ON repo.id = binding.repo_id`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY last_activity DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: bindings: %w", err)
	}
	defer rows.Close()

	var out []BindingRow
	for rows.Next() {
		br, err := scanBindingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("db: bindings: %w", err)
		}
		out = append(out, br)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: bindings: %w", err)
	}
	return out, nil
}

// Binding returns the newest (by created_at) binding with the given name.
func (d *DB) Binding(name string) (BindingRow, bool, error) {
	return getBinding(context.Background(), d.sqlDB, name)
}
func (t *Tx) Binding(name string) (BindingRow, bool, error) { return getBinding(t.ctx, t.conn, name) }

func getBinding(ctx context.Context, q queryer, name string) (BindingRow, bool, error) {
	query := `SELECT ` + bindingColumns + `
		FROM binding
		LEFT JOIN repo ON repo.id = binding.repo_id
		WHERE binding.name = ?
		ORDER BY binding.created_at DESC
		LIMIT 1`

	rows, err := q.QueryContext(ctx, query, name)
	if err != nil {
		return BindingRow{}, false, fmt.Errorf("db: binding: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return BindingRow{}, false, fmt.Errorf("db: binding: %w", err)
		}
		return BindingRow{}, false, nil
	}

	br, err := scanBindingRow(rows)
	if err != nil {
		return BindingRow{}, false, fmt.Errorf("db: binding: %w", err)
	}
	return br, true, nil
}

// PlannerBySession returns the planner row for one (harness_kind, session_id),
// false when there is none. It is what `relay planner init` reads to reuse an
// existing db row's id instead of minting one (docs/specs/2026-09-22-drop-herdr-design.md
// §3.5) -- an error here is not a missing planner, so the two outcomes are
// reported separately.
func (d *DB) PlannerBySession(kind, session string) (Planner, bool, error) {
	return plannerBySession(context.Background(), d.sqlDB, kind, session)
}

func plannerBySession(ctx context.Context, q queryer, kind, session string) (Planner, bool, error) {
	var (
		p         Planner
		locator   sql.NullString
		firstSeen string
		lastSeen  string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, harness_kind, session_id, transcript_locator, first_seen, last_seen
		   FROM planner WHERE harness_kind = ? AND session_id = ?`,
		kind, session).Scan(&p.ID, &p.HarnessKind, &p.SessionID, &locator, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Planner{}, false, nil
	}
	if err != nil {
		return Planner{}, false, fmt.Errorf("db: planner by session: %w", err)
	}

	if locator.Valid {
		p.TranscriptLocator = &locator.String
	}
	if p.FirstSeen, err = parseTime(firstSeen); err != nil {
		return Planner{}, false, fmt.Errorf("db: planner by session: parse first_seen: %w", err)
	}
	if p.LastSeen, err = parseTime(lastSeen); err != nil {
		return Planner{}, false, fmt.Errorf("db: planner by session: parse last_seen: %w", err)
	}
	return p, true, nil
}

const roundColumns = `id, binding_id, number, started_at, closed_at, outcome,
	builder_candidate, builder_harness, builder_provider, builder_model, builder_mode, tier,
	commits, tree, gate_result, gate_exit, gate_duration_ms,
	in_tokens, cache_tokens, write_tokens, out_tokens, cost_usd, cost_basis,
	report_outcome, switches`

func scanRound(rows *sql.Rows) (Round, error) {
	var r Round
	var startedAt string
	var closedAt sql.NullString
	var candidate, harness, provider, model, mode, tier sql.NullString
	var commits sql.NullInt64
	var tree, gateResult sql.NullString
	var gateExit sql.NullInt64
	var gateDurationMS sql.NullInt64
	var inTokens, cacheTokens, writeTokens, outTokens sql.NullInt64
	var costUSD sql.NullFloat64
	var costBasis, reportOutcome sql.NullString

	if err := rows.Scan(&r.ID, &r.BindingID, &r.Number, &startedAt, &closedAt, &r.Outcome,
		&candidate, &harness, &provider, &model, &mode, &tier,
		&commits, &tree, &gateResult, &gateExit, &gateDurationMS,
		&inTokens, &cacheTokens, &writeTokens, &outTokens, &costUSD, &costBasis,
		&reportOutcome, &r.Switches); err != nil {
		return Round{}, fmt.Errorf("scan: %w", err)
	}

	st, err := parseTime(startedAt)
	if err != nil {
		return Round{}, fmt.Errorf("parse started_at: %w", err)
	}
	r.StartedAt = st

	if closedAt.Valid {
		ct, err := parseTime(closedAt.String)
		if err != nil {
			return Round{}, fmt.Errorf("parse closed_at: %w", err)
		}
		r.ClosedAt = &ct
	}
	if candidate.Valid {
		r.BuilderCandidate = &candidate.String
	}
	if harness.Valid {
		r.BuilderHarness = &harness.String
	}
	if provider.Valid {
		r.BuilderProvider = &provider.String
	}
	if model.Valid {
		r.BuilderModel = &model.String
	}
	if mode.Valid {
		r.BuilderMode = &mode.String
	}
	if tier.Valid {
		r.Tier = &tier.String
	}
	if commits.Valid {
		n := int(commits.Int64)
		r.Commits = &n
	}
	if tree.Valid {
		r.Tree = &tree.String
	}
	if gateResult.Valid {
		r.GateResult = &gateResult.String
	}
	if gateExit.Valid {
		n := int(gateExit.Int64)
		r.GateExit = &n
	}
	if gateDurationMS.Valid {
		r.GateDurationMS = &gateDurationMS.Int64
	}
	if inTokens.Valid {
		r.InTokens = &inTokens.Int64
	}
	if cacheTokens.Valid {
		r.CacheTokens = &cacheTokens.Int64
	}
	if writeTokens.Valid {
		r.WriteTokens = &writeTokens.Int64
	}
	if outTokens.Valid {
		r.OutTokens = &outTokens.Int64
	}
	if costUSD.Valid {
		r.CostUSD = &costUSD.Float64
	}
	if costBasis.Valid {
		r.CostBasis = &costBasis.String
	}
	if reportOutcome.Valid {
		r.ReportOutcome = &reportOutcome.String
	}

	return r, nil
}

// Rounds returns every round of bindingID, by number ascending.
func (d *DB) Rounds(bindingID string) ([]Round, error) {
	return listRounds(context.Background(), d.sqlDB, bindingID)
}
func (t *Tx) Rounds(bindingID string) ([]Round, error) { return listRounds(t.ctx, t.conn, bindingID) }

func listRounds(ctx context.Context, q queryer, bindingID string) ([]Round, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+roundColumns+` FROM round WHERE binding_id = ? ORDER BY number ASC`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("db: rounds: %w", err)
	}
	defer rows.Close()

	var out []Round
	for rows.Next() {
		r, err := scanRound(rows)
		if err != nil {
			return nil, fmt.Errorf("db: rounds: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rounds: %w", err)
	}
	return out, nil
}

// Artifact returns the artifact for (roundID, kind) with no consult id.
func (d *DB) Artifact(roundID, kind string) (Artifact, bool, error) {
	return getArtifact(context.Background(), d.sqlDB, roundID, kind)
}
func (t *Tx) Artifact(roundID, kind string) (Artifact, bool, error) {
	return getArtifact(t.ctx, t.conn, roundID, kind)
}

func getArtifact(ctx context.Context, q queryer, roundID, kind string) (Artifact, bool, error) {
	var a Artifact
	var capturedAt string
	err := q.QueryRowContext(ctx, `SELECT id, round_id, kind, text, bytes, sha256, captured_at
		FROM artifact WHERE round_id = ? AND kind = ? AND consult_id = ''`, roundID, kind).
		Scan(&a.ID, &a.RoundID, &a.Kind, &a.Text, &a.Bytes, &a.SHA256, &capturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, fmt.Errorf("db: artifact: %w", err)
	}
	ct, err := parseTime(capturedAt)
	if err != nil {
		return Artifact{}, false, fmt.Errorf("db: artifact: parse captured_at: %w", err)
	}
	a.CapturedAt = ct
	return a, true, nil
}

// Transcript pages owner's records with seq >= fromSeq, ascending, up to
// limit records (0 = no limit).
func (d *DB) Transcript(ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	return listTranscript(context.Background(), d.sqlDB, ownerKind, ownerID, fromSeq, limit)
}
func (t *Tx) Transcript(ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	return listTranscript(t.ctx, t.conn, ownerKind, ownerID, fromSeq, limit)
}

func listTranscript(ctx context.Context, q queryer, ownerKind, ownerID string, fromSeq, limit int) ([]TranscriptRecord, error) {
	query := `SELECT id, owner_kind, owner_id, seq, ts, record_json, rendered
		FROM transcript WHERE owner_kind = ? AND owner_id = ? AND seq >= ? ORDER BY seq ASC`
	args := []any{ownerKind, ownerID, fromSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: transcript: %w", err)
	}
	defer rows.Close()

	var out []TranscriptRecord
	for rows.Next() {
		var r TranscriptRecord
		var ts sql.NullString
		if err := rows.Scan(&r.ID, &r.OwnerKind, &r.OwnerID, &r.Seq, &ts, &r.RecordJSON, &r.Rendered); err != nil {
			return nil, fmt.Errorf("db: transcript: scan: %w", err)
		}
		if ts.Valid {
			t, err := parseTime(ts.String)
			if err != nil {
				return nil, fmt.Errorf("db: transcript: parse ts: %w", err)
			}
			r.TS = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: transcript: %w", err)
	}
	return out, nil
}

// Events returns bindingID's events, restricted to round when it is
// non-zero (0 means every round), ordered by seq.
func (d *DB) Events(bindingID string, round int) ([]Event, error) {
	return listEvents(context.Background(), d.sqlDB, bindingID, round)
}
func (t *Tx) Events(bindingID string, round int) ([]Event, error) {
	return listEvents(t.ctx, t.conn, bindingID, round)
}

func listEvents(ctx context.Context, q queryer, bindingID string, round int) ([]Event, error) {
	query := `SELECT id, binding_id, round_id, seq, ts, kind, direction, note, path, delivered_at,
			confirmed, late, flagged, flagged_by, entry_json
		FROM event WHERE binding_id = ?`
	args := []any{bindingID}
	if round != 0 {
		query += ` AND round_id IN (SELECT id FROM round WHERE binding_id = ? AND number = ?)`
		args = append(args, bindingID, round)
	}
	query += ` ORDER BY seq ASC`

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("db: events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: events: %w", err)
	}
	return out, nil
}

func scanEvent(rows *sql.Rows) (Event, error) {
	var e Event
	var roundID sql.NullString
	var ts string
	var note, path sql.NullString
	var deliveredAt sql.NullString
	var confirmed, late int
	var flagged sql.NullInt64
	var flaggedBy sql.NullString

	if err := rows.Scan(&e.ID, &e.BindingID, &roundID, &e.Seq, &ts, &e.Kind, &e.Direction, &note, &path, &deliveredAt,
		&confirmed, &late, &flagged, &flaggedBy, &e.EntryJSON); err != nil {
		return Event{}, fmt.Errorf("scan: %w", err)
	}

	t, err := parseTime(ts)
	if err != nil {
		return Event{}, fmt.Errorf("parse ts: %w", err)
	}
	e.TS = t

	if roundID.Valid {
		e.RoundID = &roundID.String
	}
	if note.Valid {
		e.Note = &note.String
	}
	if path.Valid {
		e.Path = &path.String
	}
	if deliveredAt.Valid {
		dt, err := parseTime(deliveredAt.String)
		if err != nil {
			return Event{}, fmt.Errorf("parse delivered_at: %w", err)
		}
		e.DeliveredAt = &dt
	}
	e.Confirmed = confirmed != 0
	e.Late = late != 0
	if flagged.Valid {
		n := int(flagged.Int64)
		e.Flagged = &n
	}
	if flaggedBy.Valid {
		e.FlaggedBy = &flaggedBy.String
	}

	return e, nil
}

// Cursor returns the saved cursor for source.
func (d *DB) Cursor(source string) (Cursor, bool, error) {
	return getCursor(context.Background(), d.sqlDB, source)
}
func (t *Tx) Cursor(source string) (Cursor, bool, error) { return getCursor(t.ctx, t.conn, source) }

func getCursor(ctx context.Context, q queryer, source string) (Cursor, bool, error) {
	var c Cursor
	var wholeSHA sql.NullString
	var updatedAt string
	err := q.QueryRowContext(ctx, `SELECT source, byte_offset, head_sha, whole_sha, updated_at
		FROM ingest_cursor WHERE source = ?`, source).
		Scan(&c.Source, &c.ByteOffset, &c.HeadSHA, &wholeSHA, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, fmt.Errorf("db: cursor: %w", err)
	}
	if wholeSHA.Valid {
		c.WholeSHA = &wholeSHA.String
	}
	ua, err := parseTime(updatedAt)
	if err != nil {
		return Cursor{}, false, fmt.Errorf("db: cursor: parse updated_at: %w", err)
	}
	c.UpdatedAt = ua
	return c, true, nil
}

// statsTables is the fixed set of tables Stats counts rows in.
var statsTables = []string{"repo", "planner", "binding", "round", "event", "artifact", "transcript", "ingest_cursor"}

// Stats summarises row counts per table, the db's on-disk size, its schema
// version, and the newest round's started_at.
func (d *DB) Stats() (Stats, error) {
	rowsMap := make(map[string]int, len(statsTables))
	for _, tbl := range statsTables {
		var n int
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			return Stats{}, fmt.Errorf("db: stats: count %s: %w", tbl, err)
		}
		rowsMap[tbl] = n
	}

	version, err := d.Version()
	if err != nil {
		return Stats{}, fmt.Errorf("db: stats: %w", err)
	}

	var pageCount, pageSize int64
	if err := d.sqlDB.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		return Stats{}, fmt.Errorf("db: stats: page_count: %w", err)
	}
	if err := d.sqlDB.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return Stats{}, fmt.Errorf("db: stats: page_size: %w", err)
	}

	var newest sql.NullString
	if err := d.sqlDB.QueryRow(`SELECT MAX(started_at) FROM round`).Scan(&newest); err != nil {
		return Stats{}, fmt.Errorf("db: stats: newest round: %w", err)
	}
	var newestRound *time.Time
	if newest.Valid {
		nt, err := parseTime(newest.String)
		if err != nil {
			return Stats{}, fmt.Errorf("db: stats: parse newest round: %w", err)
		}
		newestRound = &nt
	}

	return Stats{
		Version:     version,
		SizeBytes:   pageCount * pageSize,
		Rows:        rowsMap,
		NewestRound: newestRound,
	}, nil
}
