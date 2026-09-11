# Candidates T2: the `internal/candidate` package (#80)

**Design spec:** `docs/specs/2026-09-11-candidates-design.md` -- read §3.1–3.3, §4.1, §4.2, §5.1
**Issue:** #80
**Depends on:** T1 (`harness.RoleByName`, `Harness.CanServe`) -- already in this tree.

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
- This task **creates** `internal/candidate/candidate.go` and
  `internal/candidate/candidate_test.go` and touches nothing else. No
  caller is wired yet. `internal/alias` stays exactly as it is.
- `candidate` imports `harness`. `harness` must **not** import
  `candidate` (it does not today; keep it so).
- Every exported symbol gets a doc comment in the house style. Read
  `internal/alias/alias.go` for the tone; this package replaces it.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: `Ref`, `Candidate`, `Set`, `Load`

**Files:**
- Create: `internal/candidate/candidate.go`
- Create: `internal/candidate/candidate_test.go`

**Interfaces consumed** (from T1): `harness.Lookup(kind) (Harness, bool)`,
`harness.RoleByName(name) (RoleSpec, bool)`, `harness.RoleNames() []string`,
`Harness.CanServe(role) bool`.

**Interfaces produced** (spec §3.1–3.3):

```go
var ErrBadRef = errors.New("bad candidate reference")
var ErrUnknownCandidate = errors.New("unknown candidate")

type Ref struct{ Harness, Provider, Model string }
func ParseRef(s string) (Ref, error)
func (r Ref) String() string

type Candidate struct {
    Harness   string   `json:"harness"`
    Provider  string   `json:"provider"`
    Model     string   `json:"model"`
    Roles     []string `json:"roles"`
    Tree      string   `json:"tree,omitempty"`
    ExtraArgs []string `json:"extra_args,omitempty"`
}
func (c Candidate) Ref() Ref
func (c Candidate) Serves(role string) bool

type Set struct{ /* unexported */ }
func Load(path string) (*Set, error)
func (s *Set) Lookup(ref Ref) (Candidate, error)
func (s *Set) ForRole(role string) []Candidate
func (s *Set) Refs() []string
func (s *Set) Len() int
```

