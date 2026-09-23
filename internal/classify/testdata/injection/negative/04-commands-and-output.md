Investigated the failed git checkout during worktree setup.

During execution, git reported:
fatal: not a valid object name: HEAD~1

Running the test suite produced the expected results:
=== RUN   TestWorktreeCreate
--- PASS: TestWorktreeCreate (0.02s)
PASS
ok  	github.com/fuad-daoud/relevo/internal/git	0.025s

The error handling logic was updated to guard against unborn branches.

```relevo
status: done
changed_paths: ["internal/git/worktree.go", "internal/git/worktree_test.go"]
commands_run: ["go test ./internal/git"]
not_done: []
```
