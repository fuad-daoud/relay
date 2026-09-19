# Classifier key hygiene: strip relay's secrets from builder environments, refuse a loose key file (#215)

Closes #215. Design in this file; no separate spec.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass, stop
at that step, write the report saying which step and why, create the done
marker, and do nothing else. A halt that surfaces a design error is the
wanted outcome; an improvised workaround is not.

**Scope guard.** Touch only the files listed in §2. Do not run `make e2e`.
Do not add a CLI verb or flag. Do not add a module dependency. Run every
command in the foreground; dispatch no sub-agents and start no background
work. Do not widen any exported signature that §4 does not name -- if a
test outside §2 stops compiling, halt and say which.

**Commits.** Make as many commits as you like while working, but before the
gate squash them into ONE commit whose subject starts `feat(proc):` (`git
reset --soft main && git commit`). The repository's drift guard counts
`feat:` commits, and one feature is one `feat`.

**Commit the plan with the work.** Copy this plan file (the path relay gave
you) to `docs/plans/2026-09-19-key-hygiene.md` in your worktree and include
it in the commit.

**Before step 1**: run `git status --short` and `git branch --show-current`.
You must be on `relay/<binding-name>` in a worktree under
`~/.local/state/relay/.worktrees/`. If you are on `main` or in
`/home/fuad/projects/relay`, halt.

## 1. System overview

#211 added a classifier whose API key resolves from `TYPESAFE_API_KEY` or
`~/.config/relay/typesafe.key`. Two gaps: (1) `proc.Start`
(`internal/proc/proc.go`) builds every headless builder's environment as
`append(os.Environ(), spec.Env...)`, so a daemon that holds the key hands it
to every builder -- an agent with a shell running a plan relay did not
write; (2) a group- or world-readable key file is accepted silently.

