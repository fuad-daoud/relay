# Usage surfaces, round 2: wait for the headless stream's trailer (#142)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-18-round-usage-design.md` §5
(amended by this round: the "claude stream" and "agy stream" rules gain
the wait below) and `docs/specs/2026-09-18-usage-surfaces-design.md`.
**Earlier round:** `docs/plans/2026-09-18-usage-surfaces.md` (all seven
tasks landed on this branch).
**Issue:** #142

## Why

Round 1's surfaces exposed a slice-1 bug on the first real round they
printed. A headless builder writes `NNN-done` as its last tool call; the
daemon closes the round on its next tick; the harness writes its final
`result` event and exits **after** that. Measured on 2026-09-18:
`001-done` at 14:34:43, the stream's last write at 14:34:44, round closed
14:34:43. At close time the stream had no `result`, so `claudeStream`
took its killed-round fallback -- the assistant events -- and recorded
`unknown (no price for anthropic/claude-sonnet-5)` with 313 output
tokens, against a `result` event of `total_cost_usd: 1.06` and 30,240
output tokens that landed one second later. The fallback is also
near-useless for claude: its per-event `usage` is not the message's
total.

The fix: for a headless source, wait for the stream's `relay-exit:`
trailer before reading. The trailer is written by relay's own supervisor
after the harness exits, so once it is there the `result` event is
there. The wait is bounded by `recordUsage`'s existing 5 s deadline;
typical wait is about one second. On timeout, read what exists and say
so.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/usage-surfaces` | **the git worktree.** Your shell's cwd, branch `relay/usage-surfaces`, seven commits ahead of `main`. |
| `~/.local/state/relay/usage-surfaces` | relay's drop directory. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**.

## Running commands

`make` is intercepted on this machine; run the constituents directly:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr` or any harness. No test under `cmd/relay`.

## Global constraints

- No new dependencies. `internal/usage` still imports nothing from
  `internal/relay`, `internal/store` or `internal/proc`: the trailer
  prefix is a string constant in `internal/usage` with a comment naming
  `proc.ExitTrailer` as its source, and a test in `internal/relay` (which
  may import both) pins the two equal.
- The round still always closes. The wait is inside the reader, under
  the caller's context; a reader that overruns is a note, never an error.
- One commit, message `fix(usage): wait for the headless stream's trailer before reading (#142)`.

---

### Task 1: `readStream` waits for the trailer

**Files:**
- Modify: `internal/usage/source.go` (`Read`, `readStream`),
  `docs/specs/2026-09-18-round-usage-design.md` §5 (amend, see Step 5)
- Test: `internal/usage/source_test.go`, `internal/relay/usage_test.go`

**Interfaces:**
- Changes: `readStream(ctx context.Context, src Source) ([]Sample, string)`
  gains the context. Nothing exported changes.
- Produces:

```go
// exitTrailer is proc.ExitTrailer, the last line relay's supervisor
// writes to a headless stream after the harness exits. Copied, not
// imported, to keep this package free of relay's process model; the
// relay package pins the two equal.
const exitTrailer = "relay-exit:"

// trailerPoll is how often a still-open stream is re-checked.
const trailerPoll = 200 * time.Millisecond

// streamClosed reports whether the stream's last non-empty line is the
// exit trailer.
func streamClosed(path string) bool

// waitClosed blocks until streamClosed or ctx is done; reports which.
func waitClosed(ctx context.Context, path string) bool
```

- [ ] **Step 1: Write the failing tests.** Append to `internal/usage/source_test.go`:

```go
func TestStreamClosed(t *testing.T) {
	_, open := mustTemp(t, "{\"type\":\"assistant\"}\n")
	if streamClosed(open) {
		t.Error("no trailer: must be open")
	}
	_, closed := mustTemp(t, "{\"type\":\"result\"}\n\nrelay-exit:0\n")
	if !streamClosed(closed) {
		t.Error("trailer as last line: must be closed")
	}
	_, mid := mustTemp(t, "relay-exit:0\n{\"type\":\"assistant\"}\n")
	if streamClosed(mid) {
		t.Error("trailer not last: must be open")
	}
	if streamClosed("/nonexistent") {
		t.Error("missing file is not closed")
	}
}

func TestReadHeadlessWaitsForTrailer(t *testing.T) {
	// Start with everything but the result event and the trailer, append
	// them 300 ms later, and expect the result-based sample.
	raw, err := os.ReadFile("testdata/claude-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	head := strings.Join(lines[:len(lines)-2], "\n") + "\n"
	tail := strings.Join(lines[len(lines)-2:], "\n") + "\n"
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(tail)
		f.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, note := New(nil, t.TempDir()).Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "" || len(got) != 1 || !got[0].HasCost {
		t.Fatalf("want the measured result sample after the trailer lands; got %d samples, note %q, %+v", len(got), note, got)
	}
}

func TestReadHeadlessTimesOutOnOpenStream(t *testing.T) {
	raw, _ := os.ReadFile("testdata/claude-stream-killed.jsonl")
	// The killed fixture ends in a trailer; strip it to make an open stream.
	body := strings.TrimSuffix(strings.TrimRight(string(raw), "\n"), "relay-exit:137")
	_, path := mustTemp(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	got, note := New(nil, t.TempDir()).Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "stream still open" {
		t.Errorf("note = %q, want \"stream still open\"", note)
	}
	if len(got) == 0 {
		t.Error("an open stream is still read: the fallback samples must come back with the note")
	}
}
```

Add `strings`, `time` to the imports.

Append to `internal/relay/usage_test.go`:

