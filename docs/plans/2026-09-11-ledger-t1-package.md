# Ledger T1: the `internal/ledger` package and `store.LedgerPath` (#61 step 1)

**Design spec:** `docs/specs/2026-09-11-availability-ledger-design.md` -- read §1, §3.1–3.4, §5.3, §6
**Issue:** #61 (step 1)
**Depends on:** nothing beyond `main` as of #83.

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- This task creates `internal/ledger/ledger.go` and `ledger_test.go`, and
  adds one method to `internal/store/store.go` (plus its test). Nothing
  else. No caller is wired yet -- that is T2.
- `ledger` imports nothing from `internal/` -- not `candidate`, not
  `store`. `Gated` takes a `providerOf` function so the package does not
  need to know how a token is parsed. Keep it that way.
- Every exported symbol gets a doc comment explaining the rationale, in
  the house style (read `internal/candidate/candidate.go` for the tone).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: `Kind`, `Entry`, `Ledger`, `Load`/`Save`, `Prune`/`Append`/`Clear`, `Gated`

**Files:**
- Create: `internal/ledger/ledger.go`, `internal/ledger/ledger_test.go`
- Modify: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces produced** (spec §3.1–3.4), by exactly these names:

```go
type Kind string
const (
    SpawnFailed Kind = "spawn_failed"
    RateLimited Kind = "rate_limited"
)

var ErrBadEntry = errors.New("bad ledger entry")

type Entry struct {
    Kind    Kind      `json:"kind"`
    Subject string    `json:"subject"`
    At      time.Time `json:"at"`
    Until   time.Time `json:"until,omitempty"`
    Note    string    `json:"note,omitempty"`
    Source  string    `json:"source"`
    Binding string    `json:"binding,omitempty"`
}
func (e Entry) Expired(now time.Time) bool

type Ledger struct {
    Entries []Entry `json:"entries"`
}
func Load(path string) (Ledger, error)
func Save(path string, l Ledger) error
func (l Ledger) Prune(now time.Time) Ledger
func (l Ledger) Append(e Entry) Ledger
func (l Ledger) Clear(kind Kind, subject string) Ledger

type Gate struct {
    Token  string
    Kind   Kind
    Since  time.Time
    Until  time.Time
    Note    string
    Source  string
    Binding string
}
func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time) []Gate

// store:
func (s *Store) LedgerPath() string
```

- [ ] **Step 1: `store.LedgerPath`**

  In `internal/store/store.go`, next to `ArchiveDir`:

  ```go
  // LedgerPath is the availability ledger (#61): one file at the state root,
  // beside .lock, so every write to it can run under WithLock like a
  // bind.json write.
  func (s *Store) LedgerPath() string { return filepath.Join(s.root, "ledger.json") }
  ```

  Test in `store_test.go`: `TestLedgerPath` -- `New(dir).LedgerPath() ==
  filepath.Join(dir, "ledger.json")`.

  Run: `go test ./internal/store/ -run LedgerPath`. Expected: green.

