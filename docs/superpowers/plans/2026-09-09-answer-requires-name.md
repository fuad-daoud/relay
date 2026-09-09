# `relay answer` Must Name Its Binding — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `relay answer` refuse to guess which builder it is answering, the same way `done`, `unbind` and `fork` already refuse.

**Architecture:** `cmdAnswer` currently resolves its target through `resolveBinding`, which falls back to the binding owning `os.Getwd()`. Peer builders live in their own worktrees, so from the planner's pane that fallback always resolves to builder #1 — meaning an unqualified `relay answer --keys enter` presses a key into the wrong agent's approval dialog. `answer` moves onto the existing `explicitBinding` + `bindingHint` pattern, which is exactly what the other refuse-to-guess verbs use.

**Tech Stack:** Go 1.x, stdlib `flag` and `testing`. No new dependencies.

## Global Constraints

- Package `main` in `cmd/relay`. No new third-party dependencies.
- Reuse `explicitBinding(nameFlag string, positional []string) (string, bool)` (`cmd/relay/main.go:788-797`) and `bindingHint(verb string) string` (`cmd/relay/main.go:802-817`). Do not write a new refusal helper.
- The refusal must happen **before** `newRuntime()`, matching `cmdDone`, `cmdUnbind` and `cmdFork` — a bad invocation should not touch the store or the herdr socket.
- `resolveBinding` stays exactly as it is. `send`, `pull` and `diff` keep the cwd fallback: they are not destructive and their worst case is a confusing error, not a keystroke into the wrong agent.
- CLI tests drive the unexported dispatcher `run([]string{...})`, never `cmdX` directly.
- Every task ends green: `go build ./... && go test ./...`.

**Known flag-ordering quirk (do not try to fix here):** `parseFlags` is a plain `fs.Parse`, so Go's flag package stops at the first non-flag argument. `relay answer webshop --keys enter` therefore yields three positionals, `explicitBinding` returns `!ok`, and the caller gets the usage error. That is a *safe* failure — a refusal, never a keystroke into the wrong pane — and it is identical to how `done`, `unbind` and `fork` already behave. The working forms are `relay answer --keys enter webshop` and `relay answer --name webshop --keys enter`.

---

### Task 1: `answer` refuses to guess

**Files:**
- Modify: `cmd/relay/main.go:619-645` (`cmdAnswer`)
- Test: `cmd/relay/main_test.go` (append)

**Interfaces:**
- Consumes: `explicitBinding(nameFlag string, positional []string) (string, bool)`; `bindingHint(verb string) string`; `run(args []string) error`; `relay.AnswerInput{Keys string, Text string, Choice int}`; `relay.Answer(ctx context.Context, rt Runtime, name string, in AnswerInput) error`.
- Produces: no new exported surface. `cmdAnswer` keeps its signature `func cmdAnswer(args []string) error`.

- [ ] **Step 1: Confirm the current suite is green**

Run: `cd /home/fuad/projects/relay && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 2: Write the failing test**

Append to `cmd/relay/main_test.go`:

```go
func TestAnswerRefusesToGuessTheBinding(t *testing.T) {
	// A bare `relay answer` used to resolve to whichever binding owns the cwd.
	// With peer builders that is always builder #1, so an unqualified answer
	// pressed a key into a dialog nobody had looked at.
	err := run([]string{"answer", "--keys", "enter"})
	if err == nil {
		t.Fatal("a bare relay answer must be refused")
	}
	if !strings.Contains(err.Error(), "will not guess which one you meant") {
		t.Fatalf("expected a refuse-to-guess error, got %v", err)
	}
	if !strings.Contains(err.Error(), "usage: relay answer") {
		t.Fatalf("expected the usage line, got %v", err)
	}
}

