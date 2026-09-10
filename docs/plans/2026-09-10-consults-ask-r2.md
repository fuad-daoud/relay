# Consults Task 4, round 2: match the CLI house convention

Round 1 is correct and committed (`0c385bd`). I verified it independently:
`make check` passes on zen, the scope matches, and I re-ran all three Step 5b
mutations myself — each killed its named test with the exact expected message.
Nothing about `Ask` or its tests needs to change.

This round fixes one thing, and it is a defect in **the plan you were given**.

## What is wrong

The plan specified the handler as `runAsk(ctx context.Context, rt relay.Runtime,
args []string)`. That signature does not exist anywhere else in `main.go`. All
fifteen other handlers are `cmdX(args []string) error` and build their own
runtime and context internally, which keeps every arm of the switch a
one-liner:

```go
	case "send":
		return cmdSend(args[1:])
	case "pull":
		return cmdPull(args[1:])
```

Following the plan literally — which was the right thing to do — made `ask` the
only arm that inlines five lines of runtime construction into the switch. You
flagged it rather than silently renaming, which is exactly right; this round
makes the change deliberately.

## Task

- [ ] **Step 1: Change the handler to the house shape**

In `cmd/relay/main.go`, change the signature at `:623`:

```go
func cmdAsk(args []string) error {
```

Delete `ctx` and `rt` from the parameter list. Immediately after the flag
parsing and the two `*role == ""` / `*file == ""` guards — mirroring `cmdSend`
(`:590-605`), read it first — build the runtime:

```go
	rt, err := newRuntime()
	if err != nil {
		return err
	}
```

Then use `context.Background()` at the `relay.Ask` call site, as `cmdSend` does
at its `relay.Send` call. Everything else in the function body stays as it is.

- [ ] **Step 2: Make the switch arm a one-liner**

Replace the whole `case "ask":` block:

```go
	case "ask":
		rt, err := newRuntime()
		if err != nil {
			return err
		}
		return runAsk(context.Background(), rt, args[1:])
```

with:

```go
	case "ask":
		return cmdAsk(args[1:])
```

- [ ] **Step 3: Verify**

```bash
dev run make check
```

zen is back up, so `dev run` should work; if it reports no desktop again, fall
back to the bare command and say so.

Then confirm the command still works end to end:

```bash
go build -o /tmp/relay ./cmd/relay && /tmp/relay help | grep -E '^  (ask|pull)'
/tmp/relay ask --role reviewer 2>&1 | head -2
```

The last one must fail with the missing-`--file` message, not a panic and not a
nil-runtime error — that is what proves the runtime is built on the path the
guards let through.

- [ ] **Step 4: Commit**

```bash
git add cmd/relay/main.go
git commit -m "refactor(cli): give ask the same handler shape as every other command

Every other handler in main.go is cmdX(args []string) and builds its own
runtime, which keeps each arm of the switch a one-liner. ask was specified with
a runAsk(ctx, rt, args) signature that exists nowhere else, making it the only
arm carrying five lines of construction."
```

## Constraints

- Touch only `cmd/relay/main.go`.
- Do not change `internal/relay/ask.go`, its tests, or the fake. They are
  verified.
- If a step is impossible as written, stop and say so in your report.
