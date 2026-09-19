# Remote builders, plan 1c: make Go 1.22 emit LC_UUID on macOS in CI (#100, PR #206)

Round 3 on the same branch. CI job `check (macos-latest, go 1.22)` fails on
PR #206 with `dyld: missing LC_UUID load command` for the
`internal/remote` race-test binary. Cause: that package is the first in
the repo to import `net` (via `net/http`), and Go 1.22's linker does not
emit an `LC_UUID` load command on darwin unless asked; the current macOS
dyld refuses such binaries. Go 1.24 emits it by default; the 1.22/1.23
backports made it opt-in with `-ldflags=-B gobuildid`
(golang/go#68678, #69992). Plans 2 and 3 import `net/http` as well, so
the flag is needed for good, not just for this PR.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the file you find, stop, write the report saying which
step and why, create the done marker, and do nothing else.

**Scope guard.** Touch only `.github/workflows/ci.yml` and copy this plan
to `docs/plans/2026-09-19-remote-builders-1c-ci-lc-uuid.md`. No Go file,
no Makefile, no dependency. Foreground only, no sub-agents.

## 1. System overview

The `check` job in `.github/workflows/ci.yml` gets one environment
variable, `GOFLAGS`, carrying the linker flag, with a comment saying why.
`make check` is unchanged. On Go >= 1.24 the flag is redundant and
harmless (it is the default there).

## 2. File structure

```
.github/workflows/ci.yml                      check job: env GOFLAGS
docs/plans/2026-09-19-remote-builders-1c-ci-lc-uuid.md
```

## 3. The change

In `.github/workflows/ci.yml`, inside `jobs.check`, directly after the
`runs-on:` line and before `strategy:`, add:

```
    env:
      # Go < 1.24 does not emit an LC_UUID load command on darwin unless
      # told to, and the current macOS dyld refuses to load a binary
      # without one -- the race-test binary of any package that imports
      # net dies with "dyld: missing LC_UUID load command" before its
      # tests start. -B gobuildid is the opt-in the 1.22/1.23 backports
      # added (golang/go#68678); on 1.24+ it is already the default.
      GOFLAGS: -ldflags=-B=gobuildid
```

Indentation: `env:` at the same depth as `runs-on:` (4 spaces); its key
and comment lines at 6 spaces. YAML, not TOML: the value is the literal
string `-ldflags=-B=gobuildid`.

## 4. Steps

**Step 1.** Copy this plan to `docs/plans/2026-09-19-remote-builders-1c-ci-lc-uuid.md`.

**Step 2.** Make the §3 edit. Verify locally: `GOFLAGS=-ldflags=-B=gobuildid
go test -race -count=1 ./internal/remote` passes on this (linux) machine --
proving the flag is accepted by the toolchain and does not break the
build; the macOS effect is verified by CI.

**Step 3.** `make check` green. One commit: `ci: -B gobuildid so Go 1.22
race binaries load on macOS (#100)` including the plan file. Report the
`git diff` of ci.yml verbatim. Create the done marker.

## Verification the planner runs

Diff touches only the two files; after push, `check (macos-latest, go
1.22)` on PR #206 passes; all other cells still pass.
