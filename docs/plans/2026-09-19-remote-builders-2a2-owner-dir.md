# Remote builders, plan 2a.2: a filesystem-safe owner directory (#100)

Round 2 on the same branch. Plan 2a (`docs/plans/2026-09-19-remote-builders-2a-serve-core.md`,
in this tree) wrote `bindings/<owner-id>/` and `repos/<owner-id>/` with the
raw `remote.ClientID` (`SHA256:<base64>`), which contains `/` and `+`. The
round-1 report (deviation 2) found that and worked around it by
enumerating `clients.List()` in `Tick` and `loadBinding` instead of the
directory. This round fixes the layout: the owner directory is the
digest as 64 lower-case hex characters, flat and enumerable, and `Tick`
walks the directory again as §4.10 said.

**Halt rule for the builder.** If any step below is impossible as written
or contradicts the code you find, stop, write the report saying which step
and why, create the done marker, and do nothing else.

**Scope guard.** Touch only `internal/remote/key.go`,
`internal/remote/key_test.go`, `internal/serve/serve.go`,
`internal/serve/daemon.go`, `internal/serve/bindings.go`,
`internal/serve/rounds.go`, `internal/serve/serve_test.go`, and copy this
plan to `docs/plans/2026-09-19-remote-builders-2a2-owner-dir.md`. No
other file. Foreground only, no sub-agents.

## 1. Interfaces

`internal/remote/key.go`:

```
// Dir is the id as a path component: the 32-byte digest in lower-case hex
// (64 characters), with no prefix. Safe for any filesystem; one-to-one with
// the id. ("", false) for a string that is not a well-formed ClientID.
func (id ClientID) Dir() (string, bool)

// IDFromDir is the inverse: 64 hex characters -> ClientID; ("", false)
// otherwise.
func IDFromDir(dir string) (ClientID, bool)
```

Well-formed: prefix `SHA256:`, then base64.RawStdEncoding of exactly 32
bytes.

`internal/serve`:

```
func (s *Server) ownerRoot(owner remote.ClientID) (string, error)
    dir, ok := owner.Dir(); !ok -> error "malformed client id"
    return filepath.Join(s.cfg.Root, "bindings", dir)

func (s *Server) repoRoot(owner remote.ClientID) (string, error)
    same shape, under "repos"

func (s *Server) runtime(owner remote.ClientID) (relay.Runtime, error)   // now returns the ownerRoot error
func (s *Server) runtimeAt(root string) relay.Runtime                    // what Tick uses: Store at root, everything else as before
```

Every caller of the old `runtime(owner)` handles the error as `500
{invalid, "malformed client id"}` -- it cannot happen for an id that
passed `authenticate` (the id came from `clients.json`), so this is a
guard, not a path.

## 2. Behaviour

- `bindings.go` create: bare repo at `filepath.Join(repoRoot(owner),
  req.RepoID+".git")`.
- `loadBinding` (or whatever the round-1 code calls the shared
  load-and-authorize helper): use `runtime(caller)` directly; delete the
  `clients.List()` iteration.
- `daemon.go` `Tick`: `os.ReadDir(filepath.Join(cfg.Root, "bindings"))`;
  for every entry that is a directory and for which `remote.IDFromDir`
  succeeds, `runtimeAt(path)` and `relay.NewDaemon(rt, interval).Tick`.
  Entries that are not 64-hex are skipped with one `slog.Warn` each
  (`unexpected entry in bindings dir`). Delete the `clients.List()`
  iteration. A missing `bindings` dir is a no-op (`TestTickSkipsMissingBindingsDir`
  stays green).

## 3. Steps

**Step 1.** Copy the plan. Add `Dir`/`IDFromDir` with tests
`TestClientIDDirRoundTrip` (`IDOf(pub).Dir()` is 64 hex, `IDFromDir` gives
the id back), `TestClientIDDirRejects` (`"x"`, `"SHA256:"`, `"SHA256:!!"`,
a 31-byte digest -> `ok == false`; `IDFromDir("zz")`, 63 chars, upper-case
hex -> `ok == false`). Run them red, then green.

**Step 2.** §1 and §2 in `internal/serve`. Update `serve_test.go`: every
place that builds a path with `string(id)` uses `id.Dir()`. Add
`TestOwnerDirIsFlatHex`: after A creates a binding, `os.ReadDir(cfg.Root/bindings)`
has exactly one entry, its name is 64 hex characters, and it is a
directory containing `<name>/binding.json`. `TestTickWalksEveryOwner` must
still pass with the directory walk (and it now proves the walk, not the
client list: add a third enrolled client with no bindings and assert its
runner is never touched).

**Step 3.** `make check` green. One commit: `serve: owner directory is
the digest in hex (#100)` including the plan file. Report: the `ls` of a
test's `bindings/` dir from `TestOwnerDirIsFlatHex`'s log. Done marker.

## Verification the planner runs

Diff within the scope list; `grep -rn "clients.List()" internal/serve/*.go`
finds only `clients.go`'s own use and the `List` handler if any; `make
check` green.
