# Served builders, part 3 of 3: a systemd scope per round, and per-round cpu/memory facts (#244, #216)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo, on a
box where `relay serve` runs as a systemd **user** unit under
`relay.slice`. The worktree has **no `origin`**: never fetch, pull or
rebase. Never run `make check` here; run the gate commands in §7 exactly
as written. Every command in the foreground; no sub-agents for edits. Do
not touch any file outside this worktree. **Never restart, stop or signal
`relay-serve` or any other unit on this machine**: you are running inside
it. The only systemd commands you may run are the read-only probes in §3
and the scoped `true` in §3.2, which start and end a throwaway scope of
your own.

Part 1 (`docs/plans/2026-09-21-serve-queue-core.md`, in this tree) added
`policy.ServePolicy.Scope` (`ScopePolicy`, validated) and the queue. This
part consumes `ScopePolicy`; it adds nothing to admission.

## 1. System overview

Every served builder is a child of `relay-serve.service`, so
`systemctl --user restart relay-serve` stops it with the daemon (#244) and
N builders share the cores per thread, not per round (#285's cpu.pressure).
This part launches each served round as a transient systemd scope:

```
systemd-run --user --scope --quiet --collect --unit=relay-round-<owner8>-<name>-<round>.scope
            [--slice=<slice>] -p CPUWeight=<w> [-p MemoryMax=<m>] [-p TasksMax=<t>] --
            /bin/sh -c '<supervisorScript>' relay-supervisor <bin> <args...>
```

`systemd-run --scope` registers the scope and then **execs the command in
place**, so the pid Go records is the supervisor's pid exactly as today;
`Setsid`, the process-group `Kill`, `ps`-based `Alive` and the exit trailer
are unchanged. The scope is a sibling of the service under the slice, so a
daemon restart leaves the builder running and the new daemon's first tick
finds the pid alive (nothing to change there: `proc.Alive` never assumed
parentage). Equal `CPUWeight` per scope makes CFS share the CPU per round.

The supervisor script also reads its own cgroup's `cpu.stat` and
`memory.peak` after the builder exits (the `sh` is still alive, so the
cgroup still exists) and prints a `relay-rusage:` line before the
`relay-exit:` trailer. Relay records it on the report entry as
`LogEntry.Rusage`, ships it in `BindingView.Rusage`, and the client
re-records it in `catchUp`. `relay show --log` prints it through `LogLine`.

Scopes are enabled by policy (default on) and confirmed by a startup probe;
if `systemd-run` is missing or the user manager refuses, scopes are off for
the daemon's lifetime with one log line, and `whoami` says so. The local
`relay daemon` never sets a scope; CI never sees `systemd-run`.

## 2. Files

```
internal/relay/runner.go              ScopeSpec; ProcSpec.Scope; ProcRusage; Runner.Rusage
internal/relay/herdr.go               Runtime.Scope *ScopeSpec (template; Unit empty)
internal/relay/headless.go            startRound fills ProcSpec.Scope from rt.Scope; scopeUnitName
internal/relay/headless_test.go       scope on the spec when rt.Scope set; unit name shape
internal/relay/reconcile.go           queueReport: report entry Rusage from rt.Runner.Rusage (headless only)
internal/relay/served.go              ServedView copies the closed round's report Rusage
internal/relay/remote.go              catchUp records view.Rusage on the report entry it appends
internal/relay/logline.go             report line shows "cpu 12.3s peak 850MB" when Rusage present
internal/relay/*_test.go              every fake Runner in internal/relay gains Rusage (returns zero,false)
internal/proc/scope.go         (new)  ScopeArgv; ProbeScopes; RusageTrailer; ParseRusageTrailer
internal/proc/scope_test.go    (new)
internal/proc/proc.go                 Start wraps argv when Scope != nil; supervisorScript emits relay-rusage; Rusage method
internal/proc/proc_test.go            supervisor emits the trailer only inside a relay-round scope (see §5)
internal/store/types.go               Rusage struct
internal/store/log.go                 LogEntry.Rusage *Rusage
internal/remote/proto.go              BindingView.Rusage; WhoAmI.Builders.Scopes/Slice are filled (fields exist from part 1)
internal/serve/serve.go               Config.Scope *relay.ScopeSpec; runtimeAt passes Scope
internal/serve/routes.go              handleWhoAmI: Builders.Scopes = cfg.Scope != nil, Slice
internal/serve/serve_test.go          scriptRunner gains Rusage
internal/e2e/fakes_test.go            scriptRunner gains Rusage
internal/pick, internal/ui            any other relay.Runner implementer (grep "Runner interface" implementers: `grep -rn 'func (.*) ExitCode(ctx' internal/`)
cmd/relay/serve.go                    scopeFromPolicy; ProbeScopes at start; Config.Scope; startup line gains scopes=...
cmd/relay/serve_test.go               scopeFromPolicy table (pure)
docs/plans/2026-09-21-serve-scopes.md copy of this plan (last step)
```

## 3. Probes (first; results verbatim in the report)

3.1 Read-only: `systemctl --version | head -1`; `cat
/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.subtree_control`;
`systemctl --user show relay-serve.service -p Slice -p KillMode`; `cat
/proc/self/cgroup`. Expected: systemd >= 250, `cpu memory pids` delegated,
`Slice=relay.slice`.

3.2 The exec contract. Run:

```
systemd-run --user --scope --quiet --collect --unit=relay-probe-$RANDOM.scope --slice=relay.slice -p CPUWeight=100 -- /bin/sh -c 'echo pid=$$; cat /proc/self/cgroup; cat /sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)/cpu.stat | head -3; cat /sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)/memory.peak'
```

Expected: the cgroup path ends in `/relay.slice/relay-probe-N.scope`, `cpu.stat`
has `usage_usec`, `memory.peak` is a number. Then confirm the pid identity:
`systemd-run --user --scope --quiet --collect --unit=relay-probe-$RANDOM.scope -- /bin/sh -c 'echo $$' & echo spawned=$!; wait` -- the two numbers must be
equal (systemd-run execs in place). **If they differ, halt and report**:
§4.3's pid contract does not hold on this systemd and the design needs a
different liveness handle.

## 4. Contracts

### 4.1 Types (`internal/relay/runner.go`, `herdr.go`)

```go
// ScopeSpec asks the runner to start the process as a transient systemd
// scope (#244, #285). nil on ProcSpec means a plain spawn.
type ScopeSpec struct {
    Unit      string // "relay-round-<owner8>-<name>-<round>"; the runner appends ".scope"
    Slice     string // "" = omit --slice
    CPUWeight int    // >= 1; always emitted
    MemoryMax string // "" = omit
    TasksMax  int    // 0 = omit
}
ProcSpec.Scope *ScopeSpec

// ProcRusage is what the supervisor measured for the round's cgroup.
type ProcRusage struct {
    CPUMS        int64
    PeakMemBytes int64
}
// Rusage reports the relay-rusage: trailer the supervisor left as the
// stream's second-to-last line; ok false when absent (plain spawn, killed
// supervisor, still running).
Runner.Rusage(ctx context.Context, h ProcHandle, streamPath string) (ProcRusage, bool)

Runtime.Scope *ScopeSpec  // template with Unit == ""; nil = no scopes (local daemon, CI)
```

Every `relay.Runner` implementer (fakes included, in every package) gains
`Rusage` returning `(ProcRusage{}, false)` unless the test sets one.

### 4.2 `startRound` (`headless.go`)

When `rt.Scope != nil`: `spec.Scope = &ScopeSpec{Unit: scopeUnitName(b),
Slice: rt.Scope.Slice, CPUWeight: rt.Scope.CPUWeight, MemoryMax:
rt.Scope.MemoryMax, TasksMax: rt.Scope.TasksMax}`.

```go
func scopeUnitName(b store.Binding) string
    // "relay-round-" + owner8 + "-" + safe(b.Name) + "-" + strconv.Itoa(b.Round)
    // owner8: first 8 chars of remote.ClientID(b.Owner).Dir() (the hex form); "local" when Owner == ""
    // safe: every rune outside [A-Za-z0-9:_.-] becomes '-'
```

`switchBuilder` and the #244 relaunch call `startRound`, so they get the
scope for free; do not add scope code there.

### 4.3 `proc` (`scope.go`, `proc.go`)

```go
func ScopeArgv(s relay.ScopeSpec, inner []string) []string
// ["systemd-run","--user","--scope","--quiet","--collect","--unit="+s.Unit+".scope",
//  {"--slice="+s.Slice}?, "-p","CPUWeight="+itoa(s.CPUWeight),
//  {"-p","MemoryMax="+s.MemoryMax}?, {"-p","TasksMax="+itoa(s.TasksMax)}?, "--", inner...]

func ProbeScopes(ctx context.Context, slice string) error
// exec ScopeArgv({Unit: "relay-probe-"+8 random hex, Slice: slice, CPUWeight: 100}, ["true"])
// with a 10 s timeout via exec.CommandContext; non-zero exit or exec error ->
// fmt.Errorf("systemd-run: %s", first non-empty stderr line, or err.Error())

const RusageTrailer = "relay-rusage:"
func ParseRusageTrailer(line string) (relay.ProcRusage, bool)
// "relay-rusage:cpu_usec=123456 mem_peak=891289600" -> {123, 891289600}, true
// fields are space-separated key=value; either may be absent (zero); unknown keys ignored;
// a line not starting with RusageTrailer -> false; a malformed number -> that field zero.
```

`Start`: build `inner` exactly as today (`/bin/sh -c supervisorScript
relay-supervisor bin args...`); `argv := inner`; `if spec.Scope != nil { argv
= ScopeArgv(*spec.Scope, inner) }`; `exec.Command(argv[0], argv[1:]...)`. Nothing
else in `Start` changes.

`supervisorScript` becomes (POSIX sh; keep the existing first line):

```
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null; rc=$?
cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
case "$cg" in */relay-round-*.scope)
  u=$(awk '/^usage_usec/{print $2}' "/sys/fs/cgroup$cg/cpu.stat" 2>/dev/null)
  m=$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null)
  printf '\nrelay-rusage:%s%s\n' "${u:+cpu_usec=$u}" "${m:+ mem_peak=$m}"
  ;;
esac
printf '\nrelay-exit:%s\n' "$rc"
```

The `case` guard is the contract: outside a `relay-round-*.scope` the
script prints no rusage line, because `/proc/self/cgroup` would then name
the whole service. Keep the script a single Go string constant as today;
`awk` and `cut` are on every box relay serves from (coreutils/gawk on Arch;
if you find the box lacks `awk`, use `grep usage_usec ... | cut -d' ' -f2`
and say so in the report).

`Rusage`: read the last two non-empty lines of `streamPath` (there is a
`lastLine` helper; generalise it to `lastLines(path, n)` and keep
`ExitCode` on the last line). If the second-to-last starts with
`RusageTrailer`, return `ParseRusageTrailer` of it.

### 4.4 Recording (`reconcile.go`, `served.go`, `remote.go`, `logline.go`)

`queueReport`: on the headless path, next to where the report entry's
`Usage` is set, `if r, ok := rt.Runner.Rusage(ctx, handleOf(b.Builder),
rt.Store.BuilderStreamPath(name, round)); ok { entry.Rusage = &store.Rusage{CPUMS:
r.CPUMS, PeakMemBytes: r.PeakMemBytes} }`. Guard `rt.Runner != nil` and
`b.Builder.Headless()`; a pane round records nothing. Do not touch
`queueReport`'s ordering, notes or the report-tail parsing -- the e2e suite
pins the note exactly.

`ServedView`: where it copies the closed round's report `Usage` into
`view.Usage`, copy `Rusage` the same way. `catchUp`: where it records the
report entry with `view.Usage`, set `Rusage: view.Rusage`. `store.Rusage`
and `remote` importing it: `remote` already imports `usage`; confirm
`store` does not import `remote` (`go list -deps`) -- if it does, define
`remote.RusageView` with the same fields and convert at both ends.

`LogLine`: a `KindReport` entry with `Rusage != nil` appends ` cpu <s>
peak <mem>` using the durations/bytes formatters the package already has
(`ShortTokens`-style helpers exist in `internal/usage/format.go`; a bytes
formatter may not -- add `shortBytes` in `logline.go` producing `850MB`,
`1.2GB`, `640KB`).

### 4.5 Serve wiring (`serve.go`, `routes.go`, `cmd/relay/serve.go`)

`Config.Scope *relay.ScopeSpec`; `runtimeAt` sets `Scope: s.cfg.Scope`.
`handleWhoAmI`: `Builders.Scopes = s.cfg.Scope != nil`; `Builders.Slice =
s.cfg.Scope.Slice` when non-nil.

`cmd/relay/serve.go`:

```go
func scopeFromPolicy(pol policy.Policy) *relay.ScopeSpec
// nil when pol.Serve != nil && pol.Serve.Scope != nil && Enabled != nil && !*Enabled
// else {Slice, CPUWeight (0->100), MemoryMax, TasksMax} from the policy or zero-value defaults
```

In `cmdServeRun`, after policy load: `scope := scopeFromPolicy(pol)`; if
non-nil, `if err := proc.ProbeScopes(ctx, scope.Slice); err != nil { log
"scopes unavailable: <err>; builders will run in the daemon's cgroup";
scope = nil }`. `Config.Scope = scope`. The part-1 startup line `builders
cap=<n>` gains ` scopes=on (slice <s>)` / ` scopes=on` / ` scopes=off` /
` scopes=unavailable`.

## 5. Tests

- `internal/proc/scope_test.go`: `TestScopeArgv` table (full spec; no
  slice; no memory; no tasks -- exact argv). `TestParseRusageTrailer`
  table (both fields; cpu only; mem only; unknown key; malformed number;
  wrong prefix). `TestRusageReadsSecondToLastLine` (write a stream file
  with builder output, the rusage line, the exit trailer -> ok true; without
  the rusage line -> false; `ExitCode` still reads the exit line).
  `TestProbeScopesStub`: put a `systemd-run` script on a temp `PATH` that
  execs everything after `--` -> nil; a stub that prints `Failed to start
  transient scope unit: Permission denied` to stderr and exits 1 ->
  error containing that line. CI has no systemd; never call the real one.
- `internal/proc/proc_test.go`: `TestSupervisorEmitsRusageOnlyInScope`:
  `/proc/self/cgroup` cannot be faked, so pin the guard's negative half:
  run `sh -c '<supervisorScript>' relay-supervisor true` (plain spawn) and
  assert the stream has **no** `relay-rusage:` line and does have
  `relay-exit:0`. The positive half is verified by the planner on the box (§8).
  `TestStartWrapsArgvWithScope`: `Runner.Start` with a `Scope` builds the
  command through `ScopeArgv` -- expose the argv builder as an unexported
  `buildArgv(spec)` and test it, rather than executing `systemd-run`.
- `internal/relay/headless_test.go`: `TestStartRoundSetsScope` (rt.Scope
  set -> the fake runner's recorded `ProcSpec.Scope` has the expected unit
  name `relay-round-<owner8>-<name>-<round>`, slice, weight); `TestScopeUnitNameSafe`
  (owner "" -> `local`; a name with `/` and space -> `-`).
- `internal/relay`: `TestQueueReportRecordsRusage` (fake runner returns
  `{12300, 850<<20}, true` -> the report entry has it; returns false -> nil).
  `TestServedViewCarriesRusage`; `TestCatchUpRecordsRusage` (extend the
  existing catch-up test's view).
- `cmd/relay/serve_test.go`: `TestScopeFromPolicy` table (nil policy ->
  defaults on; enabled=false -> nil; weight 0 -> 100; slice/memory pass through).

## 6. Steps (one commit each)

1. Probes (§3); write them into the report first. Halt if 3.2's pid check fails.
2. Types: `runner.go`, `herdr.go`, `store`, `remote`; every `Runner` fake gains `Rusage`. `go build ./... && go vet ./...`.
3. `proc`: `scope.go`, `Start` wrapping, supervisor script, `Rusage`; tests.
4. `relay`: `startRound` scope, `scopeUnitName`, `queueReport`, `ServedView`, `catchUp`, `LogLine`; tests.
5. `serve` + `cmd/relay`: `Config.Scope`, whoami, `scopeFromPolicy`, probe, startup line; test.
6. Gate (§7), plan copy, report.

## 7. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/proc/ ./internal/relay/ ./internal/serve/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

Copy this plan to `docs/plans/2026-09-21-serve-scopes.md`; commit
`chore(plans): serve scopes`. Code commits `feat(proc): ...` /
`feat(serve): ...` per step, naming #244 and #216.

## 8. Not yours (the planner does these after the round)

`make e2e` (touches `queueReport`); deploying the binary to the box;
restarting `relay-serve` and confirming a live builder survives it under
`relay.slice/relay-round-*.scope`; confirming a real round's report entry
carries `rusage`.

## Report

The §3 probe output verbatim first. Then per step: test names, gate tail,
commit shas; every exported identifier added or changed with its
signature; anything done that this plan did not say, or where you halted.
