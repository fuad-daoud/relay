---
description: Show what a builder changed in a round
argument-hint: "[<binding>] [--round N]"
allowed-tools: Bash(relevo:*)
---

```!
relevo diff $ARGUMENTS
```

The block above is the round's diff, already fetched. Summarise it
against what the round was for. Do not run `relevo diff` again -- you
already have it.
