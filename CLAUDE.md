# relay

relay automates the plan/report handoff between two AI coding agent panes
running under herdr: a planner hands work to a builder, and relay moves the
files between them.

## Working with builders

The dispatch protocol -- `relay send` not in-session subagents, headless by
default, one harness many worktrees, `relay unavailable` on a usage limit,
stop rather than improvise -- is in the shipped `architect` definition
(`internal/harness/agents/architect.*.md`, "Handing off"), not here. What
follows is what is specific to this machine and this repo.

- Candidates are in `~/.config/relay/candidates.json` (`relay candidates`
  lists them); the builder order is `order.builder` in
  `~/.config/relay/policy.json`. `relay policy` shows the current pick.
- relay closes a pane in exactly three places: `relay reap` (a terminal
  consult pane it spawned), a mid-round builder switch (the replaced
  builder's pane, when it is still open), and `relay pause` (the paused
  binding's builder pane, because the human asked). It stops a *process* in
  exactly three: `relay done` and `relay unbind` on a headless binding whose
  round is running, and a mid-round switch of a headless builder whose
  provider you gated with `relay unavailable`. After an `unbind`, a
  mis-bind, or any `--assume-dead` rebind of a pane builder, close the
  orphaned builder pane yourself with `herdr pane close <id>` or it holds
  memory indefinitely (an idle opencode builder is roughly 800 MB).
- `relay done` releases a clean worktree (the branch survives) so you can
  `gh pr checkout` in the main repo without `gc`; a dirty tree or an open
  pane round is kept and `gc` retries. `relay bind --resume` restores a
  released worktree; rebind a DONE binding only after that restore.
- A headless round's log is at `~/.local/state/relay/<name>/NNN-builder.log`.
- When a builder reports a usage limit mid-round, `relay unavailable
  <token>` is enough: the daemon switches and resends. Do not rebind by
  hand unless `relay status` says `NEEDS YOU`.

## Verifying a builder's work

Do not trust the report. Run `make check` yourself -- it is stricter than
`go test ./...` alone, adding `gofmt -l .` over the whole tree, `go vet`, and a
`go mod tidy` check -- and compare `git diff --stat` against the plan's
declared scope. `make e2e` additionally runs one round against a real, private
herdr session with scripted agents; it is local-only and not part of `make check`.
Run it after any change to `reconcile.go`'s nudge, fingerprint or scrape
path, and after any change to `reporttail.go` or `queueReport`: the e2e
suite asserts the scraped round's note exactly, and #201 changed the tail
parser without running it, which left e2e red on `main` for a week (#221).

For anything subtle, mutation-test it: break the specific condition the change
turns on and confirm a named test fails. A test that passes both with and
without the logic is not pinning anything.

## Conventions

- Specs live in `docs/specs/YYYY-MM-DD-<topic>-design.md`. Implementation
  plans live in `docs/plans/YYYY-MM-DD-<name>.md`, a directory introduced by
  #45 -- follow it or drop it, it has no history behind it yet.
- State lives in `$XDG_STATE_HOME/relay` (default `~/.local/state/relay`);
  config resolves via `$XDG_CONFIG_HOME` (default `~/.config`).
  Compose relay config paths through `userConfigRoot()` (`cmd/relay/main.go`),
  never by hand -- see #42 for what hand-rolling one costs.

## Merging and CI

- Merge only after `gh pr checks <n> --watch` has finished with every job
  passing. Checking the first job to complete, or chaining `gh pr merge`
  behind an unconditional check, merged #68 with four jobs pending and broke
  `main` (#70 fixed it).
- CI runners have no `herdr` binary. A test in `cmd/relay` must not execute
  a subcommand that reaches herdr; test the rule as a pure function in
  `internal/relay` instead. Say so in any plan step that adds a CLI test.
- A cmd/relay test never reads the user's real config or state: the package's TestMain points HOME, XDG_CONFIG_HOME and XDG_STATE_HOME at a temp root. A test that needs its own config writes it under a t.TempDir() it sets as XDG_CONFIG_HOME (#235).
