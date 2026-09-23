# Plan: upgrade handoff, round 2 of 2: what a binary upgrade leaves stale (#371)

**Read first:** `docs/specs/2026-09-23-upgrade-handoff-design.md` in your tree (committed in round 1), especially §4.10. This round implements §4.10 and one fix to round 1. Where this plan and the spec disagree, the spec wins, **except for the amendment below**, which supersedes spec §4.3 step 2.

**Stop rather than improvise.** If a step is impossible as written, or contradicts the code, halt, report which step and why, and create the done marker. Don't bend a test to fit.

**Parallel branch:** #370 edits `internal/relay/daemon.go` (`Tick`, `tickOne`) and `internal/relay/{headless,gate,consult,verify}.go`. In `daemon.go`, touch only `refreshRelease` and the `Daemon` struct fields it needs.

## 1. System overview

Round 1 made the daemon follow a newly installed binary. This round moves along the three things that still stay old after an upgrade:

1. **Role definitions.** relay records the sha256 of every definition file it writes, so a later relay can tell "unchanged since relay wrote it" (safe to update) from "edited by the user" (keep). The daemon refreshes them at every image start, re-execs included.
2. **`relay mcp`.** It runs for a whole planner session. It appends a one-line notice to every tool result when the daemon runs a different version than it does.
3. **The release check.** It backs off for an hour after a failed fetch instead of retrying every tick.

It also fixes round 1's gap: a rollback to the daemon's own binary now clears a recorded refusal.

## 2. File structure

```
internal/upgrade/watch.go (+ _test)      AMENDMENT: cur == Started clears refused as well as pending
cmd/relay/main.go                        (only if needed) hook already rewrites daemon.json when Refused() changes -> now reachable; add a test via the Watcher only
internal/harness/install.go (+ _test)    manifest (LoadManifest/SaveManifest on InstallEnv), OutcomeUpdated, OutcomeWouldUpdate, sha rules, atomic WriteFile
internal/harness/manifest.go             NEW  ManifestPath(stateRoot) + sha helper + JSON load/save used by osInstallEnv (keeps install.go small)
cmd/relay/<agent install file>           osInstallEnv gets the manifest path from the store root (compose paths as existing code does; never hand-roll config/state roots -- see CLAUDE.md, userConfigRoot / store.DefaultRoot)
cmd/relay/main.go                        cmdDaemon: role refresh once per image start, after the lock (spec §4.10 "The daemon refreshes roles")
internal/doctor/doctor.go (+ _test)      role staleness row for every harness on PATH (spec §4.10 "Doctor roles"); keep the agy model check
internal/mcp/server.go (+ _test)         Server.Notice func() string; appended text content item in handleToolsCall
cmd/relay/mcp.go                         wires Notice: daemon.json cached 30 s + mcpNotice
cmd/relay/mcp_notice.go (+ _test)        NEW  mcpNotice (pure)
internal/relay/daemon.go (+ test)        refreshRelease: releaseRetryAt backoff (spec §4.10)
README.md                                correct the role-definition claim at ~line 135; one sentence in "Upgrading" on roles + MCP reconnect
```

## 3. Data structures

- **The manifest**: `map[string]string`, a home-relative path (exactly `Role.Path` as `InstallResult.Path` carries it) to the lowercase hex sha256. The file is `<state root>/agents-manifest.json`, written atomically (temp file plus rename), mode 0644. Missing means empty; malformed is an error that `Install` reports once, and the manifest is then treated as empty.
- `harness.OutcomeUpdated InstallOutcome = "updated (unchanged since relay wrote it)"`
- `harness.OutcomeWouldUpdate InstallOutcome = "would update"`
- `Daemon.releaseRetryAt time.Time`, unexported.

## 4. Interfaces and contracts

- `InstallEnv` gains `LoadManifest() (map[string]string, error)` and `SaveManifest(map[string]string) error`. Every existing fake env in tests must implement them. An in-memory map is fine.
- `installOne`'s decision table follows spec §4.10 "Roles" exactly. `DocEqual` stays the identity test. The sha is always taken over the raw bytes.
- `Install` loads the manifest once, threads it through `installOne` and saves it once at the end, only if it changed and not under DryRun.
- `mcp.Server.Notice func() string`. It is nil-safe, and "" means nothing is appended. It is appended as an additional `{"type":"text","text":notice}` content item, on success and error results alike. `tools/list`, `initialize` and pushes are unchanged.
- `mcpNotice(own string, info store.DaemonInfo, ok bool) string`: spec §4.10 "MCP", with the exact text given there.
- Release backoff: `const releaseRetryAfter = time.Hour`. A failed `Fetcher.Latest` sets `d.releaseRetryAt = now().Add(releaseRetryAfter)`. `refreshRelease` returns before loading the cache when `now().Before(d.releaseRetryAt)`. A successful save clears it.

