# #303 step 3: planner delivery without herdr, and `internal/herdr` goes

This plan stands alone: everything you need is in this file and in the tree.
The design is `docs/specs/2026-09-22-drop-herdr-design.md` (§1.2 D4–D7,
§3.6, §4.6, §4.8, §5.4, §5.5). Steps 1a, 1b, 2 and 2b have merged:
- the planner registry and `Resolve` are in `internal/planner`;
- channel claims are keyed by planner id;
- builders and consults are headless or remote only.

**D6 was revised (#328, merged).** A plain `claude` launch runs `relay mcp`
in **tools mode**, and its reports arrive by a **background wait**, not a
push. Read the spec's §1.1 ("The channel needs a flag…", "Background
commands wake an idle session"), D6, §4.5, §4.8 and §5.4 as they are on
`main` now. Where this plan and the spec differ, the spec's revised D6 text
wins, and you say so in the report.

If a step is impossible as written or contradicts the code, **halt and
report**. Do not improvise around it.

**Work directly: do not dispatch sub-agents or explore agents.**

## Working efficiently (read this before step 1)

Your provider has about **3 s of round-trip latency per model step**,
whatever the step does. Generation itself is fast. The last comparable
round took 307 steps, and 261 of them made a single tool call. The cost
of this round is its **number of steps**, so:

1. **Batch independent tool calls in one step.** When you need several
   files, ranges or searches that don't depend on each other, issue them
   all as parallel tool calls in the same response. The same goes for
   independent edits to different files.
2. **Read once, from the locations given.** This plan names files,
   functions and line ranges. Read each file you will edit once (the named
   ranges, or the whole file if it's under ~400 lines) before editing it.
   Don't grep for things the plan already located, and don't re-read a
   range you have already seen unless you edited it.
3. **Edit in as few calls as possible.** Make every change to a file in one
   edit call (several hunks), or rewrite the file when most of it changes.
   Prefer one scripted edit (a short `python3` or `perl` over named ranges)
   to many single-hunk edits when the change is mechanical.
4. **One tight build/test loop.**
   - Iterate with `go build ./... 2>&1 | head -80` and focused
     `go test ./<pkg> -run '<names>'`.
   - Fix **every** error a run reports before running again.
   - Run `make check` once when you believe you are done, and once more
     only if it fails.
5. Don't run `git status`/`git diff` between edits for reassurance. Run
   them at the start and before the commit.

## Done means

- `grep -rli herdr --include='*.go' . | grep -v _test` is **empty**.
- No production code reads `HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`,
  `HERDR_SOCKET_PATH` or `HERDR_ENV`.
- `make check` passes.

Tests may still say "herdr" only in comments explaining history. No test
may construct or fake a herdr client: CI has no herdr.

## 0. First pass: delete by script, not by hand

Most of this round is deletion. Do the mechanical part in **one** command
per file with the tool below, then let the compiler list what's left.

1. Save the tool to `/tmp/deldecl/main.go` and build it:
   `cd /tmp/deldecl && go mod init deldecl >/dev/null 2>&1; go build -o deldecl .`
   It deletes named top-level declarations (and their doc comments) from one
   file, then gofmt-formats it. A name it can't find aborts with nothing
   written. Usage: `/tmp/deldecl/deldecl FILE NAME [NAME...]`; a method is
   `Recv.Method`.

```go
// deldecl deletes named top-level declarations (with their doc comments)
// from Go files, in one pass, then gofmt-formats the result.
//
//	go run deldecl.go FILE NAME [NAME...]
//
// NAME is a func, type, const or var name, or Recv.Method for a method
// (Recv without '*'). A const/var/type inside a grouped (...) declaration
// removes only that spec; the group goes when it becomes empty. Unknown
// names are an error, and nothing is written, so a typo never half-edits.
package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"sort"
)

type span struct{ from, to int }

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: deldecl FILE NAME [NAME...]")
		os.Exit(2)
	}
	path, names := os.Args[1], os.Args[2:]
	src, err := os.ReadFile(path)
	must(err)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	must(err)
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	found := map[string]bool{}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var spans []span
	cut := func(doc *ast.CommentGroup, from, to token.Pos) {
		if doc != nil {
			from = doc.Pos()
		}
		s, e := off(from), off(to)
		for e < len(src) && src[e] == '\n' { // take the trailing newline(s) with it
			e++
			break
		}
		spans = append(spans, span{s, e})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			key := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) == 1 {
				t := d.Recv.List[0].Type
				if st, ok := t.(*ast.StarExpr); ok {
					t = st.X
				}
				if ix, ok := t.(*ast.IndexExpr); ok {
					t = ix.X
				}
				if id, ok := t.(*ast.Ident); ok {
					key = id.Name + "." + d.Name.Name
				}
			}
			if want[key] {
				found[key] = true
				cut(d.Doc, d.Pos(), d.End())
			}
		case *ast.GenDecl:
			var keep int
			var hits []ast.Spec
			for _, s := range d.Specs {
				hit := false
				switch s := s.(type) {
				case *ast.TypeSpec:
					if want[s.Name.Name] {
						found[s.Name.Name], hit = true, true
					}
				case *ast.ValueSpec:
					all := true
					for _, n := range s.Names {
						if want[n.Name] {
							found[n.Name] = true
						} else {
							all = false
						}
					}
					if all {
						hit = true
					} else {
						for _, n := range s.Names {
							if want[n.Name] {
								fmt.Fprintf(os.Stderr, "deldecl: %s shares a spec with other names; edit it by hand\n", n.Name)
								os.Exit(1)
							}
						}
					}
				}
				if hit {
					hits = append(hits, s)
				} else {
					keep++
				}
			}
			if len(hits) == 0 {
				continue
			}
			if keep == 0 {
				cut(d.Doc, d.Pos(), d.End())
				continue
			}
			for _, s := range hits {
				var doc *ast.CommentGroup
				switch s := s.(type) {
				case *ast.TypeSpec:
					doc = s.Doc
				case *ast.ValueSpec:
					doc = s.Doc
				}
				cut(doc, s.Pos(), s.End())
			}
		}
	}
	var missing []string
	for _, n := range names {
		if !found[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "deldecl: not found in %s: %v (nothing written)\n", path, missing)
		os.Exit(1)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from > spans[j].from })
	out := src
	for _, s := range spans {
		out = append(append([]byte{}, out[:s.from]...), out[s.to:]...)
	}
	formatted, err := format.Source(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deldecl: result does not parse (%v); nothing written\n", err)
		os.Exit(1)
	}
	must(os.WriteFile(path, formatted, 0o644))
	fmt.Printf("deldecl: %s: removed %d declaration(s)\n", path, len(spans))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "deldecl:", err)
		os.Exit(1)
	}
}
```

2. Run this manifest in one step (a single shell command chaining them all):

```
git rm -q -r internal/herdr internal/serve/herdr.go \
  internal/relay/held.go internal/relay/foreign.go internal/relay/coverage.go \
  internal/relay/daemon_events.go
D=/tmp/deldecl/deldecl
$D internal/relay/send.go promptWithRetry Target ErrPromptLate lateScanLines
$D internal/relay/herdr.go Herdr SameAgent findAgentIndex FindAgent
$D internal/relay/finished.go notifyFinished finishedTitle finishedBody
$D internal/relay/diagnose.go movedPaneWarning
```

   Then delete the matching `*_test.go` files for the removed files
   (`held_test.go`, `foreign_test.go`, `coverage_test.go`,
   `daemon_events_test.go`, and `internal/herdr`'s own tests went with the
   package).
3. `go build ./... 2>&1 | head -120` and fix **every** listed error in one
   pass, grouping edits by file (one edit call per file). Use `deldecl` again
   for any further whole declarations the errors show are herdr-only. Repeat
   build → fix until it builds. Only then move on to the sections below,
   which describe what the remaining code must do.

The manifest is a starting point and doesn't cover everything. If a name
in it no longer exists on your tree, drop that name and say so in the
report; don't halt for it.

## 1. Delivery (spec §5.4)

`DeliverPending` becomes exactly the §5.4 pseudocode:
1. the channel route (a live claim for `b.PlannerID`);
2. then `rt.Deliverers[b.Planner.Kind]`, #300's port, **unchanged**;
3. otherwise the entry stays pending with `Delivery.Route = "pull"` and
   `Delivery.Reason = "awaiting pull for planner <name> (<kind>)"`. For a
   Claude Code planner in tools mode, this is the **normal** path (the
   background wait's `relay pull` delivers it), not a fault. There is **no**
   `Deliverers["claude"]`.

Every `LogEntry` for a delivery gains `route` (`channel`,
`deliverer:<kind>` or `pull`). `relay pull` marks a pending entry
delivered with `route=pull`.

Delete:
- the pane path: `FindAgent`, the planner-status gate, `promptWithRetry`,
  `Target`, `ErrPromptLate` and `lateScanLines`;
- #300's pane fallback: `OutcomeNotMine` past `FallbackAfter` stops falling
  through to a pane. `OutcomeNotMine` now leaves the entry pending with the
  deliverer's reason. Keep the `Outcome` type and every other value;
- `held.go`, the `HELD` state, `--held-grace`, `PlannerScreen`, the
  planner-screen fingerprint and the hold clock in status;
- **ORPHANED**: the state, and everything that sets it. A binding whose
  planner has no route is simply pending. `BROKEN` stays **only** if a
  non-herdr path still sets it (a headless builder's process vanishing);
  otherwise delete it too, and say which in the report.

States that vanish must still **load**. A `bind.json` with `state: held`
or `state: orphaned` loads as `active`, with a one-line debug log. Add the
mapping where `store` decodes `State`.

## 2. Notifications and the daemon (D4)

- Delete `Notify` and every caller: halt, stall, stale and all-finished
  (finished.go, daemon_events.go). Keep the `LogEntry` records they sat
  beside.
- Delete the daemon's per-tick `ListAgents` and the socket event
  subscription (#146, `Subscribe`, `daemon_events.go`). The daemon ticks on
  its interval alone.
- Delete foreign-agent rows (`foreign.go`) and sub-agent coverage
  (`coverage.go`), and their status lines.

## 3. The port, the package, the env

- Delete `relay.Herdr` (`internal/relay/herdr.go`; keep whatever else that
  file holds, renamed `runtime.go` if it becomes only the `Runtime`
  struct), `Runtime.Herdr`, and every herdr type leaking into other
  packages: `herdr.Agent`, `Sound`, `PaneMetadata`, `Event`,
  `ErrNoSocket`, `IntegrationState`, `MinVersion`, `SoundRequest`. Then
  `git rm -r internal/herdr`.
- `internal/serve/herdr.go` and `HerdrError` (serve admin): delete.
- `names.go` `MaxAgentNameLen` came from herdr's 32-character agent-name
  cap. `store.ValidName` keeps its **current** rule, so no existing binding
  name becomes invalid; move the constant into `store` with a comment
  saying where the number came from.
- `internal/harness`: delete `Integration` (herdr integration install) and
  `SubAgents`, plus anything in `relay agent` / `relay init` that installs
  a herdr integration.
- Every remaining `HERDR_*` read: delete. `Planner.PaneID` is written by
  nothing (it stays loadable; spec §3.2).

## 4. Status JSON (spec §3.6)

- **Remove:** `planner_pane`, `planner_status`, `planner_focused`,
  `workspace`, `builder_pane`, `foreign`, `sub_agents`, `hold`,
  `herdr_error`, and any other field left fed by deleted code.
- **Add:** `planner_route` (`channel` | `deliverer` | `pull`; `pull` is a route, not a fault) and
  `planner_route_live` (bool).
- Update **every** consumer in the tree in this round: `relay status`
  text, `statusline`, `internal/ui` (rail and detail pane, including the
  foreign rows and the "terminal" tab source line if it still names a
  pane), serve `FlatStatus`, and the MCP `status` tool.
- The remote wire protocol (`internal/remote/proto.go`) must not change.
  Check that with `git diff`.

## 5. doctor (spec §4.8)

Delete every herdr row: the binary, the version, `MinVersion`, the socket,
integrations and pane env. Keep `plugin` and `plugin hook` from step 1b.
**Change the `planner` row** to the revised §4.8:
- **FAIL** when, from a Claude Code session, `Resolve()` misses, or no `relay
  mcp` process is a child of the planner's host process (read
  `/proc/<pid>/task/*/children` or `ps --ppid`, whichever the tree already
  uses; a pure-function test gets the process list injected);
- a missing live channel claim is **INFO**, never FAIL, with the spec's
  text ("tools mode: reports arrive by background wait. For push, launch
  with `--dangerously-load-development-channels plugin:relay@relay`, or
  have an org admin add relay to `allowedChannelPlugins`").

Step 1b's current behaviour, FAIL on no claim, is what changes. `UsableBuilder` means "binary on PATH and
the candidate parses".

## 6. MCP (spec §4.5, revised D6)

- **The instructions depend on the mode.** The mode is known before
  `initialize` is answered, so `relay mcp` serves one of two texts:
  - *channel*: today's text, minus every `broken`/`orphaned` mention;
  - *tools*: no events arrive. After every `send`, start the background wait
    for that binding and end the turn. When the wait exits, act on its
    output as you would a `report` or `needs_you` event.
  
  Use the spec's §4.5 wording.
- **In tools mode, the `send` tool's result ends with the command**, exactly:

  ```
  background wait (run with run_in_background, then end your turn):
    relay wait --name <name> --timeout <budget>; relay pull --name <name>
  ```

  `<budget>` is the binding's round budget (the `send --timeout` default is
  24h). Check how `send` stores it and render it in a form `relay wait
  --timeout` accepts. In **channel** mode the result has no such line.
- The instructions tell the model to act on the pull output after every
  wait exit except `WaitTimeout`. On `WaitTimeout`, run `relay status
  --name <name>` and start the wait again if the round is still running.
- The `status` tool's rows follow §4.
- The wording still saying "this pane" (tools.go, instructions.go) becomes
  "this planner".

## 7. Tests

- **Delete** tests whose behaviour is deleted here: pane delivery, held
  and HELD, the grace clock, ORPHANED, notifications, socket events,
  foreign/coverage rows, herdr doctor rows, and `internal/herdr`'s own
  tests.
- **Port** (behaviour survives): the delivery tests become channel /
  deliverer / pending tests; the daemon tick tests lose `ListAgents`; the
  status goldens are regenerated from the new JSON (review each golden
  diff, and say in the report that you did); `FlatStatus`.
- **New:**
  - `TestDeliverPendingNoRouteStaysPendingAsPull`;
  - `TestDeliverPendingDelivererNotMineStaysPending` (the old fallback is
    gone);
  - `TestPullMarksDeliveredRoutePull`;
  - `TestLoadMapsHeldAndOrphanedToActive`;
  - `TestStatusJSONHasRouteFields` (`planner_route` is `pull` for a tools-mode
    planner, `channel` with a live claim);
  - `TestMCPToolsModeSendResultCarriesBackgroundWait`: exact command, this
    binding's name and budget;
  - `TestMCPChannelModeSendResultHasNoWaitLine`;
  - `TestMCPInstructionsDependOnMode`;
  - `TestDoctorPlannerRowNoClaimIsInfo` and
    `TestDoctorPlannerRowNoMCPChildFails`.
- Remove the fake herdr from every test package. Where a test only needed
  it to satisfy `Runtime.Herdr`, drop the field. The `internal/e2e` remote
  tests keep running without it: assert on the queued report / mailbox
  instead of a typed prompt.
- `internal/relay/e2e_test.go` (build tag `e2e`) drives a real herdr
  session. **Delete the file and its `testdata/e2e-shim.sh`.** Step 5
  replaces them with a CI-run headless e2e. Leave the Makefile's `e2e`
  target alone; step 4 handles it.

Mutation checks: do these and report each result.
0. Drop the command from the tools-mode `send` result.
   `TestMCPToolsModeSendResultCarriesBackgroundWait` fails.
1. Put back a fall-through from `OutcomeNotMine` to "delivered".
   `TestDeliverPendingDelivererNotMineStaysPending` fails.
2. Drop the held/orphaned load mapping. `TestLoadMapsHeldAndOrphanedToActive`
   fails.

Restore by re-editing.

## 8. Verification before you report

- `make check` passes, including `test -z "$(gofmt -l .)" || { gofmt -l .;
  exit 1; }`.
- The two `grep` lines under "Done means" are empty; paste them.
- `git diff --stat -- internal/remote/proto.go` is empty.
- Report:
  - tests deleted, one line each, grouped by file;
  - tests ported;
  - tests added;
  - golden diffs reviewed;
  - both mutation results;
  - `git diff --shortstat`;
  - every departure with its reason.
- Commit with `feat(deliver): …` (or split into several commits, one per
  section). Don't rebase and don't push.
