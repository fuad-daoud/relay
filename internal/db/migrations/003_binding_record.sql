-- Schema v3 (P3a plan §3.1): the store's own record of a binding and of its
-- log, replacing <root>/<name>/bind.json and <root>/<name>/log.jsonl.
--
-- Deliberately NOT the ingest mirror's binding/event tables in 001: those are
-- a projection of the same state for history queries, fed by internal/ingest.
-- binding_record/binding_event are the system of record that internal/store
-- reads and writes through the unchanged store.Store / store.Tx API.
--
-- Same dialect as 001: CREATE TABLE/INDEX IF NOT EXISTS only, no
-- AUTOINCREMENT, no WITHOUT ROWID, no triggers/views/FTS/virtual tables, no
-- RETURNING. Ids are text ULIDs; timestamps are RFC3339 UTC text with
-- millisecond precision; booleans are INTEGER 0/1. The partial unique index is
-- the same dialect as repo_origin_url_uidx in 001, which already proves the
-- dialect permits partial indexes.

CREATE TABLE IF NOT EXISTS binding_record (
    id TEXT PRIMARY KEY,
    owner TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    state TEXT NOT NULL,
    round INTEGER NOT NULL,
    cwd TEXT NOT NULL,
    record_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    viewed_at TEXT,
    archived_at TEXT
);

-- One live row per name: a store root holds one owner's bindings until phase
-- 5, and load() addresses a binding by name alone, so the live key is the name
-- (P3a round 3, B2). An archived row leaves the name reusable, which is what
-- lets Archive and a fresh bind of the same name coexist.
CREATE UNIQUE INDEX IF NOT EXISTS binding_record_name_uidx
    ON binding_record(name) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS binding_event (
    record_id TEXT NOT NULL REFERENCES binding_record(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    ts TEXT NOT NULL,
    round INTEGER NOT NULL,
    direction TEXT NOT NULL,
    kind TEXT NOT NULL,
    confirmed INTEGER NOT NULL,
    delivered_at TEXT,
    route TEXT,
    entry_json TEXT NOT NULL,
    PRIMARY KEY (record_id, seq)
);
