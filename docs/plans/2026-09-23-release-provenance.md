# Plan: a release binary knows it is one (#293, provenance fix only)

If any step is impossible as written or contradicts the code you find, STOP
and report what you found. Do not bend a test or the design to fit. Halt and
report if any of the following is false.

- **`release.Detect` classifies a clean-tag binary with no module version as
  `KindUnknown`.** That is `internal/release/provenance.go`, and the table
  rows "old plugin install: a clean tag claims nothing" and "otherwise: a
  clean tag with no other evidence claims nothing" in
  `provenance_test.go` pin it.
- **`statusNotice` (`cmd/relay/main.go` ~189) returns `""` only for
  `KindUnknown` and `KindLocalBuild`**, so a new kind passes through it
  without a code change. If it has become an allow-list, add `KindRelease`
  to it and say so in the report.
- **`releaseFix` (`internal/doctor/doctor.go` ~406) takes only a
  `release.Kind`**, and `releaseCheck` is its only caller.
- **`.github/workflows/release.yml` builds each archive with
  `-ldflags "-s -w -X main.version=${version}"`** and names it
  `relay_${version}_${goos}_${goarch}.tar.gz`, where `version` is the tag
  including its `v`. The published v0.9.0 assets are
  `relay_v0.9.0_linux_amd64.tar.gz` and so on, plus `checksums.txt`.

## 1. System Overview

