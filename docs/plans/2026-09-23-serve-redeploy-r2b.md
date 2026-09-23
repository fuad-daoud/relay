# Plan: invisible serve redeploys, round 2b: drop the author warning (#373)

A planner decision on your round-2 deviation 1. Your reasoning was sound. The **spec** was wrong: servers from #335 (v0.8+) already honour `CreateBindingRequest.Author` without advertising anything, because `FeatureAuthor` is new in #373 round 1. A server lacking the token is therefore usually one that honours the author, so the warning would be false against every server deployed today.

**Stop rather than improvise.** If a step is impossible as written, halt and report.

## Steps

1. In `internal/relay/remote.go`, `addRemote`, restore the pre-round-2 behaviour exactly:
   - no `WhoAmI` call when `opts.Tier == ""`;
   - `WhoAmI` and the `FeatureTier` check only when a tier is requested, as before;
   - remove `warnAuthorIgnored` and `authorWarned`.
2. In `internal/relay/remote_test.go`:
   - restore `TestAddRemoteNoTierSkipsProbe` byte-for-byte from `HEAD~1` (the pre-round-2 commit), under its original name;
   - delete `TestAddRemoteNoTierSendsNoTier` and `TestAddRemoteWarnsOnceWhenServerIgnoresAuthor`.
3. **Keep** `remote.FeatureAuthor` and the server advertising it (round 1). It costs nothing and lets a future client rely on it.
4. In `docs/specs/2026-09-23-serve-redeploy-design.md`, §3 and §4.5, replace the Author warning text with one sentence: *the client does not warn: servers since #335 honour the author without advertising `author`, so a missing token doesn't mean it is ignored; `author` is advertised from #373 on for future use.*
5. `make check` and `make e2e`. Commit ending `(#373)`.

Declared scope: `internal/relay/remote.go`, `internal/relay/remote_test.go` and the spec file.
