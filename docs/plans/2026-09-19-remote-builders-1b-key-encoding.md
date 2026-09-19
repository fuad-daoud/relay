# Remote builders, plan 1b: drop crypto/x509 from internal/remote (#100, PR #206)

Round 2 on the same branch. Plan 1 (`docs/plans/2026-09-19-remote-builders-1-remote-package.md`,
in this tree) chose PKCS#8 for the private key; that pulls `crypto/x509`
into `internal/remote`, and on macOS with Go 1.22 the race-test binary of a
package that imports `crypto/x509` without `net` is linked internally
without an `LC_UUID` load command, which the current macOS dyld refuses
(`dyld: missing LC_UUID load command`, CI job "check (macos-latest, go
1.22)" on PR #206, twice). Nothing reads the key but relay, so PKCS#8 buys
nothing. This round replaces the encoding and removes the import.

**Halt rule for the builder.** If any step below is impossible as written,
contradicts the code you find, or would require bending a test to pass,
stop at that step, write the report saying which step and why, create the
done marker, and do nothing else.

**Scope guard.** Touch only `internal/remote/key.go`,
`internal/remote/key_test.go`, and copy this plan to
`docs/plans/2026-09-19-remote-builders-1b-key-encoding.md`. No other file.
No CLI, no dependency. Foreground only, no sub-agents.

**Git in tests.** Not needed here; the key tests run no git.

## 1. System overview

`MarshalPrivate`/`ParsePrivate` change their byte format; every other
exported name in `internal/remote` keeps its signature and behaviour. The
new format is a PEM block of type `RELAY ED25519 PRIVATE KEY` whose bytes
are the 64-byte `ed25519.PrivateKey` (seed || public key) verbatim.
`ParsePrivate` rebuilds `Keypair.Public` from the private key
(`priv.Public().(ed25519.PublicKey)`) and refuses anything else.

## 2. File structure

```
internal/remote/key.go        MarshalPrivate, ParsePrivate re-encoded; crypto/x509 import gone
internal/remote/key_test.go   TestParsePrivateRejectsRSA replaced; new rejection cases
docs/plans/2026-09-19-remote-builders-1b-key-encoding.md
```

## 3. Data structures

```
const pemTypePrivate = "RELAY ED25519 PRIVATE KEY"
```

Wire format of `MarshalPrivate(k)`: `pem.EncodeToMemory(&pem.Block{Type:
pemTypePrivate, Bytes: []byte(k.Private)})`. No headers. Exactly 64 bytes
inside.

## 4. Interfaces

```
MarshalPrivate(k Keypair) ([]byte, error)
    Pre:  len(k.Private) == ed25519.PrivateKeySize, else ErrKeyFormat.
    Post: PEM bytes as §3.

ParsePrivate(data []byte) (Keypair, error)
    1. pem.Decode(data); nil block -> ErrKeyFormat
    2. block.Type != pemTypePrivate -> ErrKeyType
       (a PKCS#8 "PRIVATE KEY" block from a key written by the previous
       round therefore fails as ErrKeyType; there are no such keys in the
       wild -- the format has never shipped -- so no migration)
    3. len(block.Bytes) != ed25519.PrivateKeySize -> ErrKeyFormat
    4. priv := ed25519.PrivateKey(block.Bytes)
       pub := priv.Public().(ed25519.PublicKey)
       return Keypair{Private: priv, Public: pub}, nil
```

`ErrKeyFormat` and `ErrKeyType` keep their identities and messages.

## 5. Pseudocode

Nothing beyond §4.

## 6. Error handling

Unchanged sentinels; no new ones.

## 7. Ordered implementation steps

**Step 1 -- plan file.** Copy this plan to
`docs/plans/2026-09-19-remote-builders-1b-key-encoding.md`. No commit yet.

**Step 2 -- tests first.** In `key_test.go`: delete
`TestParsePrivateRejectsRSA` and its `crypto/rsa`/`crypto/x509` imports.
Add `TestParsePrivateRejectsWrongType` (a PEM block of type `PRIVATE KEY`
with 64 bytes -> `ErrKeyType`), `TestParsePrivateRejectsWrongLength` (a
block of the right type with 32 bytes -> `ErrKeyFormat`),
`TestParsePrivateRejectsNotPEM` (`[]byte("garbage")` -> `ErrKeyFormat`),
`TestMarshalPrivateType` (the output decodes to a block whose Type is
`RELAY ED25519 PRIVATE KEY` and whose Bytes are exactly `k.Private`).
Keep `TestKeyRoundTrip` as it is. Run `go test ./internal/remote -run
'TestParsePrivate|TestMarshalPrivate'`; the new tests must fail (wrong
error or wrong PEM type) before step 3.

**Step 3 -- re-encode.** Implement §4 in `key.go`; remove the
`crypto/x509` import. Verify with `go list -deps ./internal/remote | grep
-c x509` printing `0`. Run the package tests green.

**Step 4 -- check and commit.** `make check` green. One commit:
`remote: raw PEM private key, no crypto/x509 (#100)` including the plan
file. Report: the `go list -deps` line, and the full `key.go` import block.
Create the done marker.

## Verification the planner runs

`make check`; `go list -deps ./internal/remote | grep x509` empty; diff
touches only the three files above; CI job `check (macos-latest, go 1.22)`
on PR #206 passes after push.
