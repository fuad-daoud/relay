# `relay update`, part 1 of 2: provenance and the staleness check (#293)

This plan stands alone: everything you need is in this file and in the tree.
If a step is impossible as written or contradicts the code, **halt and
report** -- do not improvise around it.

This part is **read-only**: it teaches relay how it was installed and
whether a newer release exists, and surfaces that. It updates nothing.
Part 2 (`relay update` itself, which rewrites the binary and restarts the
daemon) builds on §4.1's provenance and is not in scope here. Do not add
an `update` verb, and do not write anything into an install directory.

**No test in this plan may touch the network.** The release fetch is
behind an interface with a fake in every test. A test that reaches
`api.github.com` is a failed step even if it passes.

## 1. System overview

`buildVersion()` (`cmd/relay/main.go:146`) already answers "what am I"
honestly: the `version` ldflags stamp, else `debug.ReadBuildInfo().Main.Version`,
else `(devel)`. The stamp is a `git describe`, so a local `make` build reads
`v0.6.0-2-gddf3d4f` and a release build reads `v0.7.0`. Nothing answers
"what is current", and nothing knows which of the four install variants
produced the running binary.

Four variants, from #293:

| variant | how it got here | how it updates (part 2) |
|---|---|---|
| plugin, release | `scripts/plugin-fetch.sh` downloads the tag in `herdr-plugin.toml` | re-run the fetch |
| plugin, source | `scripts/plugin-build.sh` | re-run the build |
| `go install` | module proxy | `go install` |
| local `make` | this worktree | refuse, and say why |

The design decision this plan makes, because #293 left it open: **no
command ever blocks on the network.** The daemon refreshes a cached
answer at most once a day from its existing tick; every other surface
reads that cache and never fetches. An unrefreshed cache, an offline
machine and a `(devel)` build all read the same way -- "not checked" --
and say nothing. That is what keeps `status` and the statusline quiet.

## 2. Files

```
internal/release/provenance.go     (new)  Kind; Inputs; Detect (pure)
internal/release/provenance_test.go (new) Detect table
internal/release/version.go        (new)  Version; ParseVersion; Newer (pure semver compare)
internal/release/version_test.go   (new)
internal/release/cache.go          (new)  Cache; Load; Save; State; Stale
internal/release/cache_test.go     (new)  fake clock; malformed file; missing file
internal/release/fetch.go          (new)  Fetcher interface; HTTPFetcher; ErrOffline
internal/release/fetch_test.go     (new)  httptest server only -- never api.github.com
internal/relay/daemon.go                  Tick refreshes the cache when Stale (see §4.4)
internal/relay/daemon_test.go             refresh fires once past TTL, not before; fetch error is swallowed
internal/doctor/env.go                    Env gains ReleaseState (cache read only, no network)
internal/doctor/doctor.go                 "release" check (see §4.5)
internal/doctor/doctor_test.go            fakeEnv gains ReleaseState; four cases
cmd/relay/doctor.go                       wires the real Env method
cmd/relay/main.go                         statusNotice line (see §4.6)
cmd/relay/main_test.go                    statusNotice table (pure; no subcommand executed)
docs/plans/2026-09-22-relay-update-staleness.md   copy of this plan (last step)
```

Nothing else. In particular **do not touch** `internal/proc/`, which
another round owns right now, or `scripts/check-plugin-version.sh`.

## 3. Probes (first; results verbatim in the report)

```
./relay version
go run ./cmd/relay version
sed -n 's/^version = "\(.*\)"$/\1/p' herdr-plugin.toml
go list -m -f '{{.Version}}' github.com/fuad-daoud/relay 2>&1 | head -2
```

Report all four outputs verbatim. They pin what `buildVersion()` actually
returns in this worktree, which §4.1's table is written against; if the
`./relay version` string does not have the `vX.Y.Z-N-g<sha>` shape, halt
and report rather than adjusting the table.

## 4. Contracts

### 4.1 Provenance (`internal/release/provenance.go`)

```go
// Kind is how the running binary was installed.
type Kind string

const (
    KindPluginRelease Kind = "plugin-release" // scripts/plugin-fetch.sh
    KindPluginSource  Kind = "plugin-source"  // scripts/plugin-build.sh
    KindGoInstall     Kind = "go-install"
    KindLocalBuild    Kind = "local-build"
    KindUnknown       Kind = "unknown"
)

// Inputs is every fact Detect reads. The caller gathers them; Detect
// touches no disk and no environment, so the table in the test is the
// whole truth about this decision.
type Inputs struct {
    // Version is buildVersion(): a git describe, a module version, or "(devel)".
    Version string
    // ExeDir is filepath.Dir of the resolved executable path.
    ExeDir string
    // ManifestVersion is the version in ExeDir/herdr-plugin.toml, "" when
    // that file is absent -- the marker of a plugin install, since both
    // plugin variants leave ./relay beside the manifest.
    ManifestVersion string
    // FromModule is true when debug.ReadBuildInfo gave the version, i.e.
    // there was no ldflags stamp.
    FromModule bool
}

// Detect classifies the install. Pure.
func Detect(in Inputs) Kind
```

