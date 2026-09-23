# Plan: restart hardening, round 4: two stale comments (#370)

Comments only. **No code, test or behaviour change.** If anything beyond comment text seems to need changing, halt and report.

## Steps

1. **`scripts/relay-service-template_test.sh`**, header comment (lines 4–7). It says headless builders are children of the daemon and share its cgroup. Reword it to match `dist/relay.service`'s updated comment (round 3):
   - builders, gates and consults run in their own `relay-*.scope` units and survive a restart of this unit (#370);
   - they share this unit's cgroup only when scopes are unavailable, so the unit still must not cap memory, and an OOM-killed builder must not take the daemon down (`OOMPolicy=continue`).

   Mention that the script also asserts `StartLimitIntervalSec=0` (#370).
2. **`internal/relay/headless.go`**, the comment above `lost := codeText == "unknown" && lostToRestart(...)`. Fold resume in: a lost local builder first resumes its own harness session (claude `--resume`, agy `--conversation`, opencode `--session --fork`), and falls back to a fresh relaunch (codex, no announced session, or a failed resume spawn). Both carry the interrupted note, stay uncounted and keep `RoundStartedAt`.
3. **Gate:** `gofmt -l`, `sh scripts/relay-service-template_test.sh`, `shellcheck scripts/relay-service-template_test.sh` if it is installed, and `go build ./...`. `git diff` must show only comment lines. Commit ending `(#370)`.

Declared scope: those two files, comment lines only.
