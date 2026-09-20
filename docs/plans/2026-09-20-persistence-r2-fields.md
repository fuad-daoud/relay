# Persistence round 2: the facts `bind.json` must carry -- repo, feature, fork parent, planner transcript locator

Spec: `docs/specs/2026-09-20-persistence-design.md` (§3 decisions 3-6;
§5.4 fields). Issue #172 (gap list points 3 and 4).

This plan stands alone: everything you need is in this file and in the
tree. If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

## 1. System overview

relay's binding record (`~/.local/state/relay/<name>/bind.json`) says which
directory a binding works in but not which repository that is; it records a
fork only as free text in the child's log; it has no human-given grouping;
and its planner is a pane id plus a herdr session id that both die with the
session. A later round ingests `bind.json` into a database and needs these
as fields. This round adds them at the three places a binding is created
(`bind`, `add`, `fork`) and, for the planner, the harness transcript path
that relay can already locate for `claude` (#228's glob). Every field is
`omitempty`; a `bind.json` written before this change round-trips
byte-identical.

## 2. File structure

```
internal/store/types.go                 + RepoRef, ForkRef; Binding.Repo, .Feature, .ForkedFrom; Endpoint.TranscriptLocator; ValidFeature()
internal/store/types_test.go            + round-trip and ValidFeature tests
internal/git/client.go                  + RepoFacts(ctx, dir) (originURL, commonDir string, err error); + NormalizeOriginURL(string) string
internal/git/client_test.go             + tests (throwaway repos with gpgsign off)
internal/relay/herdr.go                 Git interface + RepoFacts; a doc line on TranscriptLocator
internal/relay/repo.go                  captureRepo(ctx, rt, cwd) *store.RepoRef  (nil on any error, logs at Debug)
internal/relay/repo_test.go
internal/relay/bind.go                  BindOptions.Feature; set Repo, Feature, Planner.TranscriptLocator, CreatedAt
internal/relay/add.go                   AddOptions.Feature; same, local and remote (addRemote / remote.go:221)
internal/relay/fork.go                  ForkOptions.Feature; ForkedFrom{Name, Round}; Feature inherits; Repo copied from source
internal/relay/*_test.go                fakeGit gains RepoFacts; assertions on the new fields
cmd/relay/main.go                       --feature on bind, add, fork
README.md                               --feature in the bind/add/fork sections; one paragraph "What a binding records"
```

## 3. Data structures

```
// internal/store/types.go
type RepoRef struct {
    OriginURL string `json:"origin_url,omitempty"`   // normalised; "" when the repo has no origin
    CommonDir string `json:"common_dir,omitempty"`   // absolute path of the main worktree's .git (git rev-parse --git-common-dir, made absolute)
}
type ForkRef struct {
    Name  string `json:"name"`    // the source binding
    Round int    `json:"round"`   // the source round copied through
}
Binding.Repo        *RepoRef  `json:"repo_ref,omitempty"`   // NOTE: json name repo_ref -- `repo` is already taken on remote bindings (types.go: the server-side repo path)
Binding.Feature     string    `json:"feature,omitempty"`
Binding.ForkedFrom  *ForkRef  `json:"forked_from,omitempty"`
Endpoint.TranscriptLocator string `json:"transcript_locator,omitempty"`  // set on Planner only in this round
```

Before choosing the json name for `Repo`, read `internal/store/types.go`
and confirm what field currently serialises as `"repo"`; keep that one
untouched and use `repo_ref` for the new struct. If no field serialises as
`"repo"`, use `"repo"`.

`ValidFeature(s string) error`: 1..64 bytes, every byte in
`[A-Za-z0-9._ -]`, no leading or trailing space. Error text:
`feature: 1-64 chars of letters, digits, '.', '_', '-' and spaces`.

`NormalizeOriginURL`:
```
trim spaces
git@host:owner/repo(.git)      -> https://host/owner/repo
ssh://git@host/owner/repo(.git) -> https://host/owner/repo
https://host/owner/repo(.git)/  -> https://host/owner/repo
http:// stays http://; host lowercased; path case kept; trailing ".git" and "/" removed
anything else                   -> returned trimmed, unchanged
```

## 4. Interfaces

```
// internal/git
func (c *Client) RepoFacts(ctx, dir string) (originURL, commonDir string, err error)
    runs: git -C dir rev-parse --git-common-dir  (made absolute against dir when relative)
          git -C dir remote get-url origin       (exit 2 / "No such remote" -> originURL "", err nil)
    err only when dir is not a git work tree.
func NormalizeOriginURL(raw string) string

// internal/relay
type Git interface { ...existing...; RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error) }
func captureRepo(ctx context.Context, rt Runtime, cwd string) *store.RepoRef
    nil when rt.Git is nil or RepoFacts errors; OriginURL normalised; never fails the caller.
func plannerLocator(rt Runtime, kind, sessionID string) string
    rt.Sessions(kind, sessionID) when rt.Sessions != nil and ok; else "".

BindOptions.Feature, AddOptions.Feature, ForkOptions.Feature string
```

Preconditions: `--feature` is validated in `cmd/relay` before the call and
again in `Bind/Add/Fork` (`ValidFeature`), so a caller of the package gets
the same error as the CLI.

## 5. Pseudocode

```
Bind (bind.go, at the `b := store.Binding{` site ~479 and the resume path):
    b.Repo = captureRepo(ctx, rt, opts.CWD)
    b.Feature = opts.Feature
    b.Planner.TranscriptLocator = plannerLocator(rt, plannerKind, plannerSessionID)
    if b.CreatedAt.IsZero(): b.CreatedAt = now
  resume/--rebind: keep the existing Repo/Feature/ForkedFrom; set Feature only when opts.Feature != ""; refresh TranscriptLocator when empty.

Add (add.go ~213 and remote.go ~221):
    same three lines; CWD is the new worktree's parent repo (opts.CWD) -- capture from opts.CWD, not the worktree path.

Fork (fork.go ~259):
    b.Repo = src.Repo (copy) ; if nil: captureRepo(ctx, rt, src.CWD)
    b.Feature = opts.Feature if set, else src.Feature
    b.ForkedFrom = &ForkRef{Name: opts.Source, Round: opts.Round}
    b.Planner.TranscriptLocator = plannerLocator(...)
    the existing "forked from %s at round %d" log note stays exactly as is.

cmd/relay: fs.String("feature", "", "label grouping this binding with others (fork inherits it)") on bind, add, fork;
    ValidFeature before calling; exit 2 with the error text on failure.
```

## 6. Error handling

- `RepoFacts` failing never fails a bind: `captureRepo` returns nil and
  logs at `Debug` (`slog.Debug("repo facts", "cwd", cwd, "err", err)`).
- A bad `--feature` is a usage error: exit 2, the `ValidFeature` text.
- No new state transitions; nothing here touches the daemon.

## 7. Ordered implementation steps

### Task 1 -- store types

**Files:** `internal/store/types.go`, `types_test.go`.

**Tests**
- `TestBindingNewFieldsRoundTrip`: marshal a Binding with all four new fields set, unmarshal, `reflect.DeepEqual`.
- `TestBindingWithoutNewFieldsIsByteIdentical`: take an existing test fixture Binding (or build one with none of the new fields), marshal, assert the JSON contains none of `repo_ref`, `feature`, `forked_from`, `transcript_locator`.
- `TestValidFeature`: table -- `auth`, `api v2`, `x.y_z-1` ok; ``, 65 bytes, ` lead`, `trail `, `a/b`, `é` rejected.

**Verify:** `go test ./internal/store/`.

### Task 2 -- git facts

**Files:** `internal/git/client.go`, `client_test.go`.

**Tests** (create repos with `git -c commit.gpgsign=false -c tag.gpgsign=false init` in `t.TempDir()`; skip when `git` is not on PATH, matching the package's existing convention):
- `TestRepoFactsNoRemote`: origin "", commonDir == `<repo>/.git` (absolute).
- `TestRepoFactsWithOrigin`: after `git remote add origin git@github.com:o/r.git` -> originURL is the raw value (normalisation is the caller's), commonDir as above.
- `TestRepoFactsFromWorktree`: `git worktree add` a second tree; `RepoFacts(worktree)` returns the main tree's `.git`.
- `TestRepoFactsNotARepo`: err non-nil.
- `TestNormalizeOriginURL`: table over the five forms in §3 plus an unrecognised string.

**Verify:** `go test ./internal/git/`.

### Task 3 -- relay: capture at bind/add/fork

**Files:** `internal/relay/herdr.go`, `repo.go`, `repo_test.go`, `bind.go`, `add.go`, `remote.go`, `fork.go`, and every `fakeGit` in `internal/relay/*_test.go` (add a `RepoFacts` method returning configured values; default origin "" / commonDir `<cwd>/.git`).

**Tests**
- `TestBindRecordsRepoFeatureAndLocator`: fakeGit returns `git@github.com:o/r.git`; `rt.Sessions` returns `/home/x/.claude/projects/slug/S.jsonl`; after `Bind` the saved binding has `Repo{OriginURL: "https://github.com/o/r", CommonDir: ...}`, `Feature: "auth"`, `Planner.TranscriptLocator` set, `CreatedAt` non-zero.
- `TestBindRepoFactsFailureIsNil`: fakeGit errors -> `Repo == nil`, bind succeeds.
- `TestBindRejectsBadFeature`: `Feature: "a/b"` -> error containing `feature:`.
- `TestAddRecordsRepoFromCWDNotWorktree`.
- `TestForkInheritsFeatureAndRecordsParent`: source has `Feature: "auth"`, fork with none -> child `Feature: "auth"`, `ForkedFrom{Name: src, Round: 2}`, `Repo` equal to the source's; the log still has the `forked from` note.
- `TestForkFeatureOverride`: fork with `Feature: "auth-2"` -> child has it.
- `TestResumeKeepsFieldsAndRefreshesLocator`.

**Verify:** `go test ./internal/relay/`.

### Task 4 -- CLI and README

**Files:** `cmd/relay/main.go` (`cmdBind` ~589, `cmdFork` ~723, `cmdAdd` ~794), `README.md`.

README: add `--feature` to each verb's flag list, and one paragraph
"What a binding records" listing the four new facts in plain words and
that they exist for the coming history database.

**Tests:** none in `cmd/relay` (a test here would execute a subcommand that reaches herdr; CI has no herdr binary -- the rule is tested in `internal/relay` in Task 3).

**Verify:** `relay bind --help` shows `-feature`; `HERDR_PANE_ID=... relay add --name t --headless --feature 'a/b'` exits 2 with the feature message (do this only if a herdr session is available; otherwise say so).

### Task 5 -- full check and commit

Run: `gofmt -l .` (nothing), `go vet ./...`, `go test -race -count=1 ./...`,
`go mod tidy && git diff --exit-code go.mod go.sum`, then `make check`.
Known: `scripts/plugin-build_test.sh` case 3 fails on a clone without tags
(#242) -- if that is the **only** failure and `git tag` prints nothing, say
so in the report and treat the check as passed.

**Commit** (one for the round):
`feat(store): bindings record repo, feature, fork parent and the planner transcript locator (#172)`

## Report

Per task: what was done, the test names, the verify result. Then the commit
sha and the `make check` result. Say which json name you used for
`Binding.Repo` and why. If any step was impossible as written, say which
and stop there.