Rules, in order -- this *is* the test table:

| condition | Kind |
|---|---|
| `ManifestVersion != ""` and `Version` is an exact `v<ManifestVersion>` | `KindPluginRelease` |
| `ManifestVersion != ""` (any other version string) | `KindPluginSource` |
| `FromModule` | `KindGoInstall` |
| `Version == "(devel)"` | `KindLocalBuild` |
| `Version` has a `-<N>-g<sha>` or `-dirty` suffix | `KindLocalBuild` |
| otherwise | `KindUnknown` |

`KindUnknown` is not an error: it means "do not claim anything", and §4.5
reports it as SevOK/not-checked.

### 4.2 Versions (`internal/release/version.go`)

```go
// Version is a parsed vMAJOR.MINOR.PATCH. Pre-release and build metadata
// are kept verbatim in Suffix and make a version older than the same
// numbers with no suffix, which is all the ordering relay needs.
type Version struct {
    Major, Minor, Patch int
    Suffix              string // "" for a clean release; "-2-gddf3d4f", "-dirty", "-rc1"
}

// ParseVersion accepts "v1.2.3", "1.2.3" and "v1.2.3-2-gabc1234".
// ok is false for "(devel)" and anything unparseable.
func ParseVersion(s string) (v Version, ok bool)

// Newer reports whether latest is strictly newer than running. It is
// false whenever either side is unparseable -- relay never claims
// staleness it cannot prove.
func Newer(running, latest Version) bool
```

A describe-suffixed running version (`v0.6.0-2-g...`) is **older** than
`v0.6.0`? No: it is *newer* than v0.6.0 and older than v0.6.1. Encode
exactly that -- compare the numbers first; on equal numbers, a suffix on
`running` only makes it newer, never older. Put both directions in the
test; this is the condition to mutate in §5.

### 4.3 Cache (`internal/release/cache.go`)