- [ ] **Step 2: types, `Expired`, pure operations** (spec §3.1–3.3)

  Package doc: "Package ledger records when a candidate could not be used
  and why: spawn failures relay observed, rate limits the planner reported.
  It records and answers; it never decides (#61 step 1)."

  `Expired`: `!e.Until.IsZero() && !now.Before(e.Until)` -- so `Until ==
  now` is expired.

  `Prune`: returns a new `Ledger` with every non-expired entry, in order.
  `Append`: returns a new `Ledger` with `e` at the end; no dedupe (comment:
  two spawn failures are two events; step 7 counts them). `Clear`: returns
  a new `Ledger` without every entry whose `Kind == kind && Subject ==
  subject`. All three must not mutate the receiver's slice: copy with
  `append([]Entry(nil), ...)`.

  Tests (`ledger_test.go`), with a fixed `now := time.Date(2026, 9, 11, 15,
  0, 0, 0, time.UTC)`:
  - `TestExpired`: zero `Until` → false; `Until = now.Add(time.Minute)` →
    false; `Until = now` → true; `Until = now.Add(-time.Minute)` → true.
  - `TestPruneAppendClearArePure`: build a ledger of three entries (one
    expired), call each operation, assert the original `Entries` slice is
    unchanged (`reflect.DeepEqual` against a copy taken first) and the
    result has the expected length and order.
  - `TestClearMatchesKindAndSubject`: entries `{RateLimited, "anthropic"}`,
    `{RateLimited, "google"}`, `{SpawnFailed, "anthropic"}` (a token can
    look like anything; the test only needs a subject collision);
    `Clear(RateLimited, "anthropic")` leaves the other two.

- [ ] **Step 3: `Load` and `Save`** (spec §3.3, §6)

  `Load`: `os.ReadFile`; `os.ErrNotExist` → `Ledger{}, nil`; other read
  error → `fmt.Errorf("read ledger %s: %w", path, err)`; decode error →
  `fmt.Errorf("decode ledger %s: %w", path, err)`. Then validate each entry
  per spec §3.2's table, returning `fmt.Errorf("ledger %s: entry %d: %s: %w",
  path, i, why, ErrBadEntry)` with `why` one of:
  `unknown kind %q`, `subject is empty`, `at is zero`, `until precedes at`,
  `source must be "relay" or "planner" (got %q)`. `Load` does **not**
  prune; callers do, with their own `now`.

  `Save`: `json.MarshalIndent(l, "", "  ")` then write to `path + ".tmp"`
  with mode `0o644` and `os.Rename` over `path` (mirror what
  `store.writeFileAtomic` does; it is unexported in another package, so
  reimplement the four lines rather than export it). Create the parent
  directory with `os.MkdirAll(filepath.Dir(path), 0o755)` first -- the
  state root may not exist yet on a fresh machine.

  Tests:
  - `TestLoadMissingIsEmpty`: a path in `t.TempDir()` that does not exist
    → zero entries, nil error.
  - `TestSaveLoadRoundTrip`: two entries with every field set (one with a
    zero `Until`), `Save` then `Load`, `reflect.DeepEqual` after
    normalising times with `.UTC()` on both sides (JSON round-trips the
    instant, not the location).
  - `TestLoadValidation`: a table of hand-written JSON bodies, one per
    `why` above, each asserting `errors.Is(err, ErrBadEntry)` and the
    message contains the `why` text and `entry 0`.
  - `TestSaveCreatesParent`: path under a not-yet-existing subdirectory of
    `t.TempDir()`; `Save` succeeds and the file exists.

- [ ] **Step 4: `Gated`** (spec §3.4, §5.3)

  ```go
  // Gated is the one view every renderer uses: for each live entry, which
  // configured candidates it gates. A spawn failure gates its own token; a
  // rate limit gates every candidate of its provider, because the quota is
  // the provider's, not the model's (spec §1).
  func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time) []Gate
  ```

  Algorithm, exactly spec §5.3: prune with `now`; for `SpawnFailed`, emit
  a gate when `e.Subject` is in `refs`; for `RateLimited`, emit one gate
  per `ref` with `providerOf(ref) == e.Subject`. Copy `At`→`Since`,
  `Until`, `Note`, `Source`, `Binding`. Sort by `Token`, then `Since`. Return `nil`
  (not an empty slice) when nothing is gated, so `omitempty` drops it from
  JSON.

  Test `TestGated`: `refs = [agy/google/m, claude/anthropic/opus,
  claude/anthropic/sonnet, opencode/openrouter/z-ai/m]`, `providerOf` =
  split on `/` and take index 1. Ledger:

  | kind | subject | until |
  |---|---|---|
  | RateLimited | `anthropic` | zero |
  | SpawnFailed | `agy/google/m` | now+10m |
  | SpawnFailed | `opencode/openrouter/z-ai/m` | now−1m (expired) |
  | SpawnFailed | `claude/anthropic/haiku` | now+10m (not configured) |

  Want, in order: `agy/google/m` SpawnFailed; `claude/anthropic/opus`
  RateLimited; `claude/anthropic/sonnet` RateLimited. Nothing for opencode
  (expired) and nothing for haiku (not in `refs`). Then `Gated` on an
  empty ledger returns `nil`.

  Test `TestGatedOrdersByTokenThenSince`: two `SpawnFailed` entries for the
  same token with different `At`; the gates come out in `At` order.

  Run: `go test ./internal/ledger/`. Expected: green.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add internal/ledger/ internal/store/store.go internal/store/store_test.go
  git commit -m "feat(ledger): availability ledger package and store.LedgerPath (#61)"
  ```

## Report

Include the `go test ./internal/ledger/ -v` summary, the `make check`
result, and `git diff --stat HEAD~1` (four files).
