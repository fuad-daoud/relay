package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Record is one binding's authoritative row in binding_record: the whole
// Binding as JSON (JSON), plus the columns internal/store reads and sorts on.
//
// It is not the ingest mirror's Binding in types.go: that one is a projection
// of the same state for history queries, fed by internal/ingest. This is the
// system of record behind the unchanged store.Store API.
type Record struct {
	ID        string
	Owner     string
	Name      string
	State     string
	Round     int
	CWD       string
	JSON      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// ViewedAt replaces the <binding dir>/.viewed sidecar; nil when the
	// binding has never been viewed.
	ViewedAt *time.Time
	// ArchivedAt is set by RecordArchive. An archived row is invisible to
	// RecordGet and RecordList, exactly as an archived binding is to Load and
	// List.
	ArchivedAt *time.Time
}

// RecordEvent is one entry of a binding_record's log: the columns LogEntry
// promotes, plus the entry's JSON object in JSON. JSON is authoritative for the
// entry's content, so a key a newer relevo wrote survives a read and a confirm
// (P3a plan §3.1).
type RecordEvent struct {
	Seq         int
	TS          time.Time
	Round       int
	Direction   string
	Kind        string
	Confirmed   bool
	DeliveredAt *time.Time
	Route       string
	JSON        string
}

// recordCols is the binding_record column list every reader selects, in the
// order scanRecord scans.
const recordCols = `id, owner, name, state, round, cwd, record_json, created_at, updated_at, viewed_at, archived_at`

// nullIfEmpty stores an optional TEXT column: an empty string becomes NULL, so
// "never set" is distinguishable from a stored empty string.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullTimeFrom parses an optional RFC3339 text column into a *time.Time.
func nullTimeFrom(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// rowScanner is the Scan half of *sql.Row every reader here shares.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(s rowScanner) (Record, error) {
	var r Record
	var createdAt, updatedAt string
	var viewedAt, archivedAt sql.NullString

	if err := s.Scan(&r.ID, &r.Owner, &r.Name, &r.State, &r.Round, &r.CWD, &r.JSON,
		&createdAt, &updatedAt, &viewedAt, &archivedAt); err != nil {
		return Record{}, err
	}

	var err error
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return Record{}, fmt.Errorf("parse created_at: %w", err)
	}
	if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Record{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if r.ViewedAt, err = nullTimeFrom(viewedAt); err != nil {
		return Record{}, fmt.Errorf("parse viewed_at: %w", err)
	}
	if r.ArchivedAt, err = nullTimeFrom(archivedAt); err != nil {
		return Record{}, fmt.Errorf("parse archived_at: %w", err)
	}

	return r, nil
}

// RecordGet returns owner's live row for name, and whether it was found.
//
// The live row is keyed by (owner, name) (P5 §4.1): the one machine database
// holds every owner's bindings, and the local store's owner is "". load()
// addresses a binding by name within its own store's owner.
func (d *DB) RecordGet(owner, name string) (Record, bool, error) {
	return getRecord(context.Background(), d.sqlDB, owner, name)
}

func getRecord(ctx context.Context, q queryer, owner, name string) (Record, bool, error) {
	r, err := scanRecord(q.QueryRowContext(ctx,
		`SELECT `+recordCols+` FROM binding_record WHERE owner = ? AND name = ? AND archived_at IS NULL`, owner, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get %s/%q: %w", owner, name, mapBusy(err))
	}
	return r, true, nil
}

// RecordList returns owner's every live row, ordered by name. Today's
// file-based list reads the root with ReadDir, which is also name order.
func (d *DB) RecordList(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE owner = ? AND archived_at IS NULL ORDER BY name ASC`, owner)
	if err != nil {
		return nil, fmt.Errorf("db: record list: %w", mapBusy(err))
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("db: record list: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: record list: %w", mapBusy(err))
	}
	return out, nil
}

// RecordListArchived returns every archived row, oldest archived_at first and
// by name within one stamp. A record archived by RecordArchive is invisible to
// RecordGet and RecordList; this is the reader that brings it back for
// `relevo tab`, `relevo stats` and the daemon's mirror feed (P3d §4.1).
func (d *DB) RecordListArchived(owner string) ([]Record, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE owner = ? AND archived_at IS NOT NULL
			ORDER BY archived_at ASC, name ASC`, owner)
	if err != nil {
		return nil, fmt.Errorf("db: record list archived: %w", mapBusy(err))
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("db: record list archived: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: record list archived: %w", mapBusy(err))
	}
	return out, nil
}

