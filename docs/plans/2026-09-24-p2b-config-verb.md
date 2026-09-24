# P2b: one `relevo config` verb replaces init, candidates, policy, roles, agent, client and servers

Spec: `docs/specs/2026-09-24-db-as-record-design.md` §4.5, §6 (clean break, D11). This builds on
P2a (merged), in which config and secrets live in relevo.db: `internal/config`, `config.Store`
(`Load`, `Body`, `Has`, `Secret`, `Put`, `PutSecret`, `Version`, `ImportFiles`),
`client.ParseServers`/`EncodeServers`/`EnrollLine`/`PublicComment`.

**Stop rule:** if a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit. Tests may change only as §8 sanctions.

## 1. System overview

Seven top-level verbs (`init`, `candidates`, `policy`, `roles`, `agent`, `client`, `servers`) and
their subverbs become one verb, `relevo config`. The old names are removed. Each exits 2 with one
line naming its replacement. Every user-facing text that names a removed verb, or a
`~/.config/relevo/*.json` file, is rewritten to name `relevo config`. Every existing *behaviour*
(the pick explanation, the candidate probe, agent install, client key, server enrolment) survives
under the new verb.

## 2. File structure

```
cmd/relevo/config.go        NEW: cmdConfig and its subcommands (§4)
cmd/relevo/config_test.go   NEW
cmd/relevo/main.go          dispatch: remove the 7 cases; + removedVerbs table; usage text
cmd/relevo/init.go, roles.go, agent.go, client.go   their cmd* bodies move into config.go helpers,
                            or stay as unexported helpers the config subcommands call; the files
                            may be deleted if emptied
internal/config/config.go   + Delete(sec), PutDoc(doc) (§4.3)
the text sweep in §4.5
```

## 3. Data structures

None new. The whole-config document is a JSON object keyed by section name, holding each section
present as its raw JSON body:
`{"candidates": [...], "policy": {...}, "roles": {...}, "prices": {...}, "servers": {...}, "hooks": {...}}`.
Absent sections are omitted. Secrets are never in it.

## 4. Contracts

### 4.1 The verb (`cmd/relevo/config.go`)

| form | behaviour | replaces |
|---|---|---|
| `relevo config` | Print three blocks, each under a one-line heading: **roles** (`relevo.FormatRoles(rt.RoleRegistry())`, as `cmdRoles` prints); **pick** (what `cmdPolicy` prints); **candidates** (what `cmdCandidates` prints without `--probe`). | roles, policy, candidates |
| `relevo config --probe [token...]` | exactly `cmdCandidates --probe` | candidates --probe |
| `relevo config export` | the document (§3) to stdout, indented, keys in `config.Sections` order | — |
| `relevo config import <file\|->` | read a document; `PutDoc` (§4.3); print any warnings to stderr | — |
| `relevo config get <section>[.<key>...]` | the JSON value at that path, indented. A missing section or key exits 1 with `relevo: <path>: not set`. | — |
| `relevo config set <section>[.<key>...] <json>` | Parse `<json>`. If it is not valid JSON, treat it as a JSON string. Set it at the path, creating intermediate objects. A path through a non-object exits 1. Then `Put`. With no key, the whole section is replaced. | hand edits |
| `relevo config unset <section>[.<key>...]` | Remove the key, then `Put`. With no key, `Delete(section)`. | hand edits |
| `relevo config edit` | See §4.2. | hand edits |
| `relevo config init [--force] [--no-roles]` | exactly today's `cmdInit` (P2a's version) | init |
| `relevo config roles-init [--force] [--dry-run]` | exactly today's `cmdRolesInit` | roles init |
| `relevo config agents [--kind K] [--role R] [--force] [--dry-run]` | exactly today's `cmdAgentInstall` | agent install |
| `relevo config server add <name> <url> [--fingerprint F] [--ca FILE] [--insecure]` | today's `cmdClientAddServer`, except that when no client key exists it first generates one (as `cmdClientInit` does) and prints the enrolment line | client init + add-server |
| `relevo config server rm <name>` | today's `cmdClientRmServer` | client rm-server |
| `relevo config server list` | today's `cmdServers` | servers |
| `relevo config server key` | Print the id and the enrolment line, generating the key if absent. This is what `client init` printed. | client init |
| `relevo config secret set <name>` | Read the value from stdin, trimmed. `name` is `typesafe` or `client.key`, and client.key is validated by `PutSecret`. Any other name exits 2 with the allowed names. | hand-placed key files |
| `relevo config secret rm <name>` / `secret list` | list prints names only, never values | — |

