# `relevo serve status --json`: a machine-readable server census (for fuad-daoud/servers#13, relevo#391)

**Why:** the burst provider (fuad-daoud/servers#13) observes each burst worker every 5 minutes over ssh:
it needs the builder census (to delete an idle worker) and each binding's round and candidate (to record
which builder ran on which node). `relevo serve status` prints only text. This round adds `--json`, plus
`last_seen` per owner and `last_contact` for the server (relevo#391 names `last_contact` for the worker's
dead-man switch). Nothing else changes.

**If a step is impossible as written or contradicts what you find, stop and report — do not improvise.**
CI has no harness and no network: the new logic is tested as pure functions in `internal/serve`; the
cmd test (if any) must not start a server daemon or a harness.

## 1. Data structures

In `internal/serve/admin.go`:
```
OwnerStatus gains:  LastSeen time.Time   // max over the owner's bindings of b.Serve.LastSeen (zero when none);
                                          // the same field GCAbandoned reads at admin.go:222-225, but WITHOUT
                                          // its fallback to RoundStartedAt: only a real client request counts.

type StatusJSON struct {                  // the --json document; field names are a contract (servers#13 parses them)
    Builders    remote.BuildersView `json:"builders"`       // running, queued, cap (+ existing optional fields)
    LastContact *time.Time          `json:"last_contact"`   // max OwnerStatus.LastSeen over all owners; null when none
    Owners      []OwnerJSON         `json:"owners"`         // [] never null; sorted by Label as AdminStatus returns them
}
type OwnerJSON struct {
    Owner    string        `json:"owner"`      // remote.ClientID string form
    Label    string        `json:"label"`
    LastSeen *time.Time    `json:"last_seen"`  // null when zero
    Report   relevo.Report `json:"report"`     // unchanged relevo.Report JSON (bindings[] with name, round, state,
                                               // builder_candidate, builder_status, ...)
}
func StatusDocument(owners []OwnerStatus, builders remote.BuildersView) StatusJSON   // pure
```
Times are UTC, marshalled by encoding/json (RFC 3339).

## 2. Changes

- `internal/serve/admin.go`
  - `OwnerStatus` (admin.go:18-22): add `LastSeen`.
  - `AdminStatus` (admin.go:31-98): while it walks each owner's bindings, compute `LastSeen` as above.
    Reuse the walk it already does; do not add a second directory walk.
  - add `StatusJSON`, `OwnerJSON`, `StatusDocument` (below `RenderAdminStatus`, admin.go:132).
- `cmd/relevo/serve.go` `cmdServeStatus` (serve.go:627-672): add `fs.Bool("json", false, "print the census as JSON")`.
  With `--json`: `json.NewEncoder(os.Stdout)` with two-space indent, encode `serve.StatusDocument(owners, builders)`.
  Without it: output byte-identical to today. Update the command's usage/help text wherever `serve status` is listed
  (grep `serve status` under `cmd/relevo/`) to show `[--json]`.
- Docs: add one line for `--json` wherever `relevo serve status` is documented for admins (grep `serve status` in
  `README.md` and `docs/` excluding `docs/specs` and `docs/plans`), naming the three top-level keys.

## 3. Tests (internal/serve, pure)

- `TestStatusDocumentLastContact`: two owners with LastSeen t1 < t2 and one with zero → `last_contact == t2`,
  the zero owner's `last_seen` is JSON `null`; `owners` sorted by label.
- `TestStatusDocumentEmpty`: no owners → `{"builders":{...},"last_contact":null,"owners":[]}` (`[]`, not `null`).
- `TestAdminStatusLastSeen` (if the existing admin tests have a fixture server with bindings — extend it; otherwise
  construct the minimal store the other `AdminStatus` tests use): a binding with `Serve.LastSeen` set yields that
  owner's LastSeen; a binding with only `RoundStartedAt` yields zero.
- Mutation check: make `StatusDocument` take the **min** instead of the max for `last_contact` → the first test fails.
  Report it and revert.

## 4. Working efficiently

Read `internal/serve/admin.go` (whole, ~500 lines), `cmd/relevo/serve.go:620-675`, and the existing admin tests
(`internal/serve/admin_test.go`) in one batched step. Make all edits to each file in one call.
Focused: `go test ./internal/serve/ -run 'StatusDocument|AdminStatus'`
Full check once at the end: `make check` (gofmt over tracked files, go vet, go mod tidy check, tests).

## Report
Files changed, `make check` output, the mutation check, and a sample of `StatusDocument` JSON from a test
(paste it). Commit on the binding branch: `feat(serve): relevo serve status --json with last_seen/last_contact (servers#13)`.