// RecordGetByID returns the row with this id, live or archived, and whether it
// was found. It is how the archive source reads the record a tarball import or
// a gc archive minted, when the caller holds the record id rather than the
// name (P3d §4.5).
func (d *DB) RecordGetByID(id string) (Record, bool, error) {
	r, err := scanRecord(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get by id %q: %w", id, mapBusy(err))
	}
	return r, true, nil
}

// RecordCounts returns how many binding_record rows are live and how many are
// archived. It is what doctor's `database` row prints (P3d §4.7).
func (d *DB) RecordCounts() (live, archived int, err error) {
	var total, liveCount int
	if err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN archived_at IS NULL THEN 1 ELSE 0 END), 0)
			FROM binding_record`).Scan(&total, &liveCount); err != nil {
		return 0, 0, fmt.Errorf("db: record counts: %w", mapBusy(err))
	}
	return liveCount, total - liveCount, nil
}

// RecordGetArchivedByName returns the most recently archived row for name,
// and whether one exists. A name may be archived more than once -- the name is
// reused after each archive -- and this is the one whose sealed round files a
// path under the old binding directory names (internal/store.sealedLookup,
// P3d §4.1). Two archives of one name can share a millisecond archived_at, and a
// ULID is random within a millisecond, so the tie-break is rowid: insertion
// order, i.e. the later archive.
func (d *DB) RecordGetArchivedByName(owner, name string) (Record, bool, error) {
	r, err := scanRecord(d.sqlDB.QueryRowContext(context.Background(),
		`SELECT `+recordCols+` FROM binding_record
			WHERE owner = ? AND name = ? AND archived_at IS NOT NULL
			ORDER BY archived_at DESC, rowid DESC LIMIT 1`, owner, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("db: record get archived %s/%q: %w", owner, name, mapBusy(err))
	}
	return r, true, nil
}

// RecordPutArchived inserts one archived row directly, with archived_at set:
// the tarball import (§4.2) mints a record that was never live. RecordPut is
// the wrong tool on a name a live binding already holds -- its lookup is
// `WHERE name = ? AND archived_at IS NULL`, so it would update that live row
// and RecordArchive would then archive it.
func (t *Tx) RecordPutArchived(r Record, at time.Time) (string, error) {
	id := r.ID
	if id == "" {
		id = NewID()
	}
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = at
	}
	updatedAt := r.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = at
	}
	if _, err := t.exec(`INSERT INTO binding_record
			(id, owner, name, state, round, cwd, record_json, created_at, updated_at, viewed_at, archived_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, r.Owner, r.Name, r.State, r.Round, r.CWD, r.JSON,
		formatTime(createdAt), formatTime(updatedAt), nullableTime(r.ViewedAt), formatTime(at)); err != nil {
		return "", fmt.Errorf("db: record put archived %q: %w", r.Name, mapBusy(err))
	}
	return id, nil
}

// RecordPut inserts or updates the live row for r.Owner and r.Name. A hit
// keeps the row's id and its viewed_at -- and its created_at, which is the
// binding's own creation stamp, not this write's -- and takes r's owner with
// the other columns. The live row is keyed by (owner, name) (P5 §4.1), so a
// store never writes a row outside its own scope, and the same name may be
// live under a different owner. A miss mints a ULID unless r.ID is set.
func (t *Tx) RecordPut(r Record) (string, error) {
	updatedAt := r.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	var id string
	err := t.queryRow(`SELECT id FROM binding_record WHERE owner = ? AND name = ? AND archived_at IS NULL`,
		r.Owner, r.Name).Scan(&id)
	switch {
	case err == nil:
		if _, uerr := t.exec(`UPDATE binding_record SET owner = ?, state = ?, round = ?, cwd = ?, record_json = ?, updated_at = ? WHERE id = ?`,
			r.Owner, r.State, r.Round, r.CWD, r.JSON, formatTime(updatedAt), id); uerr != nil {
			return "", fmt.Errorf("db: record put %q: update: %w", r.Name, mapBusy(uerr))
		}
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		id = r.ID
		if id == "" {
			id = NewID()
		}
		createdAt := r.CreatedAt
		if createdAt.IsZero() {
			createdAt = updatedAt
		}
		if _, ierr := t.exec(`INSERT INTO binding_record
				(id, owner, name, state, round, cwd, record_json, created_at, updated_at, viewed_at, archived_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,NULL)`,
			id, r.Owner, r.Name, r.State, r.Round, r.CWD, r.JSON,
			formatTime(createdAt), formatTime(updatedAt), nullableTime(r.ViewedAt)); ierr != nil {
			return "", fmt.Errorf("db: record put %q: insert: %w", r.Name, mapBusy(ierr))
		}
		return id, nil
	default:
		return "", fmt.Errorf("db: record put %q: select: %w", r.Name, mapBusy(err))
	}
}