`relay doctor` and `relay status` warn when a newer relay release exists
(#293 part 1, `internal/release`). The warning is gated on
`release.Detect`, which classifies how the running binary was installed:
`go-install` (the module supplied the version), `local-build` (`(devel)` or
a `git describe` suffix), or `unknown`.

A binary unpacked from a release tarball is stamped with a clean tag and
nothing else, so it classifies as `unknown`. Doctor reads `unknown` as "not
checked" and never warns. After herdr was dropped (#303), the tarball is the
README's first install method, so the commonest install is the one that is
never told it is stale.

A clean tag alone cannot identify a release build: `make install` on a
checkout that sits exactly on a tag stamps the same `v0.9.0`. So this change
adds a second stamp that only `release.yml` sets:

- `release.yml` adds `-X main.distribution=release` to its ldflags.
- `release.Inputs` gains `Distribution`, filled from `main.distribution`.
- `release.Detect` returns a new `KindRelease` when `Distribution` is
  `"release"` and the version is a clean tag.
- Doctor's fix line for `KindRelease` names the exact archive and
  `checksums.txt` URLs for this platform and the cached latest tag.
- `relay status`'s one-line notice covers `KindRelease` with no code change.

**Deliberately no fallback for unstamped binaries.** #293's second finding
proposed reading "exact tag, no manifest, not from module" as a release
binary for tarballs shipped before the stamp existed. That fallback cannot
help: a binary shipped before this change does not contain this `Detect`.
The only unstamped clean-tag binaries that run this code are local builds
at a tag, and those are exactly what the fallback would misclassify. The two
existing `KindUnknown` rows stay as they are.

**Out of scope:** `relay update` or any self-update, download or binary
swap; any network access; `relay version` output; the Makefile; any change
to `go install` or local-build behaviour.

## 2. File Structure

```
cmd/relay/
  main.go            # + var distribution; releaseInputs sets Distribution
  main_test.go       # + statusNotice row for KindRelease
  release_test.go    # NEW: release.yml carries the stamp and the asset name
internal/release/
  provenance.go      # + KindRelease, DistributionRelease, Inputs.Distribution, Detect branch
  provenance_test.go # + rows
  assets.go          # NEW: DownloadBase, AssetURLs (pure)
  assets_test.go     # NEW
internal/doctor/
  doctor.go          # releaseFix gains latest/goos/goarch; KindRelease fix line
  doctor_test.go     # + releaseCheck rows
.github/workflows/release.yml  # + -X main.distribution=release
README.md            # one paragraph under ## Install
```

No other file changes. `internal/doctor/env.go` needs no change:
`ReleaseState` already calls `release.Detect(e.self)`, and `e.self` comes from
`releaseInputs()`.

## 3. Data Structures & Type Definitions

### `release.KindRelease` (`internal/release/provenance.go`)

`KindRelease Kind = "release"`, added to the existing const block after
`KindLocalBuild`. It means the binary was built by `release.yml` and
unpacked from a published archive.

### `release.DistributionRelease` (same file)

`const DistributionRelease = "release"`. It is the only value of
`main.distribution` that `Detect` recognises. Any other non-empty value,
such as a future `"homebrew"`, falls through to the existing rules.

### `release.Inputs.Distribution string` (same file)

Add it after `FromModule`, with a doc comment: it is the `main.distribution`
ldflags stamp. Only `release.yml` sets it, and it is empty in every other
build. It is optional, and the zero value means "no claim".

### `main.distribution` (`cmd/relay/main.go`)

`var distribution = ""` beside `var version`, with a comment saying it is
stamped by `release.yml` with `-X main.distribution=release`, and that no
Makefile target or `go install` sets it.

### `release.DownloadBase` (`internal/release/assets.go`)

`const DownloadBase = "https://github.com/fuad-daoud/relay/releases/download"`.
It must not be overridable by an environment variable: nothing downloads
from it, it is only printed.

## 4. Interface Definitions & Component Contracts

### `release.Detect(in Inputs) Kind` (changed)

The order of the rules is part of the contract:

1. `in.FromModule` gives `KindGoInstall` (unchanged, still first).
2. `in.Distribution == DistributionRelease` **and** `ParseVersion(in.Version)`
   is ok **and** its `Suffix == ""` gives `KindRelease`.
3. `(devel)` or a describe suffix gives `KindLocalBuild` (unchanged).
4. Otherwise `KindUnknown` (unchanged).

The stamp together with a non-clean version (a describe suffix, `-dirty` or
`(devel)`) is not a release. It falls through to rule 3, which classifies it
as a local build. Detect stays pure.

### `release.AssetURLs(tag, goos, goarch string) (archive, checksums string)` (new, pure)

- Precondition: none. It formats whatever it is given.
- Postcondition:
  - `archive = DownloadBase + "/" + tag + "/relay_" + tag + "_" + goos + "_" + goarch + ".tar.gz"`
  - `checksums = DownloadBase + "/" + tag + "/checksums.txt"`
- For `("v0.9.0", "linux", "amd64")` the archive is
  `https://github.com/fuad-daoud/relay/releases/download/v0.9.0/relay_v0.9.0_linux_amd64.tar.gz`.
- It has no error return. Callers pass only a tag `ParseVersion` accepted.

### `doctor.releaseFix(kind release.Kind, latest, goos, goarch string) string` (changed signature)

- `KindGoInstall` keeps its current string exactly.
- `KindRelease` returns this, with the two URLs from
  `release.AssetURLs(latest, goos, goarch)`:
  `download <archive>, check it against <checksums>, and replace this relay binary with the one inside`
- Every other kind returns `""`.

`releaseCheck` passes `latest`, `runtime.GOOS` and `runtime.GOARCH`. The
`runtime` reads happen in `releaseCheck`, never inside `releaseFix`, so the
fix text stays table-testable for any platform.

### `releaseCheck` (behaviour, no code change beyond the call)

For `KindRelease`, the existing flow already gives the right rows:
"not checked" when there is no cache or it cannot be parsed, `"<v> is
current"`, and `SevWarn` `"<v> is behind <latest>"` with the new fix. Do not
add a `KindRelease` branch to it.

### `statusNotice` (no change)

A new test row pins that `KindRelease` behind a newer tag prints
`relay v0.8.0 is behind v0.9.0 -- run relay doctor`.

## 5. High-Level Pseudocode

```
release.yml build:  go build -ldflags "-s -w -X main.version=$tag -X main.distribution=release"

relay doctor / status:
  in := releaseInputs()                 # + Distribution = main.distribution
  kind := release.Detect(in)            # stamp + clean tag -> KindRelease
  (running, latest, ok) from the daemon's cache   # unchanged
  releaseCheck:
     no cache / unparseable / unknown  -> OK "not checked"
     local build                       -> OK "nothing to update to"
     not newer                         -> OK "<v> is current"
     newer                             -> WARN "<v> is behind <latest>",
                                          Fix = releaseFix(kind, latest, GOOS, GOARCH)
  statusNotice: unchanged; KindRelease now reaches the "behind" line
```

## 6. Error Handling Strategy

There are no new errors. The contract is the same as the rest of
`internal/release`: relay never claims staleness it cannot prove. An
unrecognised `distribution` value, a stamped non-clean version, and an
unparseable cached tag each degrade to the existing quiet rows. `releaseFix`
cannot fail. Nothing new is logged.

## 7. Ordered Implementation Steps

After every step, run `make check` and `gofmt -l $(git ls-files '*.go')`.
The second must print nothing. No test may touch the network or run a
subcommand: CI runners have no harness binary and no network. The one new
`cmd/relay` test reads a file from the repo and nothing else.

1. **Kind and detection.** In `provenance.go`, add `KindRelease`,
   `DistributionRelease`, `Inputs.Distribution` and Detect's rule 2.
   *Verify:* new `TestDetectTable` rows:
   - `{Version: "v0.9.0", Distribution: "release"}` gives `KindRelease`.
   - `{Version: "v0.9.0-3-gabc1234", Distribution: "release"}` gives
     `KindLocalBuild`.
   - `{Version: "(devel)", Distribution: "release"}` gives `KindLocalBuild`.
   - `{Version: "v0.9.0", Distribution: "homebrew"}` gives `KindUnknown`.
   - `{Version: "v0.9.0", FromModule: true, Distribution: "release"}` gives
     `KindGoInstall`, which pins that rule 1 comes first.
   - Every existing row is unchanged, including both clean-tag
     `KindUnknown` rows.

   *Mutation (required):* delete rule 2 and confirm the first new row fails.
   Restore it.

2. **Asset URLs.** Add `assets.go` with `DownloadBase` and `AssetURLs`.
   *Verify:* `TestAssetURLs` asserts both strings exactly for
   `("v0.9.0", "linux", "amd64")` and `("v1.2.3", "darwin", "arm64")`.

3. **Doctor fix line.** Change `releaseFix`'s signature and call site, and
   add the `KindRelease` case.
   *Verify:* new rows in the existing `releaseCheck` table in
   `doctor_test.go`:
   - `KindRelease`, running `v0.8.0`, latest `v0.9.0`, ok: `SevWarn`,
     detail `v0.8.0 is behind v0.9.0`, and a fix that is exactly the
     `KindRelease` string built from
     `release.AssetURLs("v0.9.0", runtime.GOOS, runtime.GOARCH)`. The test
     builds the expected string the same way, so it passes on any
     platform.
   - `KindRelease`, `v0.9.0` against `v0.9.0`: `SevOK`, `v0.9.0 is current`.
   - Add a pure `TestReleaseFix` table:
     - `(KindRelease, "v0.9.0", "linux", "amd64")` gives the literal
       string with the linux/amd64 URLs written out in full.
     - `(KindGoInstall, …)` gives the unchanged `go install` line.
     - `(KindUnknown, …)` and `(KindLocalBuild, …)` give `""`.

   *Mutation (required):* remove the `KindRelease` case and confirm the
   `TestReleaseFix` release row fails. Restore it.

4. **Stamp wiring.** Add `var distribution` in `cmd/relay/main.go` and set
   `in.Distribution = distribution` in `releaseInputs()`. Add the
   `statusNotice` row from §4 to `main_test.go`.
   *Verify:* the new `statusNotice` row passes.

5. **Workflow stamp and its guard.** In `release.yml`, change the ldflags to
   `-s -w -X main.version=${version} -X main.distribution=release`, and
   change nothing else in the file. Add `cmd/relay/release_test.go` with
   `TestReleaseWorkflowStampsDistribution`. It reads
   `../../.github/workflows/release.yml` and asserts that the file contains:
   - the substring `"-X main.distribution=" + release.DistributionRelease`
   - the substring `relay_${version}_${goos}_${goarch}.tar.gz`, the name
     `AssetURLs` formats
   - the substring `checksums.txt`

   The failure messages say which link broke: the stamp, the archive name
   or the checksum file.
   *Verify:* the test passes. `go build -ldflags "-X main.distribution=release -X main.version=v0.9.0" -o /tmp/r ./cmd/relay`
   builds. Do not run the resulting binary against real state.

   *Mutation (required):* change `DistributionRelease` to `"releases"` and
   confirm this test **and** step 1's row both fail. Restore it.

6. **README.** Under `## Install`, after the `go install` paragraph, add one
   short paragraph:
   - A release binary and a `go install` both know how they were installed.
     `relay doctor` warns when a newer release exists and prints the update
     step for that install: the archive and `checksums.txt` to download, or
     the `go install` command.
   - `relay status` shows one line when a newer release exists.
   - A local build is never called stale.

   *Verify:* `make check` passes.

## Report

- The files touched in each step, and `git diff --stat` against `main`. It
  must match §2 exactly.
- All three required mutation checks: what you broke, and which named test
  failed.
- What you found for each halt condition, including the actual
  `statusNotice` guard and the actual `release.yml` ldflags line before your
  edit.