**AMENDMENT to spec §4.3 step 2:** `cur == Started` → `None`, and clear **both** `pending` and `refused`. With that, the round-1 hook's existing `Refused()`-changed branch writes `daemon.json` without `ReexecFailed` after a rollback.

## 5. High-level pseudocode

```
installOne(env, opts, kind, r, manifest):
  shipped, full := ...
  existing, rerr := env.ReadFile(full)
  switch:
    not exist            -> DryRun? WouldWrite : write -> Wrote; manifest[p]=sha(shipped)
    DocEqual             -> KeptIdentical; manifest[p]=sha(existing)
    manifest[p]==sha(existing) -> DryRun? WouldUpdate : write -> Updated; manifest[p]=sha(shipped)
    Force                -> DryRun? WouldOverwrite : write -> Overwrote; manifest[p]=sha(shipped)
    else                 -> KeptDiffers   (manifest untouched)
  a write error -> OutcomeError (manifest untouched for that path)

cmdDaemon, after lock + daemon.json:
  results, err := harness.Install(<same env relay agent install uses>, InstallOptions{})
     over the same kind selection `relay agent install` makes by default -- reuse its function; if that selection lives
     inside the cmd handler, extract it into a helper both call (no behaviour change for the verb)
  for each result: Wrote/Updated -> Info "role definition refreshed"; KeptDiffers -> Info "<path> was edited; relay agent install --force replaces it"; Error -> Warn
  err -> Warn; never return it

relay mcp:
  srv.Notice = cachedNotice(30s): read daemon.json via rt.Store.ReadDaemonInfo -> mcpNotice(version, info, ok); a read error -> ""
```

## 6. Error handling strategy

- A role refresh never fails the daemon or the verb: per-file errors are outcomes, and a manifest save error is a Warn (daemon) or a printed error line (verb, as today's errors print).
- An MCP notice read error → no notice.
- A release fetch failure → a Debug log (as today) plus the backoff.

## 7. Ordered implementation steps

**Step 1: Watcher amendment**, with a test: refused identity X, then Stat returns Started → None and `Refused()` is nil. Then X again → Wait, because it may be tried again after a rollback. Mutation: drop the `refused = nil`, and the test fails.

**Step 2: manifest and install decisions** (`internal/harness`). Table tests over a fake env for each row of §5's table, including DryRun (`WouldUpdate`, and no manifest save) and the "edited by the user" case (a sha mismatch stays `KeptDiffers` without `--force`).

Real env: `WriteFile` goes through a temp file in the same dir plus rename. Test on a `t.TempDir()`: no temp file is left behind, and the content is replaced.

Mutation: make the `manifest[p]==sha(existing)` row always false, and a named test fails.

**Step 3: the `relay agent install` verb** prints the new outcomes. It needs no flag changes. Verify that the verb's existing tests pass. The new outcome lines appear only where the table says.

**Step 4: the daemon role refresh** in cmdDaemon (§5). Add no cmd/relay test that runs the daemon. If you extract a kind-selection helper, test it as a pure function.

**Step 5: doctor roles row** (spec §4.10). Tests with a fake env covering stale (WouldUpdate or WouldWrite → Warn), user-edited (OK with the detail "edited by you (kept)") and current. Keep the agy model check test green and unchanged.

**Step 6: MCP notice.** `Server.Notice` plus a server test: a tools/call result carries the extra text item when Notice returns text, and it is byte-identical to today when Notice is nil or "". Add a `mcpNotice` table test. Wire it in `cmd/relay/mcp.go` with a 30 s cache (a pure `cachedString(ttl, now, f)` helper with a test is fine).

**Step 7: release backoff**, with a test: a fetcher failing once, then Tick called twice with a fake clock → Latest is called once. Advance the clock 61 minutes → called again. Use a seeded binding, the way round 1's drain test did, so Tick reaches `refreshRelease`.

**Step 8: README.** Replace the claim that the plugin reinstalls role definitions at install and update with the truth:
- the daemon refreshes unmodified definitions on every start and upgrade;
- files you edited are kept, and `relay agent install --force` replaces them.

In the Upgrading section, add that a planner session's `relay mcp` notices an upgrade and says to reconnect (`/mcp`).

**Step 9: gate.** `make check` and `make e2e`. Commit ending `(#371)`.

Declared scope: §2's files and their tests.
