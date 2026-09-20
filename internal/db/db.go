package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	// modernc.org/sqlite is the one driver import in relay: this package is
	// the only place that knows it is sqlite today and Turso tomorrow.
	"modernc.org/sqlite"
)

// rfc3339Milli is the text encoding every timestamp uses in the db: RFC3339
// UTC with millisecond precision.
const rfc3339Milli = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(rfc3339Milli) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(rfc3339Milli, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// sqliteBusy is SQLITE_BUSY, sqlite's result code for "database is locked".
const sqliteBusy = 5

// DB is a connection to relay's sqlite database. The zero value is not
// usable; construct one with Open.
type DB struct {
	sqlDB *sql.DB
}

// Open opens (creating if needed) the sqlite database at path, applying
// every pending migration before returning. path's directory must already
// exist. The connection runs with WAL journaling, a 5s busy timeout, and
// foreign keys on.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w: %w", path, ErrOpen, err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: open %s: %w: %w", path, ErrOpen, err)
	}

	if err := applyMigrations(sqlDB, migrationFiles); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: open %s: %w: %w", path, ErrOpen, err)
	}

	return &DB{sqlDB: sqlDB}, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error {
	if err := d.sqlDB.Close(); err != nil {
		return fmt.Errorf("db: close: %w", err)
	}
	return nil
}

// Version returns the highest applied schema_version, 0 when none has run.
func (d *DB) Version() (int, error) {
	var v sql.NullInt64
	if err := d.sqlDB.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("db: version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// Tx is a locked view of the database inside one BEGIN IMMEDIATE
// transaction. Every writer and reader exists on it; the *DB forms wrap one
// Tx each.
type Tx struct {
	conn *sql.Conn
	ctx  context.Context
}

// Tx runs fn inside one BEGIN IMMEDIATE transaction: commit on a nil
// return, rollback otherwise. A driver busy error is mapped to ErrBusy.
func (d *DB) Tx(fn func(*Tx) error) error {
	ctx := context.Background()

	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: tx: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("db: tx begin: %w", mapBusy(err))
	}

	if txErr := fn(&Tx{conn: conn, ctx: ctx}); txErr != nil {
		if _, rerr := conn.ExecContext(ctx, "ROLLBACK"); rerr != nil {
			return fmt.Errorf("db: tx: %v, and rollback failed: %w", txErr, rerr)
		}
		return txErr
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("db: tx commit: %w", mapBusy(err))
	}
	return nil
}

// mapBusy turns a driver's SQLITE_BUSY into ErrBusy, so callers can
// errors.Is against one sentinel regardless of the driver underneath.
func mapBusy(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteBusy {
		return ErrBusy
	}
	if strings.Contains(err.Error(), "database is locked") {
		return ErrBusy
	}
	return err
}

// exec runs a statement against the transaction's connection.
func (t *Tx) exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, query, args...)
}

// queryRow runs a single-row query against the transaction's connection.
func (t *Tx) queryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, query, args...)
}

// query runs a multi-row query against the transaction's connection.
func (t *Tx) query(query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(t.ctx, query, args...)
}

// queryer is the slice of *sql.DB and *sql.Conn that readers need, so the
// same query function backs both *DB and *Tx readers.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
