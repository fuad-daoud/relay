# relay

relay automates the plan/report handoff between two AI coding agent panes
running under herdr: a planner hands work to a builder, and relay moves the
files between them.

## Dispatching work to builders

**Parallelism comes from multiple instances of one harness, not from different
harnesses.** Several agy builders can run at once, each in its own worktree.
Needing two builders concurrently is never a reason to reach for a second
harness kind.

Use harnesses in this order, exhausting each before moving to the next:

1. `abuilder` (agy)
2. `cbuilder` (claude)
3. `builder` (opencode)

Move down the list only when the current harness is unavailable -- usage limits
as much as a crash. Do not assign different harnesses to different tasks as a
way of parallelising.

Note the alias names do not track the priority order: `builder` is **opencode**
(third), not the default choice. Check `internal/alias/alias.go` rather than
guessing from the name.

## Working with builders

- Give each concurrent builder its own git worktree. Two builders committing in
  one worktree will race.
- relay closes a pane only in `relay reap`, and only a terminal consult pane
  it spawned. After an `unbind`, a mis-bind, or any `--assume-dead` rebind,
  close the orphaned builder pane yourself with `herdr pane close <id>` or it
  holds memory indefinitely (an idle opencode builder is roughly 800 MB).
- Tell a builder to stop rather than improvise when a step is impossible as
  written or the plan conflicts with existing code. A halt that surfaces a
  design error is worth more than a green suite that bent a test to fit.

## Verifying a builder's work

Do not trust the report. Run `make check` yourself -- it is stricter than
`go test ./...` alone, adding `gofmt -l .` over the whole tree, `go vet`, and a
`go mod tidy` check -- and compare `git diff --stat` against the plan's
declared scope.

For anything subtle, mutation-test it: break the specific condition the change
turns on and confirm a named test fails. A test that passes both with and
without the logic is not pinning anything.

## Conventions

- Specs live in `docs/specs/YYYY-MM-DD-<topic>-design.md`. Implementation
  plans live in `docs/plans/YYYY-MM-DD-<name>.md`, a directory introduced by
  #45 -- follow it or drop it, it has no history behind it yet.
- State lives in `$XDG_STATE_HOME/relay` (default `~/.local/state/relay`);
  config resolves via `os.UserConfigDir()`, which honours `XDG_CONFIG_HOME`.
  Compose relay config paths through `userConfigRoot()` (`cmd/relay/main.go`),
  never by hand -- see #42 for what hand-rolling one costs.
