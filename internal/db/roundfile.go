package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// rfc3339Nano is how a sealed file's mtime is stored. It is not rfc3339Milli:
// mtime stands in for the os.Stat a caller used to do, so it keeps the
// file's own precision (store.StatFile). Parsing accepts a shorter fraction
// too, so a millisecond stamp reads back unchanged.
const rfc3339Nano = time.RFC3339Nano

func formatMTime(t time.Time) string { return t.UTC().Format(rfc3339Nano) }

func parseMTime(s string) (time.Time, error) { return time.Parse(rfc3339Nano, s) }

// RoundFilePut inserts or replaces one sealed round file: the file's exact
// bytes, its byte count and sha256, its round's NNN prefix, the file's own
// mtime and the seal's stamp. (record_id, name) is the primary key, so a
// second put of the same file is idempotent -- which is what lets a seal pass
// that failed to remove the file retry with the same bytes (P3c §4.2, §6).
func (t *Tx) RoundFilePut(recordID, name string, round int, body []byte, mtime, now time.Time) error {
	// A nil body binds as SQL NULL, which round_file.body NOT NULL refuses.
	// An empty file's bytes are empty, not absent.
	if body == nil {
		body = []byte{}
	}
	sum := sha256.Sum256(body)
	sealedAt := now
	if sealedAt.IsZero() {
		sealedAt = time.Now()
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO round_file
			(record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		recordID, name, round, body, len(body), hex.EncodeToString(sum[:]),
		formatMTime(mtime), formatTime(sealedAt)); err != nil {
		return fmt.Errorf("db: round file put %s/%s: %w", recordID, name, mapBusy(err))
	}
	return nil
}

// RoundFileGet returns one sealed file's bytes and its mtime; ok is false
// when the record has no such row. A read error other than a miss is
// returned, so a caller can tell "never sealed" from "the database failed".
func (d *DB) RoundFileGet(recordID, name string) (body []byte, mtime time.Time, ok bool, err error) {
	var mtimeText string
	err = d.sqlDB.QueryRowContext(context.Background(),
		`SELECT body, mtime FROM round_file WHERE record_id = ? AND name = ?`,
		recordID, name).Scan(&body, &mtimeText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("db: round file get %s/%s: %w", recordID, name, mapBusy(err))
	}
	mtime, err = parseMTime(mtimeText)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("db: round file get %s/%s: parse mtime: %w", recordID, name, err)
	}
	// A zero-length blob scans back as a nil slice. An existing empty row
	// has an empty body, not an absent one.
	if body == nil {
		body = []byte{}
	}
	return body, mtime, true, nil
}

// RoundFileList returns the basenames of recordID's sealed files, sorted.
func (d *DB) RoundFileList(recordID string) ([]string, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT name FROM round_file WHERE record_id = ? ORDER BY name ASC`, recordID)
	if err != nil {
		return nil, fmt.Errorf("db: round file list %s: %w", recordID, mapBusy(err))
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: round file list %s: %w", recordID, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: round file list %s: %w", recordID, mapBusy(err))
	}
	return out, nil
}
