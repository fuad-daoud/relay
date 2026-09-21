# Wave 2: opencode 2.x -- a killed or switched-away headless builder must actually stop (#256)

One bug, with a probe step first. This plan stands alone: everything you
need is in this file and in the tree. If a step is impossible as written or
contradicts the code, **halt and report** -- do not improvise around it.

You are a headless builder on a server-side worktree of this repo, on the
very machine where #256 was observed, running as the same user that owns
`~/.local/state/opencode/service.json`. Never run `make check` here (the
planner runs it); run the gate commands in §7 exactly as written. Every
command in the foreground; no sub-agents for edits. No git fetch/rebase.
**Do not kill any opencode process on this machine** -- other builders may
be running; probes are read-only unless §3 says otherwise, and the abort
test targets only a session you created yourself.

## 1. System overview

opencode 2.x runs one `opencode serve --service` per user; every
`opencode run` is a thin client of it. When the client dies (we saw
`⎿ error: Transport` on two builders in one second under load) the agent
**session keeps running inside the service** and keeps editing the
worktree relay has already switched away from; and `relay.Runner.Kill`
(`internal/proc/proc.go`, a process-group kill of the supervisor) stops
only the client. So `relay done`, `relay unbind`, `relay stop`, a gated
switch and #244's restart handling are all no-ops for an opencode 2.x
builder, and an "exited without a report" may mean only "client lost;
agent alive". Two possible fixes, decided by a probe you run first:

- **(A) a standalone mode.** If `opencode run` can be told not to use the
  shared service (a flag such as `--standalone`/`--no-service`/`--serve`
  or an env var), the opencode print form gains it and a process-group
  kill is a real kill again. Preferred: no HTTP, no credentials.
- **(B) session abort through the service API.** Otherwise: relay reads
  `service.json` (`{"url","password",...}`), finds the sessions whose
  `directory` is the binding's worktree, and aborts them -- on `Kill`, and
  before switching after an exit-without-report of an opencode builder.

## 2. Files (depends on the probe; list what you touch in the report)

```
internal/harness/harness.go        (A) opencode print form gains the standalone flag/env; (B) nothing
internal/harness/harness_test.go   (A) TestLaunchPrintPerKind row updated
internal/relay/opencode_abort.go   (B) + serviceDescriptor(), sessionsIn(dir), abortSession(id): pure HTTP client with basic auth from service.json; never logs the password
internal/relay/opencode_abort_test.go (B) httptest server standing in for the service
internal/relay/headless.go         (B) on Kill of an opencode builder and on exit-without-report with kind opencode: abort the worktree's sessions first; a stream whose last lines contain "error: Transport" is logged as "client lost; session aborted" and still switches
internal/proc/proc.go              nothing (Kill stays a process-group kill)
internal/doctor/doctor.go          opencode row: "2.x shared service" note with the session count when service.json exists (both A and B)
README.md                          headless builders: what relay does now for opencode 2.x
docs/plans/2026-09-21-w2x1-opencode-ghost.md   copy of this plan
```

## 3. The probe (do this first; write every result into the report verbatim)

1. `opencode --version`; `opencode run --help`; `opencode serve --help`;
   `opencode --help | grep -i -E "standalone|service|server|attach"`; and
   `env | grep -i opencode` (names only). Look for any flag or env var that
   makes `run` not attach to the shared service.
2. `cat ~/.local/state/opencode/service.json | python3 -c "import json,sys; d=json.load(sys.stdin); print({k:(v if k!='password' else '<redacted>') for k,v in d.items()})"`.
3. With `PW=$(python3 -c "import json;print(json.load(open('$HOME/.local/state/opencode/service.json'))['password'])")`
   and the `url` from that file, probe the API paths read-only, printing
   status code and content-type only, never bodies with secrets:
   `/doc`, `/session`, `/api/session`, `/v1/session`, `/session?directory=<this worktree>`
   with `-u opencode:$PW` and also with the header `x-opencode-directory: <this worktree>`.
   Find the one that lists sessions as JSON, and in that JSON the fields
   `id`, `directory`, `time.updated`.
4. Create a throwaway session of your own to test abort against:
   `opencode run -m opencode/nemotron-3.5-lightning-free 'sleep 20; echo hi' --format json` in
   the background is NOT allowed (background tasks are forbidden for you);
   instead, from the session list, pick **your own current session** (the
   one whose `directory` is this worktree and whose `time.updated` is
   newest) only to confirm the abort ENDPOINT SHAPE with an OPTIONS/HEAD or
   a GET on `/session/<id>` -- do not POST abort to your own session.
   Determine the abort route from `/doc` (OpenAPI) if it serves JSON:
   search it for `abort`.
5. Decision: if step 1 found a standalone mechanism -> (A). Else if steps
   3-4 found a working list route and an abort route -> (B). Else halt and
   report everything you found.

## 4. Interfaces (B)

```
// internal/relay/opencode_abort.go
type opencodeService struct { URL, Password string }
func loadOpencodeService(home string) (opencodeService, bool)     // ~/.local/state/opencode/service.json; false when absent/unparseable
func (s opencodeService) sessionsIn(ctx, dir string) ([]string, error) // session ids whose directory == dir (the route you found; basic auth user "opencode")
func (s opencodeService) abort(ctx, id string) error                  // the abort route you found (POST /session/{id}/abort or as documented)
func abortOpencodeSessions(ctx context.Context, rt Runtime, b store.Binding) int
    // kind must be "opencode"; loads the service; aborts every session in b.CWD; returns the count; logs each id at Info; never returns an error to the caller (best effort)
// headless.go: stopProcess(...) for kind opencode -> abortOpencodeSessions BEFORE Runner.Kill; exit-without-report path for kind opencode -> abortOpencodeSessions BEFORE the switch (and the note "client lost" when the stream tail has "error: Transport")
// The password is never logged or included in errors (test-pinned: an error string from a failing abort contains the host, not the password).
```

## 5. Tests

- (A): `TestLaunchPrintPerKind` opencode row includes the flag; `TestHeadlessLaunchPerKind` likewise.
- (B): `TestAbortOpencodeSessionsAbortsOnlyThisDirectory` (httptest: two sessions, one in `b.CWD`; exactly one abort POST); `TestStopProcessAbortsOpencodeFirst` (fake service via an injectable base URL on the Runtime or a package var; order: abort then Kill); `TestExitWithoutReportAbortsBeforeSwitch`; `TestServiceErrorsNeverLeakPassword`.
- Both: the doctor note test.

## 6. Steps

1. Probe (§3); write the results section of the report first.
2. Implement A or B per the decision, tests first.
3. Gate: `test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }`, `go vet ./...`, `go test -race -count=1 ./internal/relay/ ./internal/harness/`, `go test -count=1 ./...`, `go mod tidy && git diff --exit-code go.mod go.sum`.
4. Copy the plan to `docs/plans/2026-09-21-w2x1-opencode-ghost.md`; commit the code as one `fix(opencode): ...` naming the chosen design; the plan copy as `chore(plans):`.

## Report

The probe results verbatim (redacted password), the decision and why, then
per task: tests, verify output, commit shas. If neither A nor B was
possible, the probe results are the deliverable: halt there.
