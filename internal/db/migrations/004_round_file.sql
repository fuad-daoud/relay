-- Schema v4 (P3c plan §3): one row per sealed round file, holding the file's
-- raw bytes. A closed round's NNN-* files (plan, report, consul asks and
-- findings, gate and builder logs, the builder stream and the done marker)
-- become rows here and are then deleted from the binding directory, once
-- nothing can still read them (internal/store/seal.go, Sealable).
--
-- keyed by the binding record and the file's basename: binding_record is the
-- system of record behind internal/store's API (003), and a name is unique
-- within one binding directory. `name` is the basename, e.g. `003-report.md`
-- or `003-a1b2c3d4-findings.md`, and `round` its NNN prefix.
--
-- Same dialect as 001 and 003: CREATE TABLE/INDEX IF NOT EXISTS only, no
-- AUTOINCREMENT, no WITHOUT ROWID, no triggers/views/FTS/virtual tables, no
-- RETURNING. Timestamps are RFC3339 UTC text; `mtime` keeps the file's own
-- mtime at seal with nanosecond precision, because it stands in for the
-- os.Stat a caller used to do (store.StatFile).

CREATE TABLE IF NOT EXISTS round_file (
    record_id TEXT NOT NULL REFERENCES binding_record(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    round INTEGER NOT NULL,
    body BLOB NOT NULL,
    bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    mtime TEXT NOT NULL,
    sealed_at TEXT NOT NULL,
    PRIMARY KEY (record_id, name)
);