func TestAnswerRefusesBothNameAndPositional(t *testing.T) {
	err := run([]string{"answer", "--name", "webshop", "--keys", "enter", "webshop"})
	if err == nil || !strings.Contains(err.Error(), "will not guess which one you meant") {
		t.Fatalf("naming the binding twice must be refused, got %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestAnswerRefuses -v`
Expected: FAIL. `TestAnswerRefusesToGuessTheBinding` fails because the bare invocation is *not* refused — it falls through `resolveBinding` and errors with `no binding for <cwd>; run relay bind first`, or worse, succeeds against a real binding.

- [ ] **Step 4: Write the implementation**

In `cmd/relay/main.go`, replace `cmdAnswer` (lines 619-645) in full:

```go
func cmdAnswer(args []string) error {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	name := fs.String("name", "", "binding whose builder to answer")
	keys := fs.String("keys", "", "logical key to send, e.g. enter or esc")
	text := fs.String("text", "", "literal text to send")
	choice := fs.Int("choice", 0, "numbered dialog option to pick")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// answer presses a key into a live dialog, and with peer builders the cwd
	// fallback resolves to builder #1 every time -- the planner's tree is the
	// one it owns, while peers live in their own worktrees. Guessing here
	// answers a prompt nobody read, so answer joins done and unbind in
	// refusing to guess.
	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relay answer <name> (--keys K | --choice N | --text S)  (or --name <name>)%s\n"+
			"answer types into a live dialog; it will not guess which one you meant",
			bindingHint("answer"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if err := relay.Answer(context.Background(), rt, target,
		relay.AnswerInput{Keys: *keys, Text: *text, Choice: *choice}); err != nil {
		return err
	}

	fmt.Printf("answered %s's builder\n", target)
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestAnswer -v`
Expected: PASS, including the pre-existing `answer` help test if one is present.

- [ ] **Step 6: Verify the accepted forms still work**

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay answer --name nosuchbinding --keys enter`
Expected: it gets **past** the usage refusal and fails inside `relay.Answer` with a not-found error naming `nosuchbinding`. If it prints the `usage: relay answer` line instead, `explicitBinding` is being handed the wrong arguments.

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay answer --keys enter nosuchbinding`
Expected: the same not-found error — the trailing positional is accepted.

- [ ] **Step 7: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "fix(relay): relay answer must name its binding

answer resolved its target through resolveBinding, which falls back to the
binding owning the cwd. Peer builders live in their own worktrees, so from
the planner's pane that fallback is always builder #1 -- a bare
\`relay answer --keys enter\` pressed a key into a dialog nobody had read.

answer now uses explicitBinding, joining done, unbind and fork in refusing
to guess. send, pull and diff keep the fallback: they are not destructive.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 2: Documentation

**Files:**
- Modify: `README.md:145-147` (the `relay answer` bullet)
- Modify: `README.md:~180` (the paragraph beginning "`--name` defaults to whichever binding owns the current working directory")

**Interfaces:**
- Consumes: the behaviour shipped in Task 1.
- Produces: nothing code-facing.

- [ ] **Step 1: Update the `answer` bullet**

In `README.md`, replace this bullet:

```markdown
- `relay answer [--name N] (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent).
```

with:

```markdown
- `relay answer NAME|--name N (--keys K | --choice N | --text S)` — answer a
  builder that's blocked at a dialog, via `send-keys` rather than a typed
  prompt (herdr refuses `agent prompt` against a blocked agent). The binding
  is **required**: answering types a key into a live dialog, so relay will not
  guess which builder you meant.
```

- [ ] **Step 2: Update the `--name` defaulting paragraph**

In `README.md`, replace:

```markdown
`--name` defaults to whichever binding owns the current working directory for
`send`, `pull`, `diff`, `answer` and `status`. It is **required** for `done` and
`unbind`: those are the destructive verbs and they refuse to guess (see below).
```

with:

```markdown
`--name` defaults to whichever binding owns the current working directory for
`send`, `pull`, `diff` and `status`. It is **required** for `answer`, `done` and
`unbind`: those act on a specific loop — `answer` types into a live dialog, the
other two end one — and they refuse to guess (see below).
```

- [ ] **Step 3: Verify no stale claims remain**

Run: `cd /home/fuad/projects/relay && grep -n "answer" README.md`
Expected: every hit describes the binding as required. No remaining text says `answer` defaults from the cwd.

- [ ] **Step 4: Commit**

```bash
cd /home/fuad/projects/relay
git add README.md
git commit -m "docs(relay): answer names its binding

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

## Notes for the reviewer

- **Why not also fix `send`?** Sending to the wrong builder is loud and recoverable — the wrong builder reports on a plan that makes no sense, and the round log shows exactly what happened. Answering the wrong builder is silent: a key lands in a dialog and the only trace is a decision nobody made. Different blast radius, different rule.
- **Why is `bindingHint("answer")` still useful when the cwd binding is the *wrong* guess?** The hint does not act — it prints `this directory is bound as "api", so you probably want: relay answer api`. When the planner ran `answer` from the project root and meant builder #1, that is the right nudge; when it meant a peer, the printed name makes the mismatch obvious. Either way a human reads it before anything is typed.
