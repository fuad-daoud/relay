package db

import "errors"

// ErrOpen reports that the database file could not be opened or migrated.
// Open wraps it together with the underlying driver error.
var ErrOpen = errors.New("open failed")

// ErrBusy reports that a transaction could not proceed because another
// process held the database (SQLITE_BUSY) past busy_timeout.
var ErrBusy = errors.New("busy")

// ErrNotFound reports that a reader found no matching row.
var ErrNotFound = errors.New("not found")

// ErrInvalid reports a value that failed validation -- an enum field outside
// its allowed set, or a natural key with none of its parts set. Writers wrap
// it with the field name: fmt.Errorf("%s: %w", field, ErrInvalid).
var ErrInvalid = errors.New("invalid")

// ErrNewerSchema reports that the database's schema is newer than the
// migrations this relevo embeds. Open leaves such a database untouched and
// reports it through DB.Newer; `relevo db migrate` refuses with this error.
var ErrNewerSchema = errors.New("schema is newer than this relevo")
