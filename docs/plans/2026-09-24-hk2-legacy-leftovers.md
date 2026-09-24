# HK2: pre-rename leftovers seal and import too

Found on the first live install of v0.13 (2026-09-24, zen). **Stop rule:** if a step contradicts
the code, stop and report. Tests change only as the steps say.

## 1. The three leftovers

1. **Relay-era streams never seal.**
   - `(*Store).StreamDrained` (`internal/store/seal.go`) requires `"\nrelevo-exit:"` and
     `StreamOffset >= size`.
   - A stream written before the rename ends in `\nrelay-rusage:…\n\nrelay-exit:0\n`. Its drain
     offset also stops **before** those trailer lines: live example `gomaxprocs/002-builder.jsonl`,
     size 216758, offset 216687, and the remaining 71 bytes are exactly the rusage and exit lines.
   - So 11 DONE bindings keep their last round's files on disk forever.
2. **A legacy file whose kv row already exists is never removed.** `db.KVImportFile`
   (`internal/db/kv.go`) returns the row and ignores the file. The new daemon wrote kv `daemon`
   on start, so `<root>/daemon.json` stays forever. The same applies to any key written before
   its file was read.
3. **`.viewed` sidecars stay.** P3a moved "viewed" to `binding_record.viewed_at` but never
   imports or removes `<root>/<name>/.viewed`, and 8 are left. Also, a DONE binding whose rounds
   are all sealed leaves an **empty** binding dir behind.

## 2. Changes

- **`StreamDrained(b, round)`** is true iff the stream file is missing, **or** it contains an exit
  trailer (`"\nrelevo-exit:"` **or** the legacy `"\nrelay-exit:"`; take the legacy literal from
  `internal/legacy`, which is the only allowed home of the old name, adding a constant there if
  none exists), **and** every byte at or after `b.Builder.StreamOffset` belongs to trailer lines.
  - Trailer lines are lines that are empty or start with `relevo-rusage:`, `relevo-exit:`,
    `relay-rusage:` or `relay-exit:`.
  - An offset at or past EOF is trivially fine.
- **`KVImportFile(kv, key, path)`:** when the row exists **and** the file exists, remove the file
  (a remove error is logged, not returned) and return the row. The row is the truth: nothing
  writes these files any more.
- **`.viewed`:** in the store's `importPresent(name)`, when `Dir(name)/.viewed` exists:
  - if the record's `viewed_at` is NULL, set it from the file's mtime;
  - then remove the file.

  Keep the import cheap: one stat.
- **Empty dirs:** in the daemon's seal pass (`sealRounds`, `internal/relevo/daemon.go`), after
  sealing, if `b.State == StateDone` and `Dir(b.Name)` is empty, `os.Remove` it (not RemoveAll).
  Ignore errors.

## 3. Tests

- `StreamDrained`: a relay-era stream with the trailer beyond the offset → true; a trailer absent
  → false; non-trailer bytes after the offset → false.
- `KVImportFile`: a row present and a file present → the file is removed and the row returned
  unchanged.
- `.viewed`: the file becomes `viewed_at` (mtime) when unset and is removed; an existing
  `viewed_at` is kept and the file is still removed.
- The seal pass removes a DONE binding's empty dir and keeps an ACTIVE binding's empty dir.
- **Mutation:** drop the legacy trailer from `StreamDrained`, and confirm the relay-era test
  fails. Then revert.

## 4. Working efficiently

- Read `internal/store/{seal.go,db.go}`, `internal/db/kv.go`, `internal/relevo/daemon.go`
  (sealRounds) and `internal/legacy/legacy.go` in one batch.
- **Focused loop:** `go test ./internal/store/ ./internal/db/ ./internal/relevo/ ./internal/legacy/ -count=1`.
- **Full check:** `make check`, then `make e2e`. Commit as one commit. Do not rebase.
