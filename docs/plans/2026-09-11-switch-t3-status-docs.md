# Switch T3: status shows switches, `relay unavailable` says what will switch, docs (#61 step 6)

**Design spec:** `docs/specs/2026-09-11-builder-switching-design.md` -- read §1, §3.4, §4.6, §4.7, §6
**Issue:** #61 (step 6)
**Depends on:** Switch T1 and T2 -- both already in this tree. Confirm with `grep -n switchGrace internal/relay/switch.go`; stop if missing.

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
- `BindingsOnProvider` is pure and tested in `internal/relay`; the
  `cmdUnavailable` line is glue and has no subcommand test (it reaches
  the store, not herdr, but the convention is one rule: no `cmd/relay`
  test that needs a runtime).
- Every command, flag and key you write in prose must exist; check
  `relay help` and the flag help in `cmd/relay/main.go`.
- Every new exported symbol gets a doc comment in the house style.
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: surfaces and docs

**Files:**
- Modify: `internal/relay/status.go`, `internal/relay/status_test.go`
- Modify: `internal/relay/ledger.go`, `internal/relay/ledger_test.go`
- Modify: `cmd/relay/main.go` (`cmdUnavailable`)
- Modify: `README.md`, `CLAUDE.md`, `docs/design.md`

**Interfaces consumed:** `Binding.RoundSwitches`, `Binding.RoundStartedAt`,
`Binding.BuilderCandidate`, `store.StateActive`, `candidate.ParseRef`,
`rt.Store.List()`, the existing `Status`/`RenderStatus` and
`cmdUnavailable`.

**Interfaces produced** (spec §3.4, §4.6):

```go
// internal/relay/status.go, BindingStatus gains:
Switches int `json:"switches,omitempty"`

// internal/relay/ledger.go
func BindingsOnProvider(bindings []store.Binding, provider string) []string
```

