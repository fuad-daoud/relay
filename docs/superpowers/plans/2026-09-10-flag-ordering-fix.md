# Flag Ordering Fix (#48) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `relay` parse flags that appear after a positional binding name, so the invocation forms the README documents actually work.

**Architecture:** `parseFlags` is a plain `fs.Parse`, and Go's `flag` package stops at the first non-flag argument — so `relay fork webshop --round 2` silently never parses `--round`. The fix parses iteratively: `Parse`, take the first leftover as a positional, `Parse` the remainder, repeat. Because `Parse` itself consumes each flag's value, this needs no knowledge of flag arity or boolean-ness. A final `Parse` over the collected positionals makes `fs.Args()` report them as before, so **no call site changes** — all five `fs.Args()` consumers (`fork`, `unbind`, `answer`, `done`, `log`) keep working untouched.

**Tech Stack:** Go 1.x, stdlib `flag` and `testing`. No new dependencies.

## Global Constraints

- Package `main` in `cmd/relay`. No new third-party dependencies.
- `parseFlags` keeps its exact signature: `func parseFlags(fs *flag.FlagSet, args []string) error`. Do not change it, and do not change any of its ~16 call sites.
- Positional order must be preserved. `explicitBinding` refuses two positionals, and that refusal must keep working.
- `-h` must keep returning `errHelpShown`, and unknown flags must keep erroring.
- There is no `--` passthrough anywhere in `cmd/relay` (verified), so greedy reordering is safe.
- CLI tests drive the unexported dispatcher `run([]string{...})`, never `cmdX` directly. Tests that reach `newRuntime()` must isolate state with `t.Setenv("HOME", ...)` and `t.Setenv("XDG_STATE_HOME", ...)`, matching `main_test.go:66-68`.
- Every task ends green: `go build ./... && go test ./...`.

---

### Task 1: Parse flags wherever they appear

**Files:**
- Modify: `cmd/relay/main.go:114-122` (`parseFlags`)
- Test: `cmd/relay/main_test.go` (append)

**Interfaces:**
- Consumes: `errHelpShown` (`cmd/relay/main.go:79`); `flag.ErrHelp`.
- Produces: no new exported surface. `parseFlags` keeps `func parseFlags(fs *flag.FlagSet, args []string) error`.

- [ ] **Step 1: Confirm the current suite is green**

Run: `cd /home/fuad/projects/relay && go test ./...`
Expected: PASS (all packages).

- [ ] **Step 2: Add the `flag` import to the test file**

`cmd/relay/main_test.go` currently imports `errors`, `io`, `os`, `os/exec`, `path/filepath`, `strings`, `testing`, and the store package. Add `"flag"` to that import block, keeping the group gofmt-sorted (it goes first, before `"io"`).

- [ ] **Step 3: Write the failing tests**

Append to `cmd/relay/main_test.go`:

```go
func TestParseFlagsAcceptsFlagsAfterPositionals(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	round := fs.Int("round", 0, "")
	tab := fs.Bool("tab", false, "")

	// This is the README's documented shape: the binding name first, its flags
	// after. Go's flag package stops at the first bare word, so before #48 both
	// flags below were silently dropped.
	if err := parseFlags(fs, []string{"webshop", "--round", "2", "--tab"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *round != 2 {
		t.Errorf("--round after a positional must still parse, got %d", *round)
	}
	if !*tab {
		t.Error("--tab after a positional must still parse")
	}
	if got := fs.Args(); len(got) != 1 || got[0] != "webshop" {
		t.Errorf("fs.Args() = %v, want [webshop]", got)
	}
}

func TestParseFlagsKeepsEveryPositionalInOrder(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")

	if err := parseFlags(fs, []string{"alpha", "--name", "n", "beta"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *name != "n" {
		t.Errorf("--name = %q, want n", *name)
	}
	// explicitBinding refuses two positionals, so collapsing or reordering them
	// would quietly turn a refusal into a wrong guess.
	got := fs.Args()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("fs.Args() = %v, want [alpha beta]", got)
	}
}

func TestParseFlagsStillRejectsUnknownFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Before #48 this was swallowed with the rest of the tail.
	if err := parseFlags(fs, []string{"webshop", "--bogus"}); err == nil {
		t.Fatal("an unknown flag after a positional must still be rejected")
	}
}

func TestParseFlagsStillHandlesHelp(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, []string{"-h"}); !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestParseFlagsHandlesNoArguments(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, nil); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got := fs.Args(); len(got) != 0 {
		t.Errorf("fs.Args() = %v, want empty", got)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestParseFlags -v`