- [ ] **Step 1: `Ref`** (spec §3.1)

  Package doc: "Package candidate loads the harness/provider/model triples
  relay may start, and nothing else: which one to start is the caller's
  choice (#80)."

  `ParseRef` uses `strings.SplitN(s, "/", 3)`. Fewer than 3 parts, or any
  part empty → `fmt.Errorf("%q: want harness/provider/model: %w", s,
  ErrBadRef)`. `String()` joins with `/`.

  Test: `TestRefRoundTrip` -- table:

  | input | want Ref | err |
  |---|---|---|
  | `claude/anthropic/sonnet` | `{claude anthropic sonnet}` | nil |
  | `opencode/openrouter/z-ai/glm-5.3-flash` | `{opencode openrouter z-ai/glm-5.3-flash}` | nil |
  | `agy/google/gemini-3.8-flash-high` | `{agy google gemini-3.8-flash-high}` | nil |
  | `claude/anthropic` | — | `ErrBadRef` |
  | `claude//sonnet` | — | `ErrBadRef` |
  | `/anthropic/sonnet` | — | `ErrBadRef` |
  | `claude/anthropic/` | — | `ErrBadRef` |
  | `` | — | `ErrBadRef` |

  For each nil-error row also assert `got.String() == input`.

  Verify: `go test ./internal/candidate/ -run TestRefRoundTrip` green.

- [ ] **Step 2: `Candidate`, `Set`, `Lookup`, `ForRole`, `Refs`, `Len`** (spec §3.2, §3.3, §4.2)

  `Set` holds `byRef map[string]Candidate` keyed by `Ref.String()`. Add an
  unexported constructor `newSet() *Set` so an empty set is never a nil
  map. `Lookup` returns `fmt.Errorf("candidate %q not found (configured:
  %v): %w", ref.String(), s.Refs(), ErrUnknownCandidate)` on a miss.
  `ForRole` filters on `Serves` and sorts by `Ref().String()`. `Refs`
  returns the sorted keys. `Serves` is a linear scan of `Roles`.

  Test: `TestSetQueries` -- build a set through `Load` (step 3 provides it;
  write this test now, it fails to compile until step 3, that is fine --
  or write the file to a `t.TempDir()` and call `Load`): three entries,
  `claude/anthropic/sonnet` roles `[builder reviewer]`,
  `opencode/openrouter/z-ai/glm-5.3-flash` roles `[builder]`,
  `agy/google/gemini-3.8-flash-high` roles `[builder]`. Assert:
  `Len()==3`; `Refs()` is those three sorted (agy…, claude…, opencode…);
  `ForRole("builder")` has 3 in that order; `ForRole("reviewer")` has 1 and
  it is the claude one; `ForRole("researcher")` is empty;
  `Lookup(Ref{claude,anthropic,sonnet})` succeeds; `Lookup(Ref{claude,
  anthropic,opus})` errors with `errors.Is(err, ErrUnknownCandidate)` and
  the message contains `configured:`.

- [ ] **Step 3: `Load` and validation** (spec §4.1, §5.1)

  Implement `Load(path string) (*Set, error)` exactly per spec §5.1. Read
  the file with `os.ReadFile`; `errors.Is(err, os.ErrNotExist)` → `newSet(),
  nil`. Other read error → `fmt.Errorf("read candidates %s: %w", path,
  err)`. Decode error → `fmt.Errorf("decode candidates %s: %w", path,
  err)`. Then validate each entry, in this order, with these messages
  (each prefixed `candidates %s: ` with the path, then `candidate %d: `
  with the index):

  1. harness, provider or model empty → `harness, provider and model are required`
  2. provider contains `/` → `provider must be a single segment`
  3. `harness.Lookup` fails → `unknown harness %q (known: %v)` -- known is
     every `harness.All()` kind
  4. `len(Roles)==0` → `roles must not be empty`
  5. a role not in `harness.RoleByName` → `unknown role %q (known: %v)` --
     known is `harness.RoleNames()`
  6. `!h.CanServe(r)` → `harness %q has no definition for role %q`
  7. `Tree` not in `{"", "binding", "none"}` → `tree must be "binding" or "none"`
  8. duplicate `Ref().String()` → `duplicate candidate %s at index %d and %d`

  Doc comment on `Load`: a missing file is zero candidates and not an
  error, because relay ships none (spec §1 point 3); a present file that
  does not validate is an error at startup for every subcommand, because
  a daemon running on config it cannot parse is worse than one that
  refuses to start (spec §6).

  Test: `TestLoadMissingFileIsEmpty` -- `Load(filepath.Join(t.TempDir(),
  "none.json"))` returns a non-nil set with `Len()==0` and nil error.

  Test: `TestLoadValidation` -- table of JSON bodies, each written to a
  temp file, asserting the error message **contains** the substring:

  | body | want substring |
  |---|---|
  | `[{"harness":"claude","provider":"anthropic","roles":["builder"]}]` | `candidate 0: harness, provider and model are required` |
  | `[{"harness":"claude","provider":"a/b","model":"m","roles":["builder"]}]` | `provider must be a single segment` |
  | `[{"harness":"nope","provider":"p","model":"m","roles":["builder"]}]` | `unknown harness "nope"` |
  | `[{"harness":"claude","provider":"p","model":"m","roles":[]}]` | `roles must not be empty` |
  | `[{"harness":"claude","provider":"p","model":"m"}]` | `roles must not be empty` |
  | `[{"harness":"claude","provider":"p","model":"m","roles":["reviwer"]}]` | `unknown role "reviwer"` |
  | `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tree":"sideways"}]` | `tree must be "binding" or "none"` |
  | `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"p","model":"m","roles":["reviewer"]}]` | `duplicate candidate claude/p/m at index 0 and 1` |
  | `not json` | `decode candidates` |

  Rule 6 (`CanServe` false) is unreachable with the shipped table -- every
  kind serves every role -- so do not test it; note that in a comment
  above the table.

  Test: `TestLoadAcceptsExtraArgsAndTree` -- one entry with
  `"extra_args":["--dangerously-skip-permissions"],"tree":"binding"` loads,
  and `Lookup` returns `ExtraArgs` equal to that slice and `Tree ==
  "binding"`.

  Verify: `go test ./internal/candidate/` green.

- [ ] **Step 4: `make check`, commit**

  ```bash
  git add internal/candidate/
  git commit -m "feat(candidate): load harness/provider/model candidates, no shipped defaults (#80)"
  ```

## Report

Include the `go test ./internal/candidate/ -v` summary, the `make check`
result, and `git diff --stat HEAD~1` (exactly two new files).
