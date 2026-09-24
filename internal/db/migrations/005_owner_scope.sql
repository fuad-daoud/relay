-- Schema v5 (P5 plan §3): the live record's uniqueness becomes (owner, name),
-- so the one machine database can hold every owner's bindings and a name is
-- unique only within its owner's scope. The local store's owner is ''.
--
-- Same dialect as 001-004: DDL is idempotent, no AUTOINCREMENT, no WITHOUT
-- ROWID, no triggers/views/FTS/virtual tables, no RETURNING. DROP INDEX
-- IF EXISTS is standard SQLite and Turso SQL: 002 already runs a non-CREATE
-- statement (INSERT OR IGNORE), so the "CREATE ... only" note in 001's header
-- describes the create-table style, not an exhaustive whitelist of statements.

DROP INDEX IF EXISTS binding_record_name_uidx;

CREATE UNIQUE INDEX IF NOT EXISTS binding_record_owner_name_uidx
    ON binding_record(owner, name) WHERE archived_at IS NULL;
