Encountered a design contradiction during step 5 of the execution plan.

The plan instructs: "If any step below is impossible as written, contradicts the code you find, or would require bending a test to pass, stop at that step, write the report saying which step and why, create the done marker, and do nothing else."

The specified type definition in internal/api conflicts with the exported interface in internal/core. Halting per instructions rather than improvising.

No further changes have been applied to the working tree.

```relevo
status: halted
halted_at: "step 5"
changed_paths: []
commands_run: ["go test ./..."]
not_done: ["steps 6-10"]
```