Expected: `TestParseFlagsAcceptsFlagsAfterPositionals` FAILS with `--round after a positional must still parse, got 0` and `--tab after a positional must still parse`. `TestParseFlagsStillRejectsUnknownFlags` FAILS because the unknown flag is swallowed and no error is returned. The other three PASS already — they are regression fences for behaviour that must survive the change.

- [ ] **Step 5: Write the implementation**

In `cmd/relay/main.go`, replace `parseFlags` (lines 114-122) in full. Keep the existing doc comment's first line and extend it:

```go
// parseFlags parses one subcommand's flags. It turns `-h` into a clean exit:
// the flag package has already printed usage, so the caller just returns.
//
// It parses iteratively rather than once, because flag.Parse stops at the first
// non-flag argument -- which meant `relay fork webshop --round 2` silently
// dropped --round, the exact form the README documents (#48). Each pass takes
// one leftover word as a positional and re-parses the remainder, so flags are
// found wherever they appear. Letting Parse do the work is what keeps this
// correct without knowing any flag's arity: Parse has already consumed a
// flag's value before the leftovers are looked at, so `--round 2` never leaves
// a stray "2" behind.
//
// There is no `--` passthrough anywhere in this CLI, so nothing here needs to
// stop early and treat a tail as literal.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var positional []string

	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return errHelpShown
			}
			return err
		}

		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	// Re-parse the collected positionals so fs.Args() reports them, leaving
	// every caller's `fs.Args()` working exactly as before. Parse stops at the
	// first non-flag argument and every element here is one, so this consumes
	// nothing and simply reinstates the list. Flag values already set by the
	// passes above survive: Parse does not reset them.
	return fs.Parse(positional)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestParseFlags -v`
Expected: PASS, all five.

- [ ] **Step 7: Verify against the real binary**

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay fork webshop --round 2 --new-name webshop-alt`
Expected: `relay: webshop: binding not found` — it now reaches the real code path. Before the fix this printed `usage: relay fork <source> ...` claiming it needed the source binding name, which had in fact been given.

Run: `cd /home/fuad/projects/relay && go run ./cmd/relay unbind webshop --archive`
Expected: `relay: webshop: binding not found`, not the usage refusal.

Report the exact output of both, even if it differs from the above.

- [ ] **Step 8: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go build ./... && go test ./...`
Expected: PASS. Pay attention to `TestForkValidation` — if it now fails, that is expected and Task 2 fixes it. Report whether it failed and with what message.

- [ ] **Step 9: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main.go cmd/relay/main_test.go
git commit -m "fix(relay): parse flags that follow a positional name (#48)

