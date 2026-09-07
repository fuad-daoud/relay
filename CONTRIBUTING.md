# Contributing to relay

Thanks for looking. relay is small and intends to stay that way, so the most
useful thing you can do before writing code is open an issue and check the
change is in scope.

## What relay is, and is not

relay moves files between two agent panes and reports what herdr sees. It makes
no judgements: whether a report is good, whether a question needs a human,
whether the work is done — all of that stays with the planner (or the person at
the keyboard). Changes that ask relay to decide something on the human's behalf
are almost always out of scope, however convenient they look.

Two rules the code protects, and that a change must not weaken:

- **Never type over a human.** A focused planner pane is treated as unsafe to
  inject into. See "the anti-clobber rule" in the README.
- **Never close a pane you did not open.** relay spawns exactly one builder pane
  per binding and never kills anything, because a builder's terminal is often
  the only record of why a round went wrong.

## Getting set up

You need Go 1.22 or newer and [herdr](https://github.com/herdrdev/herdr) on
`PATH`. herdr is a hard runtime dependency; the test suite fakes it, so you can
build and test without it, but you cannot actually run relay without it.

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
  covered by a test that drives a fake herdr (`internal/relay/fake_test.go`) —
  add to it rather than reaching for a real one.
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

## Reporting bugs

Use the issue templates. `relay version`, `herdr --version` and
`relay status --json` answer most of the questions a maintainer would otherwise
have to ask.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE) that covers the project.
