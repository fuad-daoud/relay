# Plan: #386 round 1: `internal/chatlabel` and a `chat` column in `relevo planner list`

## 1. System overview

relevo names planners `architect-11`, `claude-7` and so on. Nothing a person sees
on screen matches those names. Every planner record already stores
`HarnessKind`, `SessionID` and `TranscriptLocator` (the harness transcript path;
`internal/planner/planner.go:69-87`). This round adds:

- a new package, `internal/chatlabel`, which turns what is already recorded into
  the harness's own human-facing name for that session (a chat title or the last
  prompt, plus a claude.ai link when there is one); and
- a `chat` column in `relevo planner list`, with two matching `--json` fields.

Round 2, a separate plan, reuses this package in `relevo status`, doctor and the
statusline. **Do not touch those surfaces this round.**

Constraints, all from the issue:

- **Read-only and local.** A label is computed when the command runs and only
  printed. It is never written to relevo state, never logged, and never
  committed. Fixtures must be synthetic: no real session ids, account or
  organization UUIDs, or real prompts.
- **Cheap.** A transcript is read only from its last 256 KiB. Across the 60
  largest transcripts on the planner's machine, the latest entry of every type
  used here sat within 35 KB of the end. Claude Code re-appends these entries
  throughout a session.
- **Never an error.** Anything unreadable gives the empty label, printed as `-`.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report. A halt that surfaces a design error is worth more than
a green suite that bent a test to fit.

## 2. File structure

```
internal/chatlabel/
  chatlabel.go        Label type, String(), text helpers (trim/quote/truncate)
  claude.go           Claude(tail []byte) Label: the claude transcript rule
  opencode.go         Opencode(out []byte) Label, OpencodeQuery(sessionID) string
  tail.go             ReadTail(path string, max int64) ([]byte, error)
  resolve.go          Resolver + Resolve(ctx, kind, sessionID, locator) Label
  chatlabel_test.go   all package tests
  testdata/
    claude-prompt.jsonl     bridge-session + several last-prompt lines
    claude-titled.jsonl     custom-title (twice, different values) + ai-title + last-prompt
    claude-aititle.jsonl    ai-title + last-prompt, no custom-title
    claude-none.jsonl       only user/assistant lines
    claude-garbled.jsonl    a truncated JSON line and a non-JSON line between valid ones
cmd/relevo/planner.go       cmdPlannerList (lines 275-325) + plannerListView (~332-338)
cmd/relevo/planner_test.go  one new CLI test
README.md                   one paragraph after line 251
```

`internal/chatlabel` imports only the standard library and `internal/usage` (for
`usage.Exec`). It must **not** import `internal/relevo` or `internal/planner`.
Round 2 has `internal/relevo` import it, and that would be a cycle.

## 3. Data structures

### `chatlabel.Label` (`chatlabel.go`)

| field | type | meaning |
|---|---|---|
| `Text` | `string` | What a person recognises: a chat title (bare) or the last prompt (wrapped in `"`). `""` means nothing readable. Never longer than `MaxTextRunes` runes, excluding the two quote characters. |
| `Link` | `string` | A full `https://claude.ai/code/session_<X>` URL, or `""`. |

Constants:

- `MaxTextRunes = 50`
- `DefaultTailBytes int64 = 256 << 10`
- `OpencodeTimeout = 2 * time.Second`

### `plannerListView` (`cmd/relevo/planner.go` ~332)

Add two fields after `Bindings`:

- `ChatLabel string` with tag `json:"chat_label,omitempty"`: `Label.Text`.
- `ChatLink string` with tag `json:"chat_link,omitempty"`: `Label.Link`.

## 4. Interfaces and contracts

### `func (l Label) String() string`

| Text | Link | returns |
|---|---|---|
| empty | empty | `-` |
| set | empty | `Text` |
| empty | set | `Link` |
| set | set | `Text + " · " + Link` (U+00B7 with a space each side) |

### Text helpers (unexported, `chatlabel.go`)