```go
func TestExitTrailerMatchesProc(t *testing.T) {
	if usage.ExitTrailerForTest() != proc.ExitTrailer {
		t.Fatalf("usage.exitTrailer %q != proc.ExitTrailer %q: the reader would wait forever on every headless round",
			usage.ExitTrailerForTest(), proc.ExitTrailer)
	}
}
```

with `"github.com/fuad-daoud/relay/internal/proc"` imported.
`ExitTrailerForTest` is a one-line exported accessor defined in
`source.go` in Step 3 (a `_test.go` file would not be visible across
packages).

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/usage/ -run 'TestStreamClosed|TestReadHeadless'; go test ./internal/relay/ -run TestExitTrailer`
Expected: build failures.

- [ ] **Step 3: Implement** in `internal/usage/source.go`.

```go
// exitTrailer is proc.ExitTrailer: the last line relay's supervisor
// writes to a headless stream, after the harness has exited. Copied, not
// imported, so this package stays free of relay's process model;
// TestExitTrailerMatchesProc in internal/relay pins the two equal.
const exitTrailer = "relay-exit:"

// ExitTrailerForTest exposes exitTrailer so internal/relay can pin it to
// proc.ExitTrailer; nothing else calls it.
func ExitTrailerForTest() string { return exitTrailer }

// trailerPoll is how often a still-open stream is re-checked.
const trailerPoll = 200 * time.Millisecond

// tailProbe is how much of the file's end is read to find the last line.
const tailProbe = 256

// streamClosed reports whether path's last non-empty line is the exit
// trailer -- the harness has exited and its final event is on disk.
func streamClosed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	n := int64(tailProbe)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	last := lines[len(lines)-1]
	return strings.HasPrefix(last, exitTrailer)
}

// waitClosed blocks until the stream is closed or ctx is done, and
// reports which. A round closes on the builder's done marker, which the
// harness writes before its final event: the wait is what turns a race
// into a measured figure.
func waitClosed(ctx context.Context, path string) bool {
	for {
		if streamClosed(path) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(trailerPoll):
		}
	}
}
```

Change `Read` to pass `ctx` and `readStream` to take it:

```go
func (r reader) readStream(ctx context.Context, src Source) ([]Sample, string) {
	if _, err := os.Stat(src.StreamPath); err != nil {
		return nil, "no stream"
	}
	closed := waitClosed(ctx, src.StreamPath)
	f, err := os.Open(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	defer f.Close()
	var samples []Sample
	switch src.Harness {
	case "claude":
		samples = claudeStream(f, src.Provider)
	case "agy":
		samples = agyStream(f, src.Provider, src.Model)
	case "opencode":
		samples = opencodeStream(f, src.Provider, src.Model)
	default:
		return nil, "no reader for " + src.Harness
	}
	switch {
	case len(samples) == 0:
		return nil, "no usage events"
	case !closed:
		return samples, "stream still open"
	}
	return samples, ""
}
```

Add `io`, `strings`, `time` to the imports. Existing tests in
`source_test.go` use fixtures that end in a trailer, so they do not wait.
`TestReadHeadlessNotes`' "unknown harness" case writes `relay-exit:0`
already; its "no events" case too. Check each fixture in `testdata/`
ends with a trailer line (they should from slice 1); if one does not,
stop -- that is a fixture bug worth naming, not silently fixing.

The `"stream still open"` note reaches `Fold` as the note argument only
when there are zero samples; with samples, `Fold` drops the note unless
the basis is unknown. So in `recordUsage` (`internal/relay/usage.go`),
after `u = usage.Fold(samples, rt.Prices, src.Plan, note)`, append the
reader's note when samples were returned with one:

```go
		if len(samples) > 0 && note != "" && !strings.Contains(u.Note, note) {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += note
		}
```

(import `strings`). This keeps "stream still open" visible on an
estimated or measured figure -- a reader must be able to tell a full
round from a truncated one.

- [ ] **Step 4: Run.**

Run: `go test -race -count=1 ./internal/usage/ ./internal/relay/`
Expected: PASS. `TestReadHeadlessWaitsForTrailer` takes ~300 ms,
`TestReadHeadlessTimesOutOnOpenStream` ~400 ms.

- [ ] **Step 5: Amend the slice-1 spec.** In
`docs/specs/2026-09-18-round-usage-design.md` §5, before the "claude
stream" bullet, add:

```markdown
- **every headless stream:** the reader first waits for the stream's
  `relay-exit:` trailer, polling every 200 ms under the caller's 5 s
  deadline. A round closes on the builder's done marker, which the
  harness writes *before* its final event (measured 2026-09-18: done at
  14:34:43, `result` at 14:34:44); without the wait the claude reader
  falls through to its killed-round fallback on every ordinary round.
  On timeout the stream is read as it is and the note gains
  `stream still open`.
```

And in the "claude stream" bullet, change "(the round was killed)" to
"(the round was killed, or the wait timed out)".

- [ ] **Step 6: Full check and commit.**

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
git add internal/usage/source.go internal/usage/source_test.go internal/relay/usage.go internal/relay/usage_test.go docs/specs/2026-09-18-round-usage-design.md docs/plans/2026-09-18-usage-surfaces-r2.md
git commit -m "fix(usage): wait for the headless stream's trailer before reading (#142)"
```

Copy this plan into the branch first (`cp
/home/fuad/projects/relay/docs/plans/2026-09-18-usage-surfaces-r2.md
docs/plans/`) so the commit above can add it.

## Report

End with the four check commands' last lines, `git log --oneline
main..HEAD` (eight commits), and the timings of the two new usage tests.