- [ ] **Step 1: status** (spec §3.4, §4.7)

  `BindingStatus`: add `Switches int` with the comment: builder switches
  in the current round (#61 step 6); zero is omitted. In `Status`, where
  `BindingStatus` is assembled from a `store.Binding`, set `Switches:
  b.RoundSwitches`. In `RenderStatus`, on the builder line (the one that
  prints the pane, kind, status and the backticked candidate), append
  `   switched %dx` when `Switches > 0`.

  Test in `status_test.go`: find the existing `RenderStatus` fixture test
  (grep `RenderStatus(`) and add a case, or `TestRenderStatusShowsSwitches`:
  a `Report` with one binding whose `Switches: 1` renders a builder line
  containing `switched 1x`, and the same with `Switches: 0` does not
  contain `switched`.

  Run: `go test ./internal/relay/ -run Status`. Expected: green.

- [ ] **Step 2: `BindingsOnProvider` and the CLI line** (spec §4.6)

  `internal/relay/ledger.go`:

  ```go
  // BindingsOnProvider names the active bindings with an open round whose
  // builder runs on provider, sorted: the ones the daemon will switch once
  // that provider is gated (spec §4.6). Pure, for cmdUnavailable's note.
  func BindingsOnProvider(bindings []store.Binding, provider string) []string
  ```

  Conditions, all required: `State == StateActive`, `!RoundStartedAt.IsZero()`,
  `BuilderCandidate != ""`, `ParseRef(BuilderCandidate)` succeeds with
  `.Provider == provider`. `sort.Strings` the names. Returns `nil` when
  none.

  Test `TestBindingsOnProvider` in `ledger_test.go`: five hand-built
  bindings -- active+open on `anthropic` (in), active+open on `google`
  (out), active but no round (out), `needs_you`+open on `anthropic` (out),
  adopted (`BuilderCandidate == ""`) active+open (out) -- and one more
  active+open on `anthropic` named so the sort is checked (`"b-web"`,
  `"a-api"` → `["a-api", "b-web"]`).

  `cmd/relay/main.go` `cmdUnavailable`: after the existing `gated ...`
  print, `if bs, err := rt.Store.List(); err == nil { if names :=
  relay.BindingsOnProvider(bs, provider); len(names) > 0 {
  fmt.Printf("the daemon will switch: %s\n", strings.Join(names, ", ")) }
  }`. A `List` error is ignored here: the gate was recorded, which is
  what the command promised.

  Run: `go build ./... && go test ./internal/relay/ -run BindingsOnProvider`.
  Expected: green.

- [ ] **Step 3: README** (spec §1, §6)

  Under `### Availability`, after the paragraph that begins
  `bind`/`add`/`fork`/`ask` with an explicit token print a `note:`, add:

  ````
  #### Mid-round switching

  A builder relay spawned can be replaced by the daemon while a round is
  open, in two cases:

  - its pane is gone for 30 seconds (a detection flicker shorter than
    that clears itself);
  - you gate its provider with `relay unavailable` -- which is how you
    tell relay a running builder hit its limit. The command names the
    bindings the daemon will switch.

  The daemon resolves `builder` again through `policy.json` order and the
  ledger (an omitted token, so the order applies even to a builder you
  named), closes the replaced pane if it is still open, starts the pick
  beside the planner in the **same** tree, and hands it the **same**
  round's plan. The round number does not change; the round clock
  restarts. The new builder inherits whatever the old one left in the
  tree. A `switch` entry in the log says what was tried and why:

  ```
  switched builder (rate-limited: 5h window): picked opencode/openrouter/z-ai/glm-5.3-flash for builder: order #3; skipped claude/anthropic/sonnet (rate-limited until cleared)
  ```

  `relay status` shows `switched 1x` on the builder line. After
  `max_switches` replacements in one round (default 2; set it in
  `policy.json`, `0` turns switching off), or when nothing ungated
  serves `builder`, the binding goes `NEEDS YOU` with the reason, and
  recovers on its own once `relay available` clears a provider. A
  failed replacement spawn counts as a switch and the daemon walks to
  the next candidate.

  Adopted builders (bound by pane id) are never switched; a builder
  gone between rounds is `BROKEN` as before -- `relay bind --resume`.
  ````

  In `### Policy`, extend the JSON example and the sentence after it:
  the file may also carry `"max_switches": 2`; say what it bounds.

- [ ] **Step 4: CLAUDE.md and design.md**

  CLAUDE.md, "Working with builders", the bullet beginning `relay closes
  a pane only in `relay reap``: rewrite to

  ```
  - relay closes a pane in exactly two places: `relay reap` (a terminal
    consult pane it spawned) and a mid-round builder switch (the replaced
    builder's pane, when it is still open). After an `unbind`, a
    mis-bind, or any `--assume-dead` rebind, close the orphaned builder
    pane yourself with `herdr pane close <id>` or it holds memory
    indefinitely (an idle opencode builder is roughly 800 MB).
  ```

  and add a bullet after it:

  ```
  - When a builder reports a usage limit mid-round, `relay unavailable
    <token>` is enough: the daemon switches the binding to the next
    ungated candidate and resends the round. Do not rebind by hand unless
    `relay status` says `NEEDS YOU`.
  ```

  `docs/design.md`: the `log.jsonl` entry kinds gain `"switch"`, with
  one line: a `switch` entry is relay -> log only: the builder was
  replaced mid-round, and why. In the "Failure handling" section, add a
  row or paragraph: builder gone for 30s or its provider gated mid-round
  → the daemon switches to the next ungated candidate in `policy.json`
  order, bounded by `max_switches`; see
  `docs/specs/2026-09-11-builder-switching-design.md`.

- [ ] **Step 5: `make check`, commit**

  ```bash
  git add -A internal cmd README.md CLAUDE.md docs/design.md
  git commit -m "feat: status shows builder switches; relay unavailable names what the daemon will switch; docs (#61 step 6)"
  ```

## Report

Include the `make check` result, `git diff --stat HEAD~1`, the test
functions added, and the final README "Mid-round switching" subsection as
committed.
