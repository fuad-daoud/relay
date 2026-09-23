# Plan: #292 round 6: two CI failures on PR #384 (a macOS-only test, and a reaper-test race)

PR #384's CI failed three of five jobs. Both causes are in tests; the code under
test is correct.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report. Change nothing outside the two tests.

## 1. `TestCmdMigrateDryRun` fails on macOS (both Go versions)

```
migrate_test.go:68: dry-run output does not name "retire":
```

`cmd/relevo/migrate_test.go` ~line 55 plants the old client unit at the **Linux**
path (`<configHome>/systemd/user/relay.service`). On darwin,
`migrate.DefaultClientUnits` looks for the launchd plist under
`$HOME/Library/LaunchAgents/`. So no old unit is found and the retire step (rightly)
never prints.

**Fix:** plant the old unit where the code looks on the running platform. Replace
that `mustWrite` with one that writes to
`migrate.DefaultClientUnits(runtime.GOOS, configHome, home).Old.Path`. Use the
function the CLI uses, not a hand-built path. Create parent dirs as `mustWrite`
does, and check it does. On any GOOS other than linux or darwin, `Old.Path` is
`""`: `t.Skip` there, with a reason.

Keep the step-name list unchanged. Every name, `retire` included, must print on
both platforms. The comment above the write should say why the path comes from
`DefaultClientUnits`.

## 2. `TestReapScopeKillsWhatIgnoresTERM` races on ubuntu/go stable

```
proc_test.go:1213: wait status = signal: terminated (signal: terminated); want SIGKILL, since TERM was ignored
```

`internal/proc/proc_test.go` ~1189 starts `sh -c "trap '' TERM; exec sleep 60"` and
reaps immediately. If the reap's TERM arrives before `sh` has run `trap`, `sh` dies
of TERM. The test came in with #381 and passed there by luck of scheduling.

**Fix:** wait until the trap is in place before reaping.

1. Use `sh -c "trap '' TERM; echo ready; exec sleep 60"` with
   `stubborn.StdoutPipe()`.
2. After `Start`, read one line with a `bufio.Reader` and require it to be
   `ready`. Guard the read with a 10 s timeout: read in a goroutine and `select`
   on a timer, then `t.Fatal` on timeout.
3. Only then write the procs file and reap.

An ignored signal disposition survives `exec`, so `sleep` still ignores TERM.
Keep every assertion as it is.

## 8. Working efficiently

- **Focused:**
  - `go test -race -count=20 -run TestReapScopeKillsWhatIgnoresTERM ./internal/proc/`
    (20 runs; all must pass);
  - `go test -count=1 -run TestCmdMigrateDryRun ./cmd/relevo/`;
  - `GOOS=darwin go vet ./cmd/relevo/` (compiles the darwin test path).
- **Once at the end:** `make check`.

## 9. Steps

1. §1.
2. §2.
3. The focused commands, then `make check`. All must pass.
4. One commit:
   `test: TestCmdMigrateDryRun plants the platform's unit; the reaper test waits for its trap (#292)`.
   Don't push.

**Declared scope:** `cmd/relevo/migrate_test.go`, `internal/proc/proc_test.go`.

**Report:**
- the focused outputs (the `-count=20` line);
- `git diff --stat HEAD~1`;
- the result of `make check`.
