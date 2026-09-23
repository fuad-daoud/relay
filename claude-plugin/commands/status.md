---
description: Show relay's bindings for this planner
argument-hint: "[--name <binding>] [--all]"
allowed-tools: Bash(relay:*)
---

```!
relay status $ARGUMENTS
```

The block above is relay's live state for this planner, already fetched.
Say only what changed or what needs a decision. Do not call the relay
status tool or run `relay status` again -- you already have the answer.
