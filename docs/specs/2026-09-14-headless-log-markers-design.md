# Headless log markers: relay says in the log when it ends or replaces a builder

**Issue:** #153 (as corrected 2026-09-14).
**Depends on:** nothing open.
**Amends:** the `supervisorScript` comment in `internal/proc/proc.go`, which
claims the trailer survives "whatever happens" to the builder; it survives
an OOM of the builder, not a group kill. README "Headless builders" gains
one sentence.
**Status:** draft; plan at `docs/plans/2026-09-14-headless-log-markers.md`.

## 1. System overview

A headless builder's stdout and stderr for a round go to
`<binding dir>/NNN-builder.log`, appended, with the supervisor's
`relay-exit:<code>` as the last line when the builder exits on its own.
Three things relay does leave no trace in that file:

- a **mid-round switch** kills the process (or finds it dead) and starts the
  next candidate on the same round, so two builders' output sits back to
  back with nothing between them (#122 round 1: gemini's two lines, then
  sonnet's);
- **`relay done`** stops a still-running process;
- **`relay unbind`** stops a still-running process.

In each case the ledger (`log.jsonl`) records what happened and why, but the
file a human opens to see what the builder did does not. And because `Kill`
signals the process group, the supervisor `sh` dies with the builder and no
trailer is written, so the log simply stops.

This design adds one line, written by relay, at each of those three points:

```
--- relay 23:13:51: switched to claude/anthropic/sonnet (rate-limited: individual quota reached; resets ~23:40) ---
--- relay 21:58:03: stopped: done ---
--- relay 09:12:40: stopped: unbind ---
```

The marker is for humans. Nothing in relay parses it: `ExitCode` reads only
a trailing `relay-exit:` line, and a marker never begins with that prefix.

### The trailer on kill: deliberately not changed

#153's second item asks whether `Kill` should let the supervisor write
`relay-exit:143`. No. The trailer's only reader is `Runner.ExitCode`, which
relay consults for a process it did *not* stop (exited without a report,
§headless 4.7). Every process relay stops itself now gets a marker naming
the reason, which is more than an exit code would say, and making plain
`sh` forward a signal, wait twice and write a trailer is the kind of
script that fails on the one platform nobody tested. The comment on
`supervisorScript` is corrected to say what the trailer actually survives.

### Scope boundary

Three call sites, one helper, one comment fix, one README sentence. No
change to `proc`'s behaviour, to `ExitCode`, to `logTail`, to the ledger
entries, or to pane builders (which have no log file; the helper is never
called for them).

## 2. File structure

```
internal/relay/headless.go        appendLogMarker (new); stopProcess gains a why argument
internal/relay/switch.go          switchBuilder: marker before startRound, headless only
internal/relay/status.go          Done: stopProcess(..., "done")
internal/relay/bind.go            Unbind: stopProcess(..., "unbind")
internal/relay/headless_test.go   tests in §7
internal/proc/proc.go             supervisorScript comment corrected
README.md                         "Headless builders": one sentence on markers
```

## 3. Data structures and type definitions

### 3.1 Marker line

```
"--- relay " + HH:MM:SS + ": " + text + " ---\n"
```

`HH:MM:SS` is `rt.Now()` in local time, the same clock and format
`relay status` prints for `since`. `text` is one of:

| event | text |
|---|---|
| switch | `switched to <new token> (<reason>)` -- `reason` is the string `switchBuilder` received |
| done | `stopped: done` |
| unbind | `stopped: unbind` |

The line is written whole, in one call, so a concurrent builder write
cannot split it (`O_APPEND` writes of one line are atomic on the platforms
relay runs on).

## 4. Interface definitions and component contracts

### 4.1 `appendLogMarker` (new, `headless.go`)

Single responsibility: put one relay line in a builder log without ever
failing the caller.

```
func appendLogMarker(path string, now time.Time, text string)
```

Preconditions: none. `path == ""` is a no-op (an endpoint that never had a
process).
Postconditions: the file at `path` exists and ends with the marker line, or
a `slog.Warn("builder log marker", "path", path, "err", err)` was emitted
and the file is as it was. No return value: the marker is a courtesy to the
reader, and no relay action depends on it.
Opens with `os.O_APPEND|os.O_CREATE|os.O_WRONLY`, mode `0o644` -- the same
mode `proc` uses.

### 4.2 `stopProcess` (existing, gains one argument)

```
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint, why string) (int, error)
```

Contract as today, plus: after `rt.Runner.Kill` returns `nil` (and only
then), `appendLogMarker(e.LogPath, rt.Now(), "stopped: "+why)`. A failed
kill writes no marker: the process is still running and the log is still
its own.

### 4.3 `switchBuilder` (existing, one call added)

Directly after `tx.AppendLog(b.Name, switchEntry(...))` succeeds and before
`composePrompt`/`startRound`, when `b.Builder.Headless()`:

```
appendLogMarker(rt.Store.BuilderLogPath(b.Name, b.Round), now, "switched to "+res.Token()+" ("+reason+")")
```

`now` is the same instant the switch entry carries. The marker precedes the
replacement's first output because `startRound` has not run yet; it is
written whether the old process was killed here (`closeOld`) or found dead
by the gone trigger, because the file is the round's record either way.
If `startRound` then fails, the marker stands: the ledger's `spawn_failed`
says what happened next, and the next switch writes its own marker.

### 4.4 `supervisorScript` comment (existing, text only)

The sentence "The builder stays a child of this sh (no exec) so an OOM
kill of the builder still leaves a trailer." gains: "A group kill from
`Kill` takes the sh with it and leaves none; relay writes its own marker
line for every process it stops (`appendLogMarker`)."

## 5. High-level pseudocode

```
appendLogMarker(path, now, text):
    if path == "": return
    f := open(path, APPEND|CREATE|WRONLY, 0644); on error: warn, return
    write("--- relay " + now.Local().Format("15:04:05") + ": " + text + " ---\n"); on error: warn
    close

stopProcess(ctx, rt, e, why):
    ...as today up to Kill...
    if Kill ok: appendLogMarker(e.LogPath, rt.Now(), "stopped: " + why)
    return e.PID, nil

switchBuilder:
    ...resolve, closeOld, resolveBuilder, set fields, AppendLog(switchEntry)...
    if b.Builder.Headless():
        appendLogMarker(BuilderLogPath(name, round), now, "switched to " + token + " (" + reason + ")")
    ...composePrompt, startRound...
```

## 6. Error handling strategy

| error | handling |
|---|---|
| log unwritable (permissions, missing dir) | `slog.Warn`, action proceeds unchanged |
| `LogPath == ""` on the endpoint | no-op |
| `Kill` fails | no marker; existing error path unchanged |

The marker can never turn a successful switch, done or unbind into a
failure. That is the whole of the strategy.

## 7. Ordered implementation steps

1. **`appendLogMarker`** with tests: writes and appends (two calls give two
   lines in order, after pre-existing content); `path == ""` touches
   nothing; an unwritable path (a directory as `path`) returns without
   panicking and leaves no file.
2. **`stopProcess(why)`** and the two callers; tests: Done on a live
   headless process leaves `stopped: done` as the log's last line; unbind
   leaves `stopped: unbind`; a failed kill leaves no marker; Done on an
   idle headless endpoint (PID 0) writes nothing. Mutation: drop the
   marker call, the Done test fails.
3. **Switch marker** in `switchBuilder`; tests: after a headless switch
   the log's last line is the marker naming the new token and the reason;
   the marker precedes the fake runner's `specs` entry for the new start
   (i.e. exists once `switchBuilder` returns, and the file had no marker
   before); a pane switch creates no log file.
4. **Comment and README.**
