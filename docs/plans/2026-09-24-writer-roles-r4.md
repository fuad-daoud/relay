# Plan: #382 round 4: doctor's binding-role row skips remote bindings

## 1. The defect

`doctor.BindingRoleChecks` (`internal/doctor/roles.go`, added in round 2) reports
a FAIL for every binding that is not DONE whose role the **local** registry does
not define.

Round 3 made a remote binding (`relevo add --server S --role ui-builder`) resolve
its role against the **server's** own `roles.json`. The client's local copy of
that binding records `Role: "ui-builder"` so that `relevo status` shows it, but
the client need not define `ui-builder` at all (spec §5.3: the client's roles
never travel). So `relevo doctor` on the client falsely reports:

    binding x runs role "ui-builder", which roles.json no longer defines

`relevo send` already has the right rule: its `--builder` path skips local role
resolution when `b.Builder.Remote()` (`internal/relevo/send.go` ~152). Doctor
must follow the same rule.

**Stop rather than improvise.** If a step is impossible as written or contradicts
the code, halt and report.

## 2. Change

1. **`internal/doctor/roles.go`, `BindingRoleChecks`.** In the loop, directly
   after the `StateDone` skip, add `if b.Builder.Remote() { continue }`. Extend
   the doc comment: a remote binding's role is resolved by its server against
   the server's own `roles.json`, so the local registry cannot judge it.
2. **`internal/doctor/roles_test.go`, `TestBindingRoleChecks`.** Add a binding
   `e` with `Role: "gone"`, which `known` says is unknown, and a remote builder
   endpoint. Set `Builder: store.Endpoint{Mode: store.ModeRemote}`, which is what `Endpoint.Remote()` reads
   (`internal/store/types.go:163`). It must produce **no**
   row. The expected result stays exactly one row, for `c`. This extends the
   existing test's fixture. Its assertion that only `c` is reported is
   unchanged.

**Mutation:** drop the new `continue`. `TestBindingRoleChecks` fails, with two
rows. Revert.

## 3. Working efficiently

- Read `internal/doctor/roles.go`, `internal/doctor/roles_test.go`, and
  `internal/store/types.go` (the `Endpoint.Remote` method) in one parallel step.
- **Focused:** `go test -count=1 ./internal/doctor/`.
- **Once at the end:** `make check` and `sh scripts/check-name.sh`.

## 4. Steps

1. §2.1 and §2.2. Verify: `go test -count=1 ./internal/doctor/`.
2. The mutation.
3. `make check` passes. Then make one commit:

       fix(doctor): a remote binding's role is its server's to judge (#382)

   Do not push. The report names the mutation and the test that failed for it.
