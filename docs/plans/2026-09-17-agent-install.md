# Agent install: relay writes its shipped definitions into the harness dirs (#174)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-09-17-agent-install-design.md`. Section numbers
below (§) refer to it.
**Issue:** #174. Closes it.
**Depends on:** nothing open.

**Goal:** `relay agent install [--kind k] [--role r] [--force] [--dry-run]`
writes every definition relay ships for every harness on `PATH` (or the
named kind) into that harness's agent directory, creating the directory,
keeping an existing file unless it is identical or `--force` is given, and
printing one line per file. Doctor's fixes point at it; both plugin
manifests run it as a build step; the README's first-run step 4 becomes one
command.

**Architecture:** A pure function `harness.Install(env InstallEnv, opts
InstallOptions) ([]InstallResult, error)` in a new
`internal/harness/install.go`, over a five-method `InstallEnv` seam
(`LookPath`, `HomePath`, `ReadFile`, `MkdirAll`, `WriteFile`) with an
`os`-backed `OSInstallEnv()`. `harness.DocEqual` is the one comparison rule,
shared with doctor. `cmd/relay/agent.go` gains `case "install"`. No new
verb: `install` is a second mode of `agent`, next to `print` (§1, decision 1).

**Tech stack:** Go 1.22. Verification is the `make check` constituent set
(runs `-race`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/agent-install` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/agent-install`, cut from `main`. |
| `~/.local/state/relay/agent-install` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Resuming a round another builder started

The branch may already carry commits from an earlier builder on this same
round. Before Task 1, run `git log --oneline main..HEAD`. A task whose
commit message is already there is **done**: run its final "Run" step to
confirm it is green, tick it, and continue with the next task. Do not redo
it, do not amend it. An untracked or modified file from an unfinished task
is yours to finish or replace as that task's steps say.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do **not** run `herdr` or `make e2e` yourself; nothing in this plan touches
the reconcile path. Tests added under `cmd/relay` in Task 3 never reach
herdr: `agent install` calls no herdr subcommand, and every test there runs
under a temporary `$HOME` and always passes `--kind` so the machine's
`PATH` is never consulted (CLAUDE.md's rule is about subcommands that reach
herdr; this one does not).

## Global constraints

- **No new verb.** The only change to `cmd/relay/main.go` is the one-line
  usage text for `agent` (Task 3). `main.go`'s `switch` is not edited.
- **One overwrite rule for every kind** (§1 decision 2): a differing file
  is kept unless `Force`; an identical file is never rewritten, even with
  `Force`.
- **`--kind` never consults `PATH`** (§1 decision 5). Only the no-kind path
  calls `LookPath`.
- **`DryRun` calls neither `MkdirAll` nor `WriteFile`** (§4.1).
- **Per-file failures are `error` outcomes, never a returned error** (§4.1).
  `Install` returns an error only for unknown kind, unknown role, or a
  `HomePath` failure.
- **Results are ordered** kinds by `harness.All()` (sorted by kind), roles
  by `Harness.Roles` table order (§4.1).
- **Written bytes are `AgentDoc` untrimmed** (§4.1 postcondition); trimming
  is only for comparison (`DocEqual`, §4.2).
- Outcome strings are exactly the §3.2 table; `Line()` output is exactly
  §3.3. The CLI's empty-selection line is exactly §4.3.
- Exit codes (§4.3): selection/home error 2; any `error` outcome 1; else 0,
  including the empty-selection case.
- The `set model: inherit in <path>` doctor fix is not changed (§4.4).
- One commit per task, on the worktree's branch.

---

### Task 1: `harness.Install` over a fake `InstallEnv`

**Files:**
- Create: `internal/harness/install.go`
- Create: `internal/harness/install_test.go`

**Interfaces:**
- Consumes: `harness.All()`, `harness.Lookup(kind)`, `(Harness).Role(name)`,
  `(Harness).RoleNames()`, `harness.AgentDoc(role, kind)`, `Role.Path`,
  `Harness.Binary`, `Harness.Roles` -- all existing in `harness.go` /
  `agents.go`.
- Produces (all in package `harness`):
  - `type InstallEnv interface { LookPath(binary string) (string, error); HomePath(rel string) (string, error); ReadFile(path string) ([]byte, error); MkdirAll(dir string) error; WriteFile(path string, data []byte) error }` (§3.4; doc comments carry each row's contract, including that `ReadFile` on an absent file returns an error satisfying `errors.Is(err, fs.ErrNotExist)`).
  - `type InstallOptions struct { Kind, Role string; Force, DryRun bool }` (§3.1).
  - `type InstallOutcome string` with constants `OutcomeWrote = "wrote"`, `OutcomeOverwrote = "overwrote"`, `OutcomeKeptIdentical = "kept (identical)"`, `OutcomeKeptDiffers = "kept (differs; --force to overwrite)"`, `OutcomeWouldWrite = "would write"`, `OutcomeWouldOverwrite = "would overwrite"`, `OutcomeError = "error"` (§3.2).
  - `type InstallResult struct { Kind, Role, Path string; Outcome InstallOutcome; Err string }` (§3.3).
  - `var ErrUnknownKind = errors.New("unknown harness kind")`, `var ErrUnknownRole = errors.New("unknown role")`.
  - `func DocEqual(shipped, installed []byte) bool` (§4.2).
  - `func Install(env InstallEnv, opts InstallOptions) ([]InstallResult, error)` (§4.1, flow §5.1).

- [ ] **Step 1: Write the fake env and the failing tests** in
  `install_test.go`. The fake is a struct with `lookPaths map[string]string`
  (binary -> path; absent means `LookPath` returns an error), `home string`
  (`HomePath` returns `filepath.Join(home, rel)`; when `homeErr != nil`
  returns it), `files map[string][]byte` (`ReadFile` returns
  `fs.ErrNotExist` when the key is absent; `readErr` if set overrides for
  every read), `dirs []string` (appended by `MkdirAll`; `mkdirErr` if set is
  returned instead), `writes map[string][]byte` (recorded by `WriteFile`
  and also stored into `files`; `writeErrFor map[string]error` returns that
  error for a matching path). A helper `freshEnv()` returns one with
  `home: "/home/u"` and empty maps. Tests, one function each, named as
  listed; every assertion is against the exact §3.2 strings via the
  constants:

  1. `TestInstallFreshHomeWritesEveryRoleOfPathKinds`: `lookPaths` has
     `agy` and `claude` only; `Install(env, InstallOptions{})`. Assert: nil
     error; results are exactly, in order, agy's four roles then claude's
     four (kind order is `All()` -- agy, claude -- and role order is each
     `Harness.Roles`); every `Outcome == OutcomeWrote`; no result has
     `Kind == "opencode"`; for each result `env.files["/home/u/"+Path]`
     equals `AgentDoc(Role, Kind)` with `bytes.Equal` (untrimmed); `dirs`
     contains `/home/u/.gemini/config/agents` and `/home/u/.claude/agents`.
  2. `TestInstallKeepsIdenticalEvenWithForce`: seed
     `files["/home/u/.claude/agents/researcher.md"]` with
     `AgentDoc("researcher","claude")` plus a trailing `"\n\n"`;
     `Kind: "claude", Role: "researcher", Force: true`. Assert one result,
     `OutcomeKeptIdentical`, `len(writes) == 0`.
  3. `TestInstallKeepsDifferingWithoutForce`: seed the same path with
     `[]byte("---\nmodel: haiku\n---\nmine\n")`; `Kind: "claude", Role:
     "researcher"`. Assert `OutcomeKeptDiffers`, `len(writes) == 0`.
  4. `TestInstallOverwritesDifferingWithForce`: same seed, `Force: true`.
     Assert `OutcomeOverwrote` and `files[path]` now `bytes.Equal` to the
     shipped doc.
  5. `TestInstallDryRunTouchesNothing`: seed one identical file
     (`plan-executor`) and one differing file (`researcher`) under claude;
     leave `reviewer` and `architect` absent; `Kind: "claude", DryRun: true,
     Force: true`. Assert outcomes in role order: `OutcomeKeptIdentical`,
     `OutcomeWouldOverwrite`, `OutcomeWouldWrite`, `OutcomeWouldWrite`;
     `len(dirs) == 0`; `len(writes) == 0`. Then the same without `Force`:
     `researcher` is `OutcomeKeptDiffers`.
  6. `TestInstallNamedKindIgnoresPath`: empty `lookPaths`, `Kind: "claude"`.
     Assert four results, all `OutcomeWrote`.
  7. `TestInstallNoKindNoBinariesIsEmpty`: empty `lookPaths`,
     `InstallOptions{}`. Assert `len(results) == 0` and nil error.
  8. `TestInstallUnknownKind`: `Kind: "nope"`. Assert
     `errors.Is(err, ErrUnknownKind)`, error text contains `nope`, nil
     results.
  9. `TestInstallUnknownRole`: `Kind: "claude", Role: "nope"`. Assert
     `errors.Is(err, ErrUnknownRole)`, text contains `nope` and
     `plan-executor` (a shipped role name). Then with `Kind: ""` and empty
     `lookPaths`, `Role: "nope"`: still `ErrUnknownRole` (validated against
     every known kind when nothing is on PATH).
  10. `TestInstallWriteFailureIsPerFile`: `Kind: "agy"`,
      `writeErrFor["/home/u/.gemini/config/agents/researcher.md"] =
      errors.New("read-only")`. Assert nil error, four results, the
      `researcher` one has `OutcomeError` and `Err == "read-only"`, the
      other three `OutcomeWrote`.
  11. `TestInstallHomeErrorAborts`: `homeErr = errors.New("no home")`,
      `Kind: "claude"`. Assert non-nil error containing `no home`, nil
      results.
  12. `TestInstallUnreadableCountsAsDiffers`: `readErr =
      errors.New("permission denied")`, `Kind: "claude", Role:
      "reviewer"`. Assert `OutcomeKeptDiffers`; with `Force: true`,
      `OutcomeOverwrote`.
  13. `TestDocEqual`: table -- equal bytes true; shipped vs shipped+`"\n \t\r\n"`
      true; a leading `" "` difference false; a changed `model:` line false.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1
  ./internal/harness -run 'TestInstall|TestDocEqual'`: compile error,
  `Install` undefined.

- [ ] **Step 3: Implement `install.go`** exactly per §3 and §5.1. Layout:
  the interface, the options and result types, the outcome constants, the
  two sentinel errors, `DocEqual` (`bytes.Equal` after
  `bytes.TrimRight(b, " \t\r\n")` on each side), `Install`, and unexported
  `installOne` / `writeDoc`. Selection: if `opts.Kind != ""`, `Lookup` it
  (miss -> `fmt.Errorf("%w: %q", ErrUnknownKind, opts.Kind)`); else every
  `All()` entry whose `LookPath(h.Binary)` returns nil error. Role
  validation when `opts.Role != ""`: the candidate set is the selected kinds,
  or every `All()` kind when the selected set is empty; if no candidate's
  `Role(opts.Role)` hits, return `fmt.Errorf("%w: %q (known: %v)",
  ErrUnknownRole, opts.Role, <sorted union of candidates' RoleNames()>)`.
  `installOne`: `AgentDoc` failure -> `OutcomeError` with the error text
  (unreachable for a table row, but never a panic); `HomePath` failure ->
  returned error, which `Install` propagates with nil results;
  `ReadFile` -> the three-way switch in §5.1 (`errors.Is(err,
  fs.ErrNotExist)` -> absent; nil error and `DocEqual` -> identical;
  anything else -> differs). `writeDoc` does `MkdirAll(filepath.Dir(full))`
  then `WriteFile(full, shipped)`, turning either failure into
  `OutcomeError` + `Err`. Doc comments on every exported name; the
  `Install` comment states the ordering and the "per-file failures are
  outcomes" rule.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/harness`. Expected:
  PASS, including the pre-existing tests.

- [ ] **Step 5: Mutation check** (do not commit the mutations): (a) remove
  the `TrimRight` from `DocEqual` -> `TestDocEqual` and
  `TestInstallKeepsIdenticalEvenWithForce` fail; (b) make the `Force`
  branch fall through to `OutcomeKeptDiffers` ->
  `TestInstallOverwritesDifferingWithForce` fails; (c) call `LookPath`
  under `opts.Kind != ""` and skip the kind when it fails ->
  `TestInstallNamedKindIgnoresPath` fails. Revert each; re-run Step 4.
  Name the three in your report.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/install.go internal/harness/install_test.go
git commit -m "feat(harness): Install writes shipped definitions over an InstallEnv seam (#174)"
```

---

### Task 2: `OSInstallEnv` and `InstallResult.Line`

**Files:**
- Modify: `internal/harness/install.go` (append)
- Modify: `internal/harness/install_test.go` (append)

**Interfaces:**
- Consumes: Task 1's types.
- Produces: `func OSInstallEnv() InstallEnv` -- `exec.LookPath`,
  `os.UserHomeDir`+`filepath.Join`, `os.ReadFile`, `os.MkdirAll(dir,
  0o755)`, `os.WriteFile(path, data, 0o644)` (§3.4);
  `func (r InstallResult) Line() string` (§3.3).

- [ ] **Step 1: Write the failing tests.**
  - `TestInstallResultLine`: table over three results --
    `{Kind:"claude", Role:"researcher", Path:".claude/agents/researcher.md", Outcome: OutcomeWrote}` -> `"wrote  ~/.claude/agents/researcher.md"`;
    same path with `OutcomeKeptDiffers` -> `"kept (differs; --force to overwrite)  ~/.claude/agents/researcher.md"`;
    `{Path:".gemini/config/agents/reviewer.md", Outcome: OutcomeError, Err:"read-only"}` -> `"error  ~/.gemini/config/agents/reviewer.md: read-only"`.
  - `TestOSInstallEnvRoundTrip`: `t.Setenv("HOME", t.TempDir())`; `env :=
    OSInstallEnv()`; `HomePath("a/b.md")` equals `filepath.Join(home,
    "a/b.md")`; `ReadFile` of that path returns an error with
    `errors.Is(err, fs.ErrNotExist)`; `MkdirAll(filepath.Dir(p))` then
    `WriteFile(p, []byte("x"))` then `ReadFile(p)` returns `"x"`;
    `os.Stat(p)` mode perm is `0o644`; `LookPath("definitely-not-a-binary-relay-test")`
    returns an error.

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1
  ./internal/harness -run 'TestInstallResultLine|TestOSInstallEnv'`: compile
  error.

- [ ] **Step 3: Implement.** An unexported `osInstallEnv struct{}` with the
  five methods; `OSInstallEnv()` returns it. `Line()`: two `fmt.Sprintf`
  forms per §3.3, two spaces between outcome and path, `~/` prefix on
  `Path`.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/harness`. Expected:
  PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness/install.go internal/harness/install_test.go
git commit -m "feat(harness): OS-backed InstallEnv and one-line result rendering (#174)"
```

---

### Task 3: `relay agent install`

**Files:**
- Modify: `cmd/relay/agent.go` (new case in `cmdAgent`; new `cmdAgentInstall`; both usage strings)
- Modify: `cmd/relay/main.go:72` (the `agent` line of the top-level usage text only)
- Modify: `cmd/relay/agent_test.go` (append)

**Interfaces:**
- Consumes: `harness.Install`, `harness.OSInstallEnv`, `harness.InstallOptions`,
  `harness.OutcomeError`, `(harness.InstallResult).Line`, the existing
  `exitCodeErr{code: n}` and `captureOutput` test helper.
- Produces: `func cmdAgentInstall(args []string) error`.

- [ ] **Step 1: Write the failing tests** in `agent_test.go`, each starting
  with `t.Setenv("HOME", t.TempDir())` and each passing `--kind` (never
  rely on the machine's `PATH`); drive through `run([]string{"agent",
  "install", ...})` and `captureOutput` like the `print` tests:
  - `TestAgentInstallDryRunWritesNothing`: `--kind claude --dry-run`.
    Assert nil error; stdout is exactly four lines, in order
    `would write  ~/.claude/agents/plan-executor.md`, `...researcher.md`,
    `...reviewer.md`, `...architect.md`; `os.Stat(home + "/.claude")`
    reports not-exist.
  - `TestAgentInstallWritesThenKeeps`: `--kind agy`. Assert nil error; four
    `wrote  ~/.gemini/config/agents/<role>.md` lines; each file's bytes
    `bytes.Equal` to `harness.AgentDoc(role, "agy")`. Run again: four
    `kept (identical)  ~/...` lines and nil error.
  - `TestAgentInstallUnknownKindExits2`: `--kind unknown-kind`. Assert
    `exitCodeErr` code 2, empty stdout, stderr contains `unknown-kind`.
  - `TestAgentInstallUnknownRoleExits2`: `--kind claude --role nope`.
    Assert code 2, stderr contains `nope` and `plan-executor`.
  - `TestAgentInstallWriteFailureExits1`: `--kind claude`, after
    `os.MkdirAll(home+"/.claude/agents", 0o755)` and
    `os.WriteFile(home+"/.claude/agents/researcher.md", nil, 0o644)` then
    `os.Chmod(home+"/.claude/agents", 0o555)` (and `t.Cleanup` restoring
    `0o755`). Skip with `t.Skip` when `os.Geteuid() == 0` (root ignores
    mode bits). Assert code 1; stdout has one line starting `error
    ~/.claude/agents/plan-executor.md:`; `researcher` line is `kept
    (differs; --force to overwrite)  ~/.claude/agents/researcher.md` (an
    empty file differs from the shipped doc); the loop did not stop at the
    first error (four lines total).

- [ ] **Step 2: Run to verify it fails** -- `go test -count=1 ./cmd/relay
  -run TestAgentInstall`: all fail with `relay agent: unknown command
  "install"` (exit 2) or missing output.

- [ ] **Step 3: Implement.** In `cmdAgent`: `case "install": return
  cmdAgentInstall(args[1:])`; both usage strings (the no-args one and the
  `help` one) become

  ```
  usage: relay agent print   --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer|architect>]
         relay agent install [--kind <agy|claude|opencode>] [--role <name>] [--force] [--dry-run]
  ```

  `cmdAgentInstall`: `flag.NewFlagSet("relay agent install",
  flag.ContinueOnError)`, output to stderr, flags `kind`, `role`, `force`,
  `dry-run`; parse error -> `exitCodeErr{code: 2}`. Then the §5.2 flow:
  `harness.Install(harness.OSInstallEnv(), opts)`; error -> `fmt.Fprintf(os.Stderr,
  "relay: %v\n", err)` and `exitCodeErr{code: 2}`; empty results -> print
  `no harness binaries on PATH (agy, claude, opencode); nothing to install`
  on stdout, return nil; else print every `Line()` on stdout and return
  `exitCodeErr{code: 1}` if any `Outcome == harness.OutcomeError`, nil
  otherwise. In `main.go:72` change the `agent` usage line to
  `agent     print or install embedded agent role definitions (e.g. relay agent install --kind claude)`.

- [ ] **Step 4: Run** `go test -race -count=1 ./cmd/relay` and `go vet
  ./cmd/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/relay/agent.go cmd/relay/agent_test.go cmd/relay/main.go
git commit -m "feat(relay): agent install writes shipped definitions into the harness dirs (#174)"
```

---

### Task 4: doctor points at `agent install` and shares `DocEqual`

**Files:**
- Modify: `internal/doctor/doctor.go` (`roleCheck`, the missing-row `Fix`, the drift-row `Fix`, the `trim` closure)
- Modify: `internal/doctor/doctor_test.go:585`, `:792`, `:837` (expected fix strings)

**Interfaces:**
- Consumes: `harness.DocEqual` (Task 1).
- Produces: nothing new; two changed `Check.Fix` strings (§4.4).

- [ ] **Step 1: Update the three expectations first** so the tests fail:
  line 585 `want` becomes `"relay agent install --kind claude --role
  researcher"`; line 792's `c.Fix !=` string becomes `"relay agent install
  --kind agy --role reviewer"`; line 837's becomes `"relay agent install
  --kind agy --role researcher --force"`. Search the test file for any other
  `agent print` expectation (`grep -n 'agent print'
  internal/doctor/doctor_test.go`) and update it by the same two rules:
  missing -> `install --kind k --role r`; differs -> the same plus
  `--force`.

- [ ] **Step 2: Run to verify they fail** -- `go test -count=1
  ./internal/doctor`: the three (or more) tests fail on `fix = ...`.

- [ ] **Step 3: Implement** in `roleCheck`: the missing-row `Fix` becomes
  `fmt.Sprintf("relay agent install --kind %s --role %s", kind, r.Name)`
  and its comment now says the install creates the directory (drop the
  `mkdir -p` rationale, keep the #166 reference); delete the local `trim`
  closure and replace `trim(shipped) != trim(raw)` with
  `!harness.DocEqual(shipped, raw)`; the drift-row `Fix` becomes
  `fmt.Sprintf("relay agent install --kind %s --role %s --force", kind,
  r.Name)`. The pin row (`set model: %s in %s`) is untouched. `homeRel` is
  still used by `Detail` strings -- keep it.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/doctor`. Expected:
  PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat(doctor): definition fixes call relay agent install; share DocEqual (#174)"
```

---

### Task 5: plugin build step and README

**Files:**
- Modify: `herdr-plugin.toml` (after the `["./relay", "version"]` `[[build]]`)
- Modify: `from-source/herdr-plugin.toml` (same position)
- Modify: `README.md:123-151` (first-run step 4), `README.md:765-775` (reviewer section), `README.md:815-826` (architect section)

**Interfaces:**
- Consumes: the `relay agent install` CLI (Task 3).
- Produces: nothing in code.

- [ ] **Step 1: Manifests.** In both files insert, directly after the
  `[[build]]` block whose command is `["./relay", "version"]`:

  ```toml
  [[build]]
  command = ["./relay", "agent", "install"]
  ```

  No `--force` (§1 decision 3, §4.5). Run `sh
  scripts/check-plugin-version_test.sh` and `sh
  scripts/plugin-fetch_test.sh`; both must still pass (they do not inspect
  build steps, but confirm rather than assume).

- [ ] **Step 2: README first-run step 4.** Replace the whole step-4 block
  (the `Emit the builder's role definitions ...` line through the paragraph
  ending `warns when an agy copy pins a tier.`) with:

  ````
  4. Install the role definitions into each harness on `PATH` (the plugin
     does this for you at install and update):
     ```
     relay agent install
     ```
     One line per file says `wrote`, `kept (identical)` or `kept (differs;
     --force to overwrite)`. Pass `--kind` to name a harness that is not on
     `PATH` yet, `--role` for one definition, `--dry-run` to look first.
     This writes `plan-executor`, `researcher`, `reviewer` and `architect`
     for every kind; `relay agent print --kind <k> --role <r>` still emits
     one to stdout.

     `researcher` is the read-only role the builder's own sub-agents run as. It
     exists because exactly one agent may write to a working tree: research can fan
     out safely, implementation cannot. The claude and opencode definitions pin a
     `model:` in their front matter as a worked example, chosen so neither needs a
     provider the rest of relay does not already assume; that line is the first
     thing to change for your own setup, and a plain `relay agent install`
     keeps your edit. The agy definitions pin `model: inherit`
     and that is not an example: on agy the key is a tier (`inherit`, `flash`,
     `pro`) that would override the `--model` relay passes at launch. `relay
     doctor` reports the pin each installed definition carries, warns when an
     agy copy pins a tier or differs from what relay ships, and names the
     `relay agent install ... --force` that restores it.
  ````

  Keep the numbering of steps 5-9 as it is. In step 5, drop the sentence
  `(Optional) Emit the planner's definition too -- see [The planner:
  architect](#the-planner-architect).` -- install already wrote it.

- [ ] **Step 3: README reviewer section.** Replace the three-kind
  `relay agent print ... --role reviewer > ...` code block and its lead-in
  sentence (`Emit the definitions into the harness's agent directory the
  same way as the other roles:`) with:

  ````
  `relay agent install` writes the reviewer definition with the other
  roles; to install just this one:

  ```
  relay agent install --role reviewer
  ```
  ````

  Keep the following `relay doctor reports whether the definition landed`
  sentence.

- [ ] **Step 4: README architect section.** Replace the three-kind
  `relay agent print ... --role architect > ...` code block with:

  ````
  ```
  relay agent install --role architect
  ```

  (`relay agent install` with no flags writes it too.)
  ````

  Keep the sentence before it and the `--agent architect` launch examples
  after it.

- [ ] **Step 5: Run the full `make check` constituent set** from "Running
  commands". Expected: clean gofmt, vet, all packages PASS, tidy no-op.
  Then `git diff --stat main..HEAD` must list only: `internal/harness/install.go`,
  `internal/harness/install_test.go`, `cmd/relay/agent.go`,
  `cmd/relay/agent_test.go`, `cmd/relay/main.go`,
  `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`,
  `herdr-plugin.toml`, `from-source/herdr-plugin.toml`, `README.md`.

- [ ] **Step 6: Commit**

```bash
git add herdr-plugin.toml from-source/herdr-plugin.toml README.md
git commit -m "docs(relay): plugin installs definitions at build; README step 4 is one command (#174)"
```

---

## Report

Your report (`NNN-report.md`) states: each task's commit hash; the four
`make check` constituent commands and that each passed; the three Task 1
mutations by name and the test each one failed; the `git diff --stat`
against the list in Task 5 Step 5; and any place you stopped rather than
improvised, with what you found. Then create the `NNN-done` marker.