Lives at `<state>/release-check.json`, where `<state>` is the root
`store.DefaultRoot()` already returns. Compose it from that root; do not
build an XDG path by hand (#42).

```go
type Cache struct {
    Latest    string    `json:"latest"`     // "v0.7.0"
    CheckedAt time.Time `json:"checked_at"`
    Source    string    `json:"source"`     // the URL it came from
}

// Load reads the cache. A missing file is (Cache{}, false, nil) -- not an
// error. A malformed file is the same: a corrupt cache must never fail a
// caller, it must only fail to inform one.
func Load(root string) (Cache, bool, error)

// Save writes it via temp-and-rename.
func Save(root string, c Cache) error

// Stale reports whether a refresh is due: no cache, or CheckedAt older
// than TTL.
func Stale(c Cache, ok bool, now time.Time, ttl time.Duration) bool

const TTL = 24 * time.Hour
```

### 4.4 Fetch and refresh (`internal/release/fetch.go`, `internal/relay/daemon.go`)

```go
// Fetcher returns the latest published release tag.
type Fetcher interface {
    Latest(ctx context.Context) (string, error)
}

// HTTPFetcher reads the GitHub releases API. The endpoint is overridable
// with RELAY_RELEASE_API, matching plugin-fetch.sh's RELAY_RELEASE_BASE_URL,
// so tests and air-gapped installs can point it elsewhere.
func NewHTTPFetcher(endpoint string, timeout time.Duration) Fetcher
```

Timeout 5s, no auth header, no retry. Any failure returns an error the
caller swallows.

In `Daemon.Tick` (`internal/relay/daemon.go:287`), after the existing work
and never before it:

- read the cache; if `!Stale`, do nothing (this is the common path -- a
  2s tick must not stat anything expensive, so the cache read is the
  whole cost)
- if stale, fetch; on success `Save`; on error, **save nothing** and log
  at debug, so an offline machine retries next tick without ever writing
  a wrong answer

The daemon must keep ticking when the fetch fails. Wire the `Fetcher`
through `Runtime` so the daemon test injects a fake; do not construct an
HTTP client inside `Tick`.

### 4.5 Doctor (`internal/doctor/`)

`Env` gains one method -- **cache read only, no network**:

```go
// ReleaseState returns the running version, the cached latest (ok false
// when there is no usable cache) and the install kind.
ReleaseState() (running string, latest string, ok bool, kind release.Kind)
```

The `release` check is **unconditional** -- build it in `Run` itself,
alongside the herdr probe at `internal/doctor/doctor.go:367`, not behind a
`RunOption`. (`prices` sits behind `WithUsage`; do not follow it. There is
nothing to opt into here: the check reads one small file and never
touches the network.)

| state | severity | detail |
|---|---|---|
| no cache, or unparseable either side, or `KindUnknown` | `SevOK` | `not checked` |
| `KindLocalBuild` | `SevOK` | `local build <version>; nothing to update to` |
| latest not newer | `SevOK` | `<running> is current` |
| latest newer | `SevWarn` | `<running> is behind <latest>` + `Fix:` naming the variant's update path from §1's table |

`SevWarn`, never `SevFail`: a stale relay runs fine.

### 4.6 The one status line (`cmd/relay/main.go`)

```go
// statusNotice is the line `relay status` prints above the rows when the
// cached check says a newer release exists, and "" whenever it does not.
// Pure: every input is an argument, so it is table-tested without a
// store, a daemon or a network (CI has no herdr).
func statusNotice(running, latest string, ok bool, kind release.Kind) string
```

Returns `""` for every SevOK row in §4.5's table, and otherwise exactly:

```
relay v0.6.0 is behind v0.7.0 -- run relay doctor
```

One line, only when newer, nothing on the statusline (it is a fixed-width
surface and this does not earn a slot there).

## 5. Tests

Each bullet names the condition to break and the test that must fail when
you break it. A test that passes both ways pins nothing; prove each one.

1. `TestDetectTable` -- §4.1's six rows. Mutate: swap the first two rules'
   order; the `plugin-release` row must fail.
2. `TestNewerOrdersDescribeSuffix` -- `v0.6.0-2-g...` is newer than
   `v0.6.0`, older than `v0.6.1`. Mutate: make a suffix always older; the
   first case must fail.
3. `TestNewerRefusesUnparseable` -- `(devel)` on either side is false.
4. `TestLoadMissingAndMalformed` -- both are `(Cache{}, false, nil)`.
   Mutate: return an error for malformed; the test must fail.
5. `TestStaleTTL` -- fake clock at TTL-1s and TTL+1s.
6. `TestHTTPFetcherParsesTag` -- `httptest.NewServer` only.
7. `TestTickRefreshesOncePastTTL` -- fake fetcher counts calls: 0 when
   fresh, 1 when stale. Mutate: drop the `Stale` guard; the fresh case
   must fail.
8. `TestTickSurvivesFetchError` -- fetcher returns an error; `Tick`
   returns nil and the cache file is not written.
9. `TestDoctorReleaseCheck` -- §4.5's four rows through `fakeEnv`.
10. `TestStatusNotice` -- §4.6, including the `""` rows.

`cmd/relay` tests: `statusNotice` only, as a pure function. **Do not add a
test that executes a subcommand** -- CI runners have no `herdr` binary. If
a test needs config, it sets `XDG_CONFIG_HOME` to its own `t.TempDir()`
(#235); the package `TestMain` already points HOME and the XDG roots at a
temp root, and nothing here may read the user's real state.

## 6. Steps (one commit each)

1. `internal/release`: `version.go` + `provenance.go` + their tests. No
   callers yet. `feat(release): parse versions and detect the install variant (#293)`
2. `internal/release`: `cache.go` + `fetch.go` + their tests.
   `feat(release): a day-cached latest-release answer, fetched over an interface (#293)`
3. Daemon refresh (§4.4) + its two tests.
   `feat(daemon): refresh the cached release check once a day from the tick (#293)`
4. Doctor check (§4.5) + `cmd/relay/doctor.go` wiring.
   `feat(doctor): warn when a newer relay release exists (#293)`
5. `statusNotice` (§4.6) + its test.
   `feat(status): one line when the cached check says relay is behind (#293)`
6. Copy this plan to `docs/plans/`. `chore(plans): relay update part 1`

## 7. Gate

```
test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
go vet ./...
go test -race -count=1 ./internal/release/ ./internal/doctor/ ./internal/relay/ ./cmd/relay/
go test -count=1 ./...
go mod tidy && git diff --exit-code go.mod go.sum
```

`make e2e` is **not** in this gate and you must not run it: nothing here
touches `reconcile.go`'s nudge, fingerprint or scrape path, `reporttail.go`
or `queueReport`. Say so in the report rather than running it.

Report `git diff --stat` in full: it must name only §2's files.

## 8. Not yours (the planner does these after the round)

`make check` on the merged branch; deciding whether part 2 (`relay update`
itself) ships as one round or two; confirming a real `relay doctor` on a
plugin install reads the cache the daemon wrote.

## Report

The §3 probe output verbatim first. Then per step: test names, the
mutation you performed for each §5 bullet and the test that failed under
it, gate tail, commit shas; every exported identifier added with its
signature; `git diff --stat`; anything done that this plan did not say, or
where you halted.