`clean(s string) string` returns `""` for input that is empty after trimming.
Otherwise it:

1. takes the first non-empty line of `s`;
2. collapses every run of whitespace to one space and trims;
3. truncates to `MaxTextRunes` runes: when it cuts, it keeps `MaxTextRunes-1`
   runes and appends `…`.

Title text is `clean(title)`. Prompt text is `"` + `clean(prompt)` + `"`, and
stays `""` when `clean` returns `""`.

### `func Claude(tail []byte) Label` (`claude.go`)

The input is the bytes of a Claude Code transcript (JSONL). The function never
panics and never errors.

Walk the input line by line. Skip any line that, once trimmed, is empty, does not
start with `{`, or fails `json.Unmarshal` into this struct:

```
{ Type string `json:"type"`; CustomTitle string `json:"customTitle"`; AITitle string `json:"aiTitle"`;
  LastPrompt string `json:"lastPrompt"`; BridgeSessionID string `json:"bridgeSessionId"` }
```

For each type below, keep the **latest** (last in file order) non-empty value of
its field:

| `type` | field |
|---|---|
| `custom-title` | `customTitle` (set by `/rename`; it can happen more than once, and the last one wins) |
| `ai-title` | `aiTitle` (the automatic chat title) |
| `last-prompt` | `lastPrompt` |
| `bridge-session` | `bridgeSessionId` |

Result:

- `Text` is the first of these that is non-empty:
  1. `clean(latest customTitle)`;
  2. `clean(latest aiTitle)`;
  3. the quoted `clean(latest lastPrompt)`;
  4. `""`.
- `Link`: when the latest `bridgeSessionId` starts with `cse_` and has more after
  it, `Link = "https://claude.ai/code/session_" + <everything after "cse_">`.
  Otherwise it is `""`.

### `func ReadTail(path string, max int64) ([]byte, error)` (`tail.go`)

- `max <= 0` means `DefaultTailBytes`.
- Open the file and stat it.
  - If size ≤ max, return the whole content.
  - Otherwise read the last `max` bytes and drop everything up to and including
    the first `\n`, because that line is partial. If there is no `\n`, return an
    empty slice.
- Errors (open, stat, read) are returned as-is. The caller turns them into the
  empty label.

### `OpencodeQuery` and `Opencode` (`opencode.go`)

`func OpencodeQuery(sessionID string) string` returns:

    select title from session where id = '<sessionID with ' doubled>'

`func Opencode(out []byte) Label` takes the stdout of that query. Its first line,
after trimming, is the title: `Text = clean(title)`, and there is no link.
Empty output gives `Label{}`.

A local `var opencodeSessionID = regexp.MustCompile("^ses_[A-Za-z0-9]+$")`
duplicates `internal/relevo/deliver_opencode.go`'s pattern. Add a comment saying
so and why (the import cycle).

### `Resolver` (`resolve.go`)

```
type Resolver struct {
    Exec       usage.Exec // the sqlite3 shell-out; nil -> opencode labels are empty
    OpencodeDB string     // path to opencode.db; "" -> opencode labels are empty
    TailBytes  int64      // 0 -> DefaultTailBytes
}
func (r Resolver) Resolve(ctx context.Context, kind, sessionID, locator string) Label
```

Dispatch on `kind`:

- **`"claude"`:**
  1. If `locator == ""`, return `Label{}`.
  2. Call `ReadTail(locator, r.TailBytes)`. On error, return `Label{}`.
  3. Return `Claude(tail)`.
- **`"opencode"`:**
  1. If `r.Exec == nil`, or `r.OpencodeDB == ""`, or `sessionID` does not match
     `opencodeSessionID`, return `Label{}`.
  2. Create a child context with `OpencodeTimeout`.
  3. Run `r.Exec.Run(ctx, "sqlite3", "-readonly", r.OpencodeDB, OpencodeQuery(sessionID))`.
     On error, return `Label{}`.
  4. Return `Opencode(out)`.
