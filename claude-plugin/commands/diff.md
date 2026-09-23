---
description: Show what a builder changed in a round
argument-hint: "[<binding>] [--round N]"
allowed-tools: Bash(relay:*)
---

```!
relay diff $ARGUMENTS
```

The block above is the round's diff, already fetched. Summarise it
against what the round was for. Do not run `relay diff` again -- you
already have it.