- **Flag parsing:** every subcommand uses `parseFlags`.
- **Unknown subcommand:** exits 2 with `relevo config: unknown command %q` and the usage.
- **`agent print`** is dropped with no replacement; it was a debug aid, and `relevo config agents
  --dry-run` shows what would be written.

### 4.2 `relevo config edit`

```
doc = export bytes
loop:
  write doc to a 0600 temp file (os.CreateTemp("", "relevo-config-*.json"))
  run $VISUAL, else $EDITOR, else "vi", split on spaces, with the temp path appended;
      stdin/stdout/stderr attached
  editor exit != 0 -> exit 1 "relevo config edit: editor exited <code>; nothing changed"
  read the file; empty or whitespace-only -> print "aborted; nothing changed", exit 0
  unchanged from the original -> print "no changes", exit 0
  parse and validate every section (config.Validate) -> on error print "relevo config edit: <err>"
      to stderr, set doc = the edited bytes, and go round again (the user fixes it or empties the
      file)
  PutDoc(edited) -> print warnings, then "saved (config version N)"
remove the temp file on every exit path
```

A test sets `EDITOR` to a small shell script in `t.TempDir()` that rewrites its argument. That is
allowed in CI: it is `sh`, not a harness.

### 4.3 `internal/config` additions

- `func (s *Store) Delete(sec Section) error`: `ConfigDelete` in one tx; the version bumps.
- `func (s *Store) PutDoc(doc map[Section]json.RawMessage) ([]string, error)`:
  - an unknown section name → error, with nothing written;
  - `Validate` every section first, and the first error aborts with nothing written;
  - then all `ConfigPut`s in **one** tx, so the version bumps once per changed section. That is
    acceptable; state it in the doc comment.
  - Sections not in the doc are untouched.

### 4.4 Removed verbs

In `cmd/relevo/main.go`, delete the dispatch cases for `candidates`, `policy`, `roles`, `agent`,
`init`, `client` and `servers` (lines ~371-392), and add:

```
var removedVerbs = map[string]string{
  "init": "relevo config init", "candidates": "relevo config", "policy": "relevo config",
  "roles": "relevo config", "agent": "relevo config agents",
  "client": "relevo config server", "servers": "relevo config server list",
}
```

In the `default:` case, before `unknown subcommand`, a hit prints
`relevo: "<verb>" was removed; use <replacement>` to stderr and exits 2. The rename guard's exempt
list (`guardExempt`, main.go ~300) gains nothing. The usage text (main.go 58-121) drops the seven
lines and adds one line each for `config`, `config edit|get|set|unset|export|import`,
`config init|roles-init|agents`, and `config server add|rm|list|key` / `config secret set|rm|list`.

### 4.5 The text sweep

Every non-test, non-`docs/` occurrence of `relevo policy`, `relevo candidates`, `relevo roles`,
`relevo init`, `relevo client …`, `relevo servers` and `relevo agent …` becomes the new form from
§4.1. So does every `~/.config/relevo/<file>.json` in a user-facing text, which becomes
`relevo config` or `relevo config set <section>…` as fits. The baseline, from
`grep -rn -e 'relevo policy' -e 'relevo candidates' -e 'relevo roles' -e 'relevo init' -e 'relevo client' -e 'relevo servers' -e 'relevo agent'`:

| file | count |
|---|---|
| README.md | 41 |
| cmd/relevo/client.go | 13 |
| internal/doctor/doctor.go | 7 |
| cmd/relevo/agent.go | 7 |
| internal/relevo/remote.go | 6 |
| cmd/relevo/main.go | 6 |
| internal/relevo/policy_view.go | 5 |
| internal/setup/setup.go | 3 |
| internal/relevo/runtime.go | 3 |
| cmd/relevo/roles.go | 3 |
| internal/relevo/roles_list.go | 2 |
| internal/relevo/ledger.go | 2 |
| internal/relevo/candidate.go | 2 |
| cmd/relevo/serve.go | 2 |
| CLAUDE.md | 2 |

It also covers one each in:
- internal/ui/pane.go, internal/roles/registry.go, internal/roles/init.go
- internal/remote/client/{config,client}.go
- internal/planner/handoff.md, internal/history/history.go
- internal/harness/agents/{plan-executor.codex.toml, architect.opencode.md, architect.codex.toml,
  architect.claude.md, architect.agy.md}
- cmd/relevo/{migrate,init,doctor}.go

