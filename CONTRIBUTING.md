# Contributing to relay

Thanks for looking. relay is small and intends to stay that way, so the most
useful thing you can do before writing code is open an issue and check the
change is in scope.

## What relay is, and is not

relay moves files between a planner and the builders it runs. It makes
no judgements: whether a report is good, whether a question needs a human,
whether the work is done — all of that stays with the planner (or the person at
the keyboard). Changes that ask relay to decide something on the human's behalf
are almost always out of scope, however convenient they look.

One rule the code protects, and that a change must not weaken:

- **Never stop a process you did not start.** relay stops only the headless
  builder processes it launched itself, and only on the verbs that say so
  (`done`, `unbind`, `stop`, a gated switch). A builder's round log is
  often the only record of why a round went wrong, so it is never deleted.

## Getting set up

You need Go 1.22 or newer. To run a real round you also need at least one
builder harness (`claude`, `opencode`, `agy` or `codex`) on `PATH`; the test
suite fakes them, so you can build and test without any.

```
git clone https://github.com/fuad-daoud/relay
cd relay
make check
```

`make check` is the whole gate: `gofmt -l .`, `go vet ./...`, and
`go test -count=1 ./...`. CI runs exactly that on Linux and macOS against both
the Go 1.22 floor and current stable, plus a cross-compile sweep. If
`make check` passes locally it will almost certainly pass there.

## Making a change

- **Write the test first.** Nearly every behaviour in `internal/relay` is
  covered by a test that drives a fake runner (`internal/relay/fake_test.go`) —
  add to it rather than reaching for a real harness.
- **Say why in a comment, not what.** The existing comments explain the
  reasoning behind a decision, especially where the obvious implementation
  would be wrong. Match that; skip comments that restate the code.
- **Keep it stdlib.** relay has no third-party dependencies and that is a
  feature. A PR that adds one needs to argue for it.
- **Run `gofmt`.** CI fails on unformatted files.
- **One change per PR.** It makes review, and reverting, far easier.

## Platform support

relay targets Linux and macOS. State locking is behind a build tag
(`internal/store/lock_unix.go`), and the tree must keep compiling for other
platforms even though they are unsupported — the cross-compile job in CI is
what enforces that. If you touch anything platform-specific, put it behind a
build tag rather than a `runtime.GOOS` check.

## Releasing

The Claude Code plugin manifests (`claude-plugin/.claude-plugin/plugin.json`
and `.claude-plugin/marketplace.json`) are bumped in the release commit, and
`scripts/check-plugin-version.sh` refuses a tag that does not match them.
`make release` does both in the right order.

## Reporting bugs

Use the issue templates. `relay version`, the harness's version and
`relay status --json` answer most of the questions a maintainer would otherwise
have to ask.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE) that covers the project.
