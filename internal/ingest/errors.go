package ingest

import "errors"

// ErrSource reports that a Source could not produce a valid binding: its
// bind.json is missing or invalid, or its tarball could not be opened or
// indexed. Ingest returns it without writing anything.
var ErrSource = errors.New("ingest: source")

// ErrCursor is internal to readAppendOnly: it marks a detected rewrite (the
// file shrank, or its head no longer matches the saved cursor). It never
// escapes Ingest -- the caller logs at Info and restarts the cursor at 0.
var ErrCursor = errors.New("ingest: cursor")
