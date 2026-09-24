# P6: serve log/show/tab fold into show and history --owner; the server imports owner tarballs at startup

Spec: `docs/specs/2026-09-24-db-as-record-design.md` §6 (the last row: "serve log/show/tab/unbind/gc
→ show/history/unbind --owner <label> run on the server host"; this round does log, show and tab).
Base: main at v0.13.0 plus fixes.

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. Tests change only as §8 sanctions.

## 1. System overview

On a server host, `relevo serve log --owner X <name>`, `relevo serve show --owner X <name>` and
`relevo serve tab [--owner X]` (`cmd/relevo/serve.go` `cmdServeLog` ~:733, `cmdServeShow` ~:797,
`cmdServeTab` ~:872) read one owner's bindings. They duplicate `relevo show`, `relevo show --log`
and `relevo history --tab`.

After this round:
- `relevo show <name> --owner <label|id> [--state DIR] …` does exactly what `serve show` did, and
  with `--log` exactly what `serve log` did.
- `relevo history --tab --owner <label|id> [--state DIR] …` does exactly what `serve tab --owner`
  did. `--owner all` (or `--by owner`) covers every owner.
- The three `serve` subverbs exit 2, naming the new form.
- When `relevo serve` starts, it imports every owner root's `.archive/*.tar.gz` once, through
  `ListArchived`, which already imports and removes them (`internal/store/archive.go:61,182`).

## 2. File structure

```
cmd/relevo/show.go        --owner, --state (§4.1)
cmd/relevo/history.go     --owner, --state with --tab (§4.2)
cmd/relevo/serve.go       cmdServeLog/Show/Tab bodies become helpers show/history call; the dispatch refuses the 3 names (§4.3)
internal/serve/daemon.go  Run imports owner tarballs once before its loop (§4.4)
cmd/relevo/*_test.go, internal/serve/*_test.go   ported per §8
README.md                 the Remote builders: the server section's admin verbs
```

## 3. Data structures

None.

## 4. Contracts

### 4.1 show --owner

In `cmdShow` (`cmd/relevo/show.go:74`):
- add `--owner string` (a client label or id) and `--state string` (the same meaning as the serve
  verbs' `--state`);
- when `--owner` is set, route to the old `cmdServeShow` body, or to the `cmdServeLog` body when
  `--log` is given without `--round`. Move those bodies into helpers `serveShow(...)` /
  `serveLog(...)` that take the parsed values; **do not duplicate them**;
- output, exit codes and root resolution (`adminRoot`, the running daemon found through the DB)
  must be **byte-identical** to the old serve verbs;
- `--state` without `--owner` exits 2: `--state only applies with --owner`.

### 4.2 history --tab --owner

In `cmdHistory` (`cmd/relevo/history.go:144`):
- add `--owner string` and `--state string`;
- `--owner` requires `--tab` (otherwise exit 2: `--owner works with --tab`);
- with `--tab --owner X`, route to the old `cmdServeTab` body (helper `serveTab(...)`), with
  `owner=X`;
- `--owner all` means every owner (today's `serve tab` with no `--owner`);
- `--by` accepts `owner` only together with `--owner`;
- the output must be byte-identical to `serve tab`.

### 4.3 The serve dispatch

In `cmdServe` (`cmd/relevo/serve.go:149`), `case "log"`, `"show"` and `"tab"` print, and exit 2:
- `relevo serve log was removed; use relevo show <name> --owner <label> --log`
- `relevo serve show was removed; use relevo show <name> --owner <label>`
- `relevo serve tab was removed; use relevo history --tab --owner <label|all>`

Follow the existing `case "gates", "available", "unavailable":` refusal at :189, and drop the three
lines from the serve usage text.

### 4.4 The startup tarball import

In `(*Server).Run` (`internal/serve/daemon.go`, just before or after the existing one-time
`settleAllServed` under `s.mu`):
- for every owner dir under `<Root>/bindings`, call `s.ownerStore(root).ListArchived()` once;
- log `slog.Info("imported archived bindings", "owner", label, "count", n)` when n > 0, and
  `slog.Warn` on error;
- never fail startup.

## 5. Pseudocode

In §4.

## 6. Error handling

- Invalid flag combinations exit 2 with one line.
- The routed helpers keep their old errors exactly.

## 7. Working efficiently

- **Read in one batch:**
  - `cmd/relevo/{show.go, history.go}`;
  - `cmd/relevo/serve.go:140-200, 700-960`;
  - `internal/serve/{daemon.go, admin.go:300-400, serve.go:130-160}`;
  - `internal/store/archive.go:55-80,175-260`;
  - `grep -rn 'serve", "log"\|serve", "show"\|serve", "tab"\|cmdServeLog\|cmdServeShow\|cmdServeTab' cmd internal`.
- **Focused loop:** `go test ./cmd/relevo/ ./internal/serve/ -count=1`.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.** The serve admin tests already run in-process; port them.

## 8. Ordered steps

**Closed deletion list:**
- **D1:** the `serve log/show/tab` dispatch to their `cmdServe*` functions. The bodies survive as
  helpers.

**Sanctioned ports:** tests that invoke `serve log|show|tab` are ported to the `show --owner` /
`history --tab --owner` forms, with their assertions unchanged apart from the command spelling and
the usage text. **Any other failing test: stop and report.**

1. **§4.1 + tests:** `show --owner` equals the old `serve show` output for a seeded server state;
   `--log` equals `serve log`; `--state` alone refuses.
2. **§4.2 + tests:** `history --tab --owner X` and `--owner all` equal `serve tab`; `--owner`
   without `--tab` refuses.
3. **§4.3 + a refusal test** over the three names.
4. **§4.4 + a test:** a tarball placed in an owner's `.archive/` before `Run` is imported and
   removed, and `ListArchived` shows it.
5. **README:** the server admin section's `serve log/show/tab` lines become the new forms.
6. **Full check.** Report the tails, `git diff --stat`, and the ported tests. Commit as one
   commit. Do not rebase.
