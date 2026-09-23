# Plan: #292 round 5: a relay-era client key still parses

Spec: `docs/specs/2026-09-23-rename-relevo-design.md` §1 (legacy reads). Found by
the planner's rehearsal of `relevo migrate` on a copy of real state. After the
migration, **every** relevo verb failed with:

```
relevo: parse client key: invalid key type; ed25519 required
```

The cause: round 1's sweep renamed the PEM block type in
`internal/remote/key.go:21` (`pemTypePrivate`) from `"RELAY ED25519 PRIVATE KEY"`
to `"RELEVO ED25519 PRIVATE KEY"`. Every existing `client.key` (written by
`relay client init`) still says `-----BEGIN RELAY ED25519 PRIVATE KEY-----`, and
`ParsePrivate` (~line 102-110) now rejects it. The key's bytes are unchanged, and
enrolment is by public key, so accepting the old block type is all that's needed.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 1. Change

1. **`internal/legacy/legacy.go`:** add `const KeyPEMType = "RELAY ED25519 PRIVATE
   KEY"`, with a doc comment: the PEM block type of a client key written before
   the rename; `remote.ParsePrivate` still accepts it; new keys are written as
   `RELEVO ED25519 PRIVATE KEY`.
2. **`internal/remote/key.go` `ParsePrivate`:** accept `block.Type ==
   pemTypePrivate || block.Type == legacy.KeyPEMType`. `MarshalPrivate` is
   unchanged, so new keys are written with the new type.
   - Update the doc comment at ~100-101 to say the pre-rename type is accepted.
   - `internal/remote` importing `internal/legacy` is fine: it's a stdlib-only
     leaf.
3. **The key file stays as it is on disk.** Don't rewrite it in `migrate` or
   anywhere else: reading is enough, and it keeps `migrate` from touching secret
   material.

## 2. Tests (`internal/remote/key_test.go`, find the existing ParsePrivate tests)

- `TestParsePrivateAcceptsLegacyType`: generate a keypair, `MarshalPrivate` it,
  replace `RELEVO ED25519 PRIVATE KEY` with `legacy.KeyPEMType` in the bytes
  (both BEGIN and END lines), then `ParsePrivate`. It must succeed with the same
  public key.
- Keep, or add, a case where a PEM of another type (e.g. `"EC PRIVATE KEY"`) still
  returns `ErrKeyType`.
- Mutation: drop the legacy clause and the new test must fail.

## 8. Working efficiently

- **Focused:** `go test ./internal/remote/ ./internal/legacy/`, then
  `sh scripts/check-name.sh` (round 4's guard; `internal/legacy` is allowlisted).
- **Once at the end:** `make check`.

## 9. Steps

1. The §1 change.
2. The §2 tests and mutation.
3. `make check` must pass. One commit:
   `fix(remote): a client key written by relay still parses (#292)`. Don't push.

**Declared scope:** `internal/legacy/legacy.go`, `internal/remote/key.go`,
`internal/remote/key_test.go`.

**Report:**
- the mutation;
- `git diff --stat HEAD~1`;
- the result of `make check`.
