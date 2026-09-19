# Remote builders, plan 4b: enrolled clients reload live; then plan 4's scenarios (#100)

Round 2 on the same branch. Round 1 halted at plan 4's step 1 with the
right diagnosis: `serve.New` loads `clients.json` once into
`Server.clients`, and nothing re-reads it, so `relay serve enroll` or
`revoke` on a running server has no effect until restart. That is a
production defect, not a test problem. This round fixes it in
`internal/serve/clients.go`, then completes plan 4
(`docs/plans/2026-09-19-remote-builders-4-e2e.md`, in this tree) as
written. The round-1 worktree has plan 4's `internal/e2e/fakes_test.go`
and `remote_test.go` uncommitted; keep them and build on them.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else. Do not rebase or
move this branch.

**Scope guard.** Plan 4's files plus `internal/serve/clients.go` and
`internal/serve/serve_test.go` (or the clients test file), and copy this
plan to `docs/plans/2026-09-19-remote-builders-4b-clients-reload.md`.
Foreground only, no sub-agents.

## 1. The seam

`Clients` re-reads its file whenever the file changed since the last
read. Rule, in `internal/serve/clients.go`:

```
type Clients struct {
    path  string
    mu    sync.Mutex
    list  []Client
    stamp fileStamp     // ModTime and Size of the file at the last read; zero when the file was absent
}

func (c *Clients) refresh() error        // called under mu by Lookup, List, LabelOf, Add, Revoke
    st, err := os.Stat(c.path)
    absent -> if c.stamp is not zero: c.list = nil, c.stamp = zero; return nil     (file deleted: nobody is enrolled)
    err (other) -> return it
    if (st.ModTime(), st.Size()) == c.stamp -> return nil
    read and parse the file; on success c.list = parsed, c.stamp = (st.ModTime(), st.Size())
    on a parse error -> return it and keep the old list (a half-written file must not lock everyone out;
                        save() writes temp-and-rename, so a torn read is rare but possible)

Lookup: refresh(); a refresh error is logged once per distinct error text (slog.Warn) and the old list is used.
List/LabelOf: same.
Add/Revoke: refresh() first (so a CLI enroll after another enroll sees both), then mutate, then save() and set stamp
            from the file just written.
```

Mtime resolution on some filesystems is one second; Size is in the stamp
so two writes inside one second with different content still differ.
Two writes inside one second with the same size would be missed -- say so
in the doc comment; `enroll` and `revoke` are human-paced.

## 2. Steps

**Step 1 -- seam.** Tests: `TestClientsLookupSeesEnrollFromAnotherInstance`
(two `LoadClients` on one path; `Add` on the first; `Lookup` on the second
returns `KeyActive` without any reload call -- mutation target: make
`refresh` a no-op and this fails); `TestClientsRefreshKeepsListOnParseError`
(corrupt the file; `Lookup` still answers from the old list);
`TestClientsRefreshOnDelete` (remove the file; `Lookup` -> `KeyUnknown`).
Implement §1. Commit: `serve: enrolled clients reload when the file
changes (#100)`.

**Step 2 -- plan 4, steps 1–3 as written**, with one change to its
harness: `enroll` in `newServer` simply calls `Add` on a `Clients` loaded
from the server's `clients.json` path (a second instance); the running
server picks it up through §1. Commits as plan 4 names them.

**Step 3.** `make check` green; report per plan 4's step 3 plus the
mutation result from step 1. Done marker.