- **Any other kind** (including `"agy"`): return `Label{}`. agy's conversation
  metadata has no verified readable title today. Add a one-line comment saying so.

## 5. Pseudocode: `cmdPlannerList` (`cmd/relevo/planner.go:275-325`)

```
records := registry.List()              // unchanged
counts  := plannerBindingCounts(rt)     // unchanged
res     := chatResolver()               // new helper in cmd/relevo/planner.go
labels  := for each rec: res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)
if --json: each view also sets ChatLabel = labels[i].Text, ChatLink = labels[i].Link
table header: "name\tchat\tid\tkind\tsession\thost pid\tstate\tbindings\tcwd\tseen"
table row:    rec.Name, labels[i].String(), <the existing columns, unchanged order>
```

`seen` stays the last column. `TestListAlignsLongValues` depends on that.

`func chatResolver() chatlabel.Resolver`:

- If `exec.LookPath("sqlite3")` succeeds, return `Resolver{Exec: binExec{}, OpencodeDB: opencodeDBPath()}`.
- Otherwise return `Resolver{}`.

`binExec` is in `cmd/relevo/exec.go:13`, and `opencodeDBPath` is in
`cmd/relevo/main.go:488`. Reuse both. Do not write new ones.

## 6. Error handling

Every failure inside `chatlabel` ends in `Label{}`: a missing or unreadable file,
a garbled line, a missing sqlite3, an absent DB or row, or a timeout. `planner
list` never fails and never prints to stderr because of a label. There is no
logging.

## 7. Tests

The package tests go in `internal/chatlabel/chatlabel_test.go`. Build fixture
lines synthetically (for example `cse_01TESTTESTTESTTEST`), not from any real
transcript.

1. **`TestClaudeLatestPromptAndLink`** uses `claude-prompt.jsonl`, which has three
   `last-prompt` lines (`"first"`, `"second"`, then a 70-rune prompt with a
   leading blank line) and a `bridge-session` line.
   - `Text` is the quoted, 50-rune-truncated third prompt ending in `…`.
   - `Link` is `https://claude.ai/code/session_01TESTTESTTESTTEST`.
2. **`TestClaudeCustomTitleWins`** uses `claude-titled.jsonl`: `custom-title` "old
   name", then `ai-title` "auto", then `custom-title` "new name", then a
   `last-prompt`. `Text == "new name"`, unquoted.
3. **`TestClaudeAITitleBeatsPrompt`** uses `claude-aititle.jsonl`.
   `Text == "auto title"`.
4. **`TestClaudeNothing`** uses `claude-none.jsonl`. It checks `Label{}` and
   `String() == "-"`.
5. **`TestClaudeGarbledLinesSkipped`** uses `claude-garbled.jsonl`. The valid
   `last-prompt` after the bad lines still wins, and nothing panics.
6. **`TestClaudeBridgeWithoutPrefix`**: a `bridgeSessionId` without `cse_` gives
   `Link == ""`.
7. **`TestReadTail`** writes a temp file larger than `max`:
   - the returned bytes start at a line boundary;
   - a file ≤ max comes back whole;
   - a missing path returns an error.
8. **`TestLabelString`** covers the four rows of the `String()` table.
9. **`TestOpencode`**:
   - `Opencode([]byte("My session\n"))` gives `Text == "My session"`;
   - empty output gives `Label{}`;
   - `OpencodeQuery("ses_a'b")` doubles the quote.
10. **`TestResolveOpencode`** uses a fake `usage.Exec` that records its arguments
    and returns `"Planner round 2\n"`.
    - `Resolve(ctx, "opencode", "ses_abc123", "")` gives that title, and the fake
      saw `sqlite3 -readonly <db> <query>`.
    - An invalid session id (`"bad id"`) and a nil Exec each give `Label{}` without
      calling the fake.
11. **`TestResolveOther`**:
    - `Resolve(ctx, "agy", "x", "/some/path")` gives `Label{}`;
    - `Resolve(ctx, "claude", "x", "/does/not/exist")` gives `Label{}`.