flag.Parse stops at the first non-flag argument, so every command taking a
binding name positionally silently dropped any flag written after it --
including \`relay fork webshop --round 2 --new-name x\`, the form the README
documents. The error was misleading too: it claimed the source binding name
was missing when it had been given.

parseFlags now parses iteratively, taking one leftover word per pass and
re-parsing the rest, then reinstates the positionals so every fs.Args()
caller is untouched. Letting Parse consume flag values keeps this correct
without knowing any flag's arity.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

### Task 2: Make `TestForkValidation` test what it claims

**Files:**
- Modify: `cmd/relay/main_test.go:195-213` (`TestForkValidation`)

**Interfaces:**
- Consumes: the behaviour shipped in Task 1; `run(args []string) error`.
- Produces: nothing new.

`TestForkValidation` asserts on two different validation errors, but before Task 1 both of its cases produced the *same* usage string — which contains both `--round` and `--new-name` as substrings. Both assertions passed while never reaching the `*round < 1` or `*newName == ""` branches. Those validations have had no real coverage.

- [ ] **Step 1: Confirm what the cases produce now**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestForkValidation -v`
Expected: report the outcome. After Task 1 the two flag cases reach the real validation branches, so the assertions may now pass for the right reason — but they are still satisfiable by the usage string, which is what Step 2 fixes.

- [ ] **Step 2: Replace the test**

In `cmd/relay/main_test.go`, replace `TestForkValidation` in full:

```go
func TestForkValidation(t *testing.T) {
	// Each assertion names the specific validation being exercised. The usage
	// line lists every flag, so asserting on a bare "--round" would pass on the
	// usage string alone -- which is exactly how these cases passed while never
	// reaching the validation they claim to cover (#48).

	// Missing source
	err := run([]string{"fork", "--round", "1", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "needs the source binding name") {
		t.Fatalf("expected the refuse-to-guess error, got %v", err)
	}

	// Missing --round
	err = run([]string{"fork", "src", "--new-name", "fork-1"})
	if err == nil || !strings.Contains(err.Error(), "relay fork requires --round N") {
		t.Fatalf("expected the --round validation, got %v", err)
	}

	// Missing --new-name
	err = run([]string{"fork", "src", "--round", "1"})
	if err == nil || !strings.Contains(err.Error(), "relay fork requires --new-name NAME") {
		t.Fatalf("expected the --new-name validation, got %v", err)
	}
}
```

- [ ] **Step 3: Run the test**

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestForkValidation -v`
Expected: PASS. Each case now reaches and asserts on its own distinct validation error.

- [ ] **Step 4: Prove it is a real fence**

Temporarily revert `parseFlags` to a plain `return fs.Parse(args)` body (keeping the `flag.ErrHelp` handling), re-run the test, and confirm it FAILS on the `--round` and `--new-name` cases — they should collapse back to the usage string. Then restore Task 1's implementation and confirm it passes again. Verify the restore with `git diff cmd/relay/main.go`, which must be empty. Do not commit the reverted state.

Run: `cd /home/fuad/projects/relay && go test ./cmd/relay/ -run TestForkValidation -v`
Expected: FAIL while reverted; PASS once restored.

- [ ] **Step 5: Run the whole suite**

Run: `cd /home/fuad/projects/relay && go clean -testcache && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/fuad/projects/relay
git add cmd/relay/main_test.go
git commit -m "test(relay): fork validation must not pass on the usage string

Both flag cases asserted on substrings the usage line already contains, so
they passed without ever reaching the --round or --new-name validation.
Each case now asserts on its own specific error.

Closes #48

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01YAb2u3n17kC9xy54zsgWFY"
```

---

## Notes for the reviewer

- **Why iterative parsing instead of hoisting flags to the front?** Hoisting requires knowing whether each flag takes a value — `--tab` does not, `--round` does — which means inspecting each `flag.Value` for `IsBoolFlag()`. Letting `Parse` run repeatedly gets that for free, because `Parse` has already consumed a flag's value before the leftovers are inspected.
- **Why the trailing `fs.Parse(positional)`?** So `fs.Args()` keeps reporting the positionals and none of the five consumers (`fork`, `unbind`, `answer`, `done`, `log`) need touching. Every element is a non-flag word, so `Parse` stops immediately and simply reinstates the list.
- **Unknown flags after a positional now error instead of being swallowed.** That is a deliberate behaviour change and strictly better — it was previously possible to typo a flag and have it silently ignored.