This plan (a) filters the parent environment through a constant deny list
before starting any builder, as a pure function so the rule is tested
without a process; (b) treats a key file with any of `0o077` set as "no
key", without reading it, and says why in `relay doctor`; (c) adds `; not
passed to builders` to doctor's `classify` row when the key comes from the
environment, so the strip is visible without grepping a builder log.
Nothing is logged about the strip itself: a builder log line saying
"stripped TYPESAFE_API_KEY" would confirm the key exists.

`relay serve` runs builders through the same `proc.Runner`, so the strip
covers remote builders with no extra code.

## 2. File structure

```
internal/proc/
  env.go            NEW  DeniedEnv, ChildEnv (pure)
  env_test.go       NEW  table over ChildEnv
  proc.go           EDIT Start uses ChildEnv(os.Environ(), DeniedEnv, spec.Env)
  proc_test.go      EDIT TestStartStripsDeniedEnv (execs sh, as the file's other tests do)
internal/classify/
  keyfile.go        NEW  KeyFileUsable (pure)
  keyfile_test.go   NEW  table over modes
  resolve.go        EDIT stat before read; loose mode -> Unavailable with the reason; Status.KeyFileMode
  resolve_test.go   EDIT 0644 key file -> Unavailable, KeySource "", reason names the mode; 0600 -> file key
internal/doctor/
  doctor.go         EDIT ClassifyCheck: env -> "; not passed to builders"; loose file -> SevWarn with chmod fix
  doctor_test.go    EDIT two cases
README.md           EDIT classify section: key never reaches builders; key file must be 0600
docs/plans/2026-09-19-key-hygiene.md   NEW  this file
```

## 3. Data structures & type definitions

```go
// internal/proc/env.go

// DeniedEnv names the variables relay never passes to a builder process.
// They are relay's own secrets, not the harness's: a builder IS the harness
// and needs its provider credentials, so nothing like ANTHROPIC_API_KEY or
// GOOGLE_API_KEY belongs here. Constant on purpose -- a user who wants a
// builder to hold one of these sets it in the harness's own config.
var DeniedEnv = []string{"TYPESAFE_API_KEY"}

// internal/classify -- on Status:
// KeyFileMode is the key file's permission bits when a key file was found,
// whether or not it was usable; 0 when no file exists. For doctor.
KeyFileMode os.FileMode
// KeyFileLoose is true when the file exists but KeyFileUsable refused it.
KeyFileLoose bool
```

## 4. Interface definitions & component contracts

```go
// internal/proc/env.go

// ChildEnv returns parent with every entry whose name is in deny removed,
// then extra appended verbatim. Order is otherwise preserved. A name
// matches when the entry is "NAME=..." or exactly "NAME". extra is not
// filtered: it is relay's own and may set a denied name deliberately.
// Pure; never mutates its inputs; returns a fresh slice.
func ChildEnv(parent, deny, extra []string) []string
```

```go
// internal/classify/keyfile.go

// KeyFileUsable says whether a key file with these permission bits may be
// read. Any of the group or other bits set (mode & 0o077 != 0) refuses,
// with a reason the user can act on: `typesafe.key is readable by others
// (mode 0644); chmod 600 it`. Pure.
func KeyFileUsable(mode os.FileMode) (ok bool, reason string)
```

`Resolve` (existing signature unchanged): after the env check fails, `stat`
`keyPath`; on `IsNotExist` continue to "no key" as today; on another error
treat as no key with the error in the reason; otherwise set
`st.KeyFileMode = info.Mode().Perm()`, call `KeyFileUsable`; refused ->
`st.KeyFileLoose = true`, return `Unavailable{Reason: reason}` **without
reading the file**; usable -> read as today.

`doctor.ClassifyCheck` (signature unchanged):
- `KeySource "env"` -> Detail `"<model>; key from TYPESAFE_API_KEY; not passed to builders"`.
- `KeyFileLoose` -> `SevWarn`, Detail `"<model> configured but <KeyPath> is readable by others (mode 0644); ignored"`, Fix `"chmod 600 <KeyPath>"`.
- other cases unchanged.

## 5. High-level pseudocode

```
proc.Start:
  cmd.Env = ChildEnv(os.Environ(), DeniedEnv, spec.Env)     // the only change in Start

ChildEnv(parent, deny, extra):
  out = make([]string, 0, len(parent)+len(extra))
  for e in parent:
    name = e up to first '=' (whole string if none)
    if name in deny: continue
    out = append(out, e)
  return append(out, extra...)

Resolve (file branch):
  info, err = os.Stat(keyPath)
  if errors.Is(err, os.ErrNotExist): fall through to no-key
  elif err != nil: reason = fmt.Sprintf("cannot stat %s: %v", keyPath, err); return Unavailable, st
  st.KeyFileMode = info.Mode().Perm()
  if ok, reason = KeyFileUsable(st.KeyFileMode); !ok:
    st.KeyFileLoose = true
    return Unavailable{Reason: reason}, st
  data = os.ReadFile(keyPath) ... (as today)
```

## 6. Error handling strategy

No new error types cross a package boundary. A refused key file is the
existing `Unavailable` path: the round's entry note reads
`classify: unavailable: typesafe.key is readable by others (mode 0644); chmod 600 it`,
regex stands, nothing is held. `ChildEnv` cannot fail. `proc.Start`'s
error surface is unchanged.

## 7. README

In the classify section (added by #211): one sentence that the key is
stripped from every builder's environment and why; one that the key file
must be mode 600 and is ignored otherwise, with doctor naming it.

## 8. Tests

`internal/proc/env_test.go` -- `TestChildEnv` table: denied name removed
whether `NAME=value` or bare `NAME`; unrelated entries kept in order;
`NAME_SUFFIX=x` (prefix match) kept; extra appended after; extra may set a
denied name and it survives; empty parent; nil deny returns parent's
contents plus extra; inputs are not mutated (compare copies).

`internal/proc/proc_test.go` -- `TestStartStripsDeniedEnv`: `t.Setenv("TYPESAFE_API_KEY", "leak")`
and `t.Setenv("RELAY_T4", "keep")`; start `sh -c 'echo "k=${TYPESAFE_API_KEY-unset}"; echo "r=$RELAY_T4"'`
the way `TestStartRunsInDirWithExtraEnv` does; stream must contain `k=unset`
and `r=keep`. **Mutation check the builder runs and reports:** change
`Start` back to `append(os.Environ(), spec.Env...)`, confirm this test fails
on `k=leak`, restore by re-editing (never `git checkout` the file), confirm
it passes.

`internal/classify/keyfile_test.go` -- `TestKeyFileUsable`: 0600 ok; 0400
ok; 0640 refused; 0644 refused; 0666 refused; 0700 ok (owner exec bit is
odd but not a leak); reason contains the octal mode and `chmod 600`.

`internal/classify/resolve_test.go` -- key file written with 0644 and no env
-> `Unavailable`, `KeySource ""`, `KeyFileLoose true`, `KeyFileMode 0644`,
`Judge` error contains `readable by others`; same file `os.Chmod` 0600 ->
`*Client`, `KeySource "file"`, `KeyFileLoose false`. Existing cases pass
unchanged (they must write the key file with 0600 -- check and fix the
helper if it uses 0644, and say so in the report).

`internal/doctor/doctor_test.go` -- env key detail ends `; not passed to
builders`; `KeyFileLoose` -> `SevWarn`, Detail contains `readable by
others` and `0644`, Fix starts `chmod 600 `.

## 9. Ordered implementation steps

1. `internal/proc/env.go` + `env_test.go`. `go test ./internal/proc -run TestChildEnv`.
2. `proc.go` one-line change; `TestStartStripsDeniedEnv`; run the mutation
   check and record both outcomes. `go test ./internal/proc`.
3. `internal/classify/keyfile.go` + test; `Status` fields; `Resolve` stat
   branch; `resolve_test.go` cases. `go test ./internal/classify`.
4. `doctor.ClassifyCheck` two branches + tests. `go test ./internal/doctor`.
5. README.
6. Squash to one `feat(proc):` commit (include the plan copy). Gate, exactly:
   ```
   test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
   make check
   ```
   Both exit 0; `git status --short` empty. Report the `make check` tail
   verbatim, the mutation outcomes, and `git diff --stat main..HEAD`.

## 10. Report

End `NNN-report.md` with the ```relay block: `status`, `halted_at`,
`changed_paths`, `commands_run` (the gate lines), `not_done`. In prose:
the mutation result and whether any pre-existing test needed its key-file
mode changed.