CLI test in `cmd/relevo/planner_test.go`: **`TestListShowsChat`**. Model it on
`TestListAlignsLongValues` (line 286):

1. Set a temp `XDG_STATE_HOME`. The package's TestMain already isolates HOME and
   the config dirs.
2. Write a synthetic claude transcript under `t.TempDir()` with a `custom-title`
   and a `bridge-session`.
3. Create one record with that path as `TranscriptLocator` and a second record
   with no locator.
4. Check `planner list`:
   - the header contains `chat`;
   - the first record's row contains `<title> · https://claude.ai/code/session_…`;
   - the second record's row has `-` in its chat cell.
5. Check `planner list --json`: `chat_label` and `chat_link` are set on the first
   record and absent on the second.

**This test must spawn nothing and reach no network. Use claude records only**
(CI runners may lack sqlite3, and an opencode record would shell out).

**Mutations.** Apply each one, run
`go test -count=1 ./internal/chatlabel/`, confirm the named test fails, then
revert. List every mutation and its failing test in the report.

- **M1:** keep the **first** `last-prompt` rather than the last.
  `TestClaudeLatestPromptAndLink` fails.
- **M2:** keep the **first** `custom-title` rather than the last.
  `TestClaudeCustomTitleWins` fails.
- **M3:** drop the `cse_` → `session_` mapping, so `Link` uses the raw id.
  `TestClaudeLatestPromptAndLink` fails.
- **M4:** check `last-prompt` before `ai-title`. `TestClaudeAITitleBeatsPrompt`
  fails.
- **M5:** in `ReadTail`, stop dropping the partial first line. `TestReadTail`
  fails.

## 8. Working efficiently

Each model step costs a full round trip, so:

- Batch independent reads and edits as parallel tool calls in one step.
- Read once, from the locations named above. They are already located; do not
  re-find them.
- Make every change to a file in one edit call. Write each new file in one call.
- **Focused loop:**
  - `go test -count=1 ./internal/chatlabel/`
  - `go test -count=1 -run 'TestList' ./cmd/relevo/`
  - Fix every reported error before the next run.
- **Once at the end:** `make check` (gofmt over tracked files, go vet, the
  `go mod tidy` check, `go test -race ./...`) and `sh scripts/check-name.sh`. New
  files must not contain the old project name.

## 9. Ordered steps

1. **Package core.** Deliverable: `chatlabel.go`, `claude.go`, `tail.go`,
   `opencode.go` and `resolve.go`, as specified in §3–§4. Verify:
   `go build ./internal/chatlabel/`.
2. **Package tests and fixtures.** Deliverable: §7 tests 1–11 and the
   `testdata/` files. Verify: `go test -count=1 ./internal/chatlabel/` passes.
3. **Mutations M1–M5.** Apply each, observe its test fail, revert. Verify: the
   package tests pass again after the last revert.
4. **CLI.** Deliverable: `chatResolver`, the view fields, and the header/row
   change in `cmdPlannerList` (§5), plus `TestListShowsChat`. Verify:
   `go test -count=1 -run 'TestList' ./cmd/relevo/`. `TestListAlignsLongValues`
   must still pass **unmodified**. If it fails, stop and report; do not edit it.
5. **README.** After the paragraph ending at line 251 ("Run `relevo planner list`
   to see the planners relevo knows."), add one paragraph:
   - Its `chat` column names each planner as a person sees it:
     - Claude Code: the chat's title, or its last prompt, plus the claude.ai link
       when the session is bridged;
     - opencode: the session title;
     - otherwise `-`.
   - The label is read from the harness's own files when the command runs, and
     is never stored.
   - `relevo planner rename <id|name> <new-name>` gives a planner a name of your
     own.

   Verify: `sh scripts/check-name.sh`.
6. **Full check.** `make check` must pass. Then make one commit:

       feat(planner): planner list shows which chat each planner is (#386)

   Do not push. The report lists:
   - the files changed;
   - each mutation and the test that failed for it;
   - the `make check` result.