// RecordArchive stamps archived_at on owner's live row for name, which takes
// it out of RecordGet and RecordList. The live row is keyed by (owner, name)
// (P5 §4.1). No live row is a no-op: the caller load()s first, so a missing
// row there is already an error.
func (t *Tx) RecordArchive(owner, name string, at time.Time) error {
	if _, err := t.exec(`UPDATE binding_record SET archived_at = ? WHERE owner = ? AND name = ? AND archived_at IS NULL`,
		formatTime(at), owner, name); err != nil {
		return fmt.Errorf("db: record archive %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// RecordDelete removes owner's live row for name; its events go with it through
// the foreign key's ON DELETE CASCADE. The live row is keyed by (owner, name)
// (P5 §4.1). Archived rows are kept: they are the record of a name that was
// already archived and freed, so deleting a later binding of the same name must
// not destroy that history.
func (t *Tx) RecordDelete(owner, name string) error {
	if _, err := t.exec(`DELETE FROM binding_record WHERE owner = ? AND name = ? AND archived_at IS NULL`,
		owner, name); err != nil {
		return fmt.Errorf("db: record delete %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// RecordSetViewed stamps viewed_at on owner's live row for name, which is keyed
// by (owner, name) (P5 §4.1). No row is a no-op: a read verb must not fail
// because a stamp could not be written.
func (t *Tx) RecordSetViewed(owner, name string, at time.Time) error {
	if _, err := t.exec(`UPDATE binding_record SET viewed_at = ? WHERE owner = ? AND name = ? AND archived_at IS NULL`,
		formatTime(at), owner, name); err != nil {
		return fmt.Errorf("db: record set viewed %s/%q: %w", owner, name, mapBusy(err))
	}
	return nil
}

// EventsOf returns recordID's events whose Seq is greater than afterSeq,
// ascending. afterSeq 0 reads the whole log; readLogAfter passes the cursor.
func (d *DB) EventsOf(recordID string, afterSeq int) ([]RecordEvent, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT seq, ts, round, direction, kind, confirmed, delivered_at, route, entry_json
			FROM binding_event WHERE record_id = ? AND seq > ? ORDER BY seq ASC`,
		recordID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("db: events of %s: %w", recordID, mapBusy(err))
	}
	defer rows.Close()

	var out []RecordEvent
	for rows.Next() {
		e, err := scanRecordEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("db: events of %s: %w", recordID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: events of %s: %w", recordID, mapBusy(err))
	}
	return out, nil
}

func scanRecordEvent(rows *sql.Rows) (RecordEvent, error) {
	var e RecordEvent
	var ts string
	var confirmed int
	var deliveredAt sql.NullString
	var route sql.NullString

	if err := rows.Scan(&e.Seq, &ts, &e.Round, &e.Direction, &e.Kind, &confirmed,
		&deliveredAt, &route, &e.JSON); err != nil {
		return RecordEvent{}, fmt.Errorf("scan: %w", err)
	}

	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return RecordEvent{}, fmt.Errorf("parse ts: %w", err)
	}
	e.Confirmed = confirmed != 0
	if e.DeliveredAt, err = nullTimeFrom(deliveredAt); err != nil {
		return RecordEvent{}, fmt.Errorf("parse delivered_at: %w", err)
	}
	e.Route = route.String

	return e, nil
}

// EventAppend inserts one event. (record_id, seq) is the primary key, so
// appending an existing seq fails rather than duplicating a line.
func (t *Tx) EventAppend(recordID string, e RecordEvent) error {
	if _, err := t.exec(`INSERT INTO binding_event
			(record_id, seq, ts, round, direction, kind, confirmed, delivered_at, route, entry_json)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		recordID, e.Seq, formatTime(e.TS), e.Round, e.Direction, e.Kind,
		boolToInt(e.Confirmed), nullableTime(e.DeliveredAt), nullIfEmpty(e.Route), e.JSON); err != nil {
		return fmt.Errorf("db: event append %s#%d: %w", recordID, e.Seq, mapBusy(err))
	}
	return nil
}

// EventReplaceAll replaces recordID's entire log with evs, in one transaction:
// import-on-presence adopts a waiting log.jsonl this way, and a failed import
// must not leave half of a previous log's events behind.
func (t *Tx) EventReplaceAll(recordID string, evs []RecordEvent) error {
	if _, err := t.exec(`DELETE FROM binding_event WHERE record_id = ?`, recordID); err != nil {
		return fmt.Errorf("db: event replace %s: delete: %w", recordID, mapBusy(err))
	}
	for _, e := range evs {
		if err := t.EventAppend(recordID, e); err != nil {
			return err
		}
	}
	return nil
}

// EventConfirm marks recordID's seq'th event delivered now, by route when that
// is non-empty, and replaces the entry's JSON with newJSON -- the caller's
// patched map, so unknown keys survive. A seq with no row is a no-op.
func (t *Tx) EventConfirm(recordID string, seq int, at time.Time, route, newJSON string) error {
	if _, err := t.exec(`UPDATE binding_event SET confirmed = 1, delivered_at = ?, route = ?, entry_json = ? WHERE record_id = ? AND seq = ?`,
		formatTime(at), nullIfEmpty(route), newJSON, recordID, seq); err != nil {
		return fmt.Errorf("db: event confirm %s#%d: %w", recordID, seq, mapBusy(err))
	}
	return nil
}

// EventMaxSeq returns the highest seq recordID has, 0 when it has none, in a
// write transaction's view. SaveWithLog computes the seqs of the entries it
// appends from it without leaving the transaction.
func (t *Tx) EventMaxSeq(recordID string) (int, error) {
	var n sql.NullInt64
	if err := t.queryRow(
		`SELECT MAX(seq) FROM binding_event WHERE record_id = ?`, recordID).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: event max seq %s: %w", recordID, mapBusy(err))
	}
	if !n.Valid {
		return 0, nil
	}
	return int(n.Int64), nil
}

// EventMaxSeq returns the highest seq recordID has, 0 when it has none.
func (d *DB) EventMaxSeq(recordID string) (int, error) {
	var n sql.NullInt64
	if err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT MAX(seq) FROM binding_event WHERE record_id = ?`, recordID).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: event max seq %s: %w", recordID, mapBusy(err))
	}
	if !n.Valid {
		return 0, nil
	}
	return int(n.Int64), nil
}

// The *DB forms below wrap one Tx each, following write.go.

func (d *DB) RecordPut(r Record) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.RecordPut(r)
		return err
	})
	return id, err
}

func (d *DB) RecordArchive(owner, name string, at time.Time) error {
	return d.Tx(func(t *Tx) error { return t.RecordArchive(owner, name, at) })
}

func (d *DB) RecordDelete(owner, name string) error {
	return d.Tx(func(t *Tx) error { return t.RecordDelete(owner, name) })
}

func (d *DB) RecordSetViewed(owner, name string, at time.Time) error {
	return d.Tx(func(t *Tx) error { return t.RecordSetViewed(owner, name, at) })
}

func (d *DB) EventAppend(recordID string, e RecordEvent) error {
	return d.Tx(func(t *Tx) error { return t.EventAppend(recordID, e) })
}

func (d *DB) EventReplaceAll(recordID string, evs []RecordEvent) error {
	return d.Tx(func(t *Tx) error { return t.EventReplaceAll(recordID, evs) })
}

func (d *DB) EventConfirm(recordID string, seq int, at time.Time, route, newJSON string) error {
	return d.Tx(func(t *Tx) error { return t.EventConfirm(recordID, seq, at, route, newJSON) })
}