**Also rewrite the file mentions the P2a map listed:**
- `cmd/relevo/doctor.go` Fix strings at ~200, 225-235, 467, 543, 560;
- `internal/doctor/doctor.go:227-239` (the ConfigCheck OK text), `serve.go`, `roles.go:13-39`,
  `scope.go:62`;
- `internal/relevo/policy_view.go:257-481` texts, `candidate.go:229-281`, `tier.go:48`,
  `writer_role.go:87`, `serve/bindings.go:139`;
- flag help in `main.go` that says `policy.json`/`roles.json`: say "config policy" / "config roles".

The README sections to rewrite are the ones the P2a map located:
- First run (139-222);
- Command surface (378-384, 405-413);
- Remote builders client (735-749);
- Candidates (1247-1346), Policy (1347-1449, including the typesafe key at 1419-1423, now
  `relevo config secret set typesafe`), Roles (1450-1537);
- Lifecycle hooks (2142-2190, now argv lists in `config set hooks.<event> '[["/path/script"]]'`).

State that config lives in relevo.db, and that a file dropped into `~/.config/relevo` is imported
on the next command.

**CLAUDE.md lines 15-17:** change to say candidates, roles and policy live in the DB, that
`relevo config` shows and edits them, and that `relevo config` shows the current pick.

**handoff.md and the architect definitions:** change "`relevo policy` explains the current pick
and why" to "`relevo config` shows the current pick and why". Then regenerate with
`scripts/agents-shipped.sh --write` (this updates `internal/harness/agents/shipped.sha256`); the
test that pins the architect copies to handoff.md must pass.

After the sweep, the baseline grep above returns hits only in `docs/`, in tests that assert the
removed-verb refusal, and in `removedVerbs` itself.

## 5. Pseudocode

In §4.1 and §4.2.

## 6. Error handling

| failure | behaviour |
|---|---|
| a bad path, a non-object in the path, or unknown section | exit 1 with the path |
| validation failure | exit 1 with the parser's error; nothing written |
| a removed verb | exit 2 naming its replacement |
| the DB schema is newer | writers exit 1 with P2a's ErrNewerSchema text |

## 7. Working efficiently

- **Read in one batch:**
  - `cmd/relevo/{main.go 58-121, 289-403, 736-821; init.go; roles.go; agent.go; client.go}`;
  - `internal/config/config.go`.
- **The sweep:** script it. Build the file list from the baseline grep, then edit each file in one
  call. Read the README ranges once.
- **Focused loop:** `go test ./cmd/relevo/ ./internal/config/ ./internal/relevo/ ./internal/doctor/ ./internal/harness/ ./internal/planner/ -count=1`.
- **Full check** once at the end: `make check`, then `make e2e`.
- **CI has no harness and no network.** `config --probe` and `config server add`'s enrolment check
  are **not** run in tests; test the dispatch and the refusal paths only. `config agents` uses the
  fake InstallEnv that `init_test.go` already uses.

## 8. Ordered steps

**Closed deletion list:**
- **D1:** the seven dispatch cases.
- **D2:** `agent print` (`cmd/relevo/agent.go` cmdAgentPrint and its test, if any).
- **D3:** `cmdClientInit`'s standalone form (now `config server key`).

Behaviour tests of the moved commands (`init_test.go`, roles, client, candidates, policy tests in
cmd/relevo) are **ported** to call the new forms. A test's assertions change only in the verb it
invokes and in the texts §4.5 rewrote. Tests asserting old help or usage text are ported to the
new text.

1. **`config.Delete`/`PutDoc` + tests.**
2. **`cmd/relevo/config.go`** with every §4.1 form, reusing the moved bodies, plus `edit` (§4.2).
   Tests in `config_test.go`:
   - get/set/unset round-trips (set then get; set of an invalid policy refused, nothing changed);
   - export then import is identity;
   - edit with an EDITOR script: one run that changes a value, one that empties the file (abort),
     and one that writes invalid JSON first and then valid JSON on the second invocation (the
     script can count its runs in a file);
   - `server key` twice prints the same id;
   - `secret set typesafe` from stdin, then `secret list` shows the name but not the value.
3. **Dispatch:** remove D1, add `removedVerbs`, the usage text, and one table test over all seven
   removed names (exit 2, replacement named).
4. **The §4.5 sweep**, then `scripts/agents-shipped.sh --write`.
5. **Full check.** Report:
   - the tails;
   - `git diff --stat`;
   - the post-sweep grep output;
   - every deleted test with its D-number;
   - every ported test with a one-line reason.

   **Commit** as one commit on the branch.
