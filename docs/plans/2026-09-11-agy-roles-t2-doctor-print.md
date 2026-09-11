# agy roles T2: doctor rows for agy, the `inherit` warning, the version floor, `agent print --kind agy` (#85)

**Design spec:** `docs/specs/2026-09-11-agy-role-definitions-design.md` -- read §1, §3.2–3.3, §4.4–4.6, §4.10, §5.2, §6, §7.2, §7.6, §8
**Issue:** #85
**Depends on:** T1 (`Role.ExpectModel`, `Harness.MinVersion`, agy `Roles`, three `*.agy.md` definitions) -- already in this tree.

The spec is in your worktree. Read the section a step cites when the rationale
is not obvious -- this plan tells you what, the spec tells you why.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/<binding>` | **the git worktree. Every source edit goes here.** It is your shell's cwd. |
| `~/.local/state/relay/<binding>` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan.

## Running commands

Verification is `make check`. If `make` is intercepted on this machine, run its
constituents directly and say so in your report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

## Global constraints

- Go stdlib only. `go mod tidy` must produce no diff.
- This task touches `internal/doctor` and `cmd/relay` and their tests.
  Nothing in `internal/harness`, `internal/relay`, `internal/store`,
  `README.md` or `docs/`.
- CI runners have no `herdr` binary. The `cmd/relay` test you add runs
  `agent print`, which reads the embed FS and never reaches herdr -- keep
  it that way; do not add a test that executes `doctor` through `run`.
- Every exported symbol you add or change gets a doc comment explaining
  the rationale, in the house style (read `internal/doctor/doctor.go`
  for the tone).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: `Env.BinaryVersion`, the version row, the `ExpectModel` warning, agy through `roleCheck`, `agent print --kind agy`

**Files:**
- Modify: `internal/doctor/env.go`, `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`
- Modify: `cmd/relay/agent.go`, `cmd/relay/agent_test.go`

**Interfaces consumed** (from T1, `internal/harness`):

```go
type Role struct { Name, Path, Doc, ExpectModel string }
type Harness struct { Kind, Binary, Integration string; Roles []Role; MinVersion string }
func AgentDoc(role, kind string) ([]byte, error)   // now resolves every agy row
```

**Interfaces produced:**

```go
// internal/doctor
type Env interface {
    // ...existing methods...
    // BinaryVersion runs `<path> --version` and returns the first
    // whitespace-separated field of its trimmed stdout.
    BinaryVersion(ctx context.Context, path string) (string, error)
}
```

- [ ] **Step 1: `Env.BinaryVersion`** (spec §4.4)

  In `internal/doctor/env.go`, add to the `Env` interface:

  ```go
  // BinaryVersion runs `<path> --version` and returns the first field of
  // its trimmed stdout, so a harness with a version floor can be held to
  // it the way herdr is (spec §4.4). path came from LookPath.
  BinaryVersion(ctx context.Context, path string) (string, error)
  ```

  Implement on `realEnv`:

  ```go
  func (e *realEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
      out, err := exec.CommandContext(ctx, path, "--version").Output()
      if err != nil {
          return "", err
      }
      fields := strings.Fields(strings.TrimSpace(string(out)))
      if len(fields) == 0 {
          return "", errors.New("empty version output")
      }
      return fields[0], nil
  }
  ```

  (`agy --version` prints `1.2.1` alone; taking the first field also
  survives a binary that prints `agy 1.2.1`.)

  In `doctor_test.go`, extend `fakeEnv`:

  ```go
  versions   map[string]string // binary path -> version output
  versionErr error
  ```

  and

  ```go
  func (f *fakeEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
      if f.versionErr != nil {
          return "", f.versionErr
      }
      v, ok := f.versions[path]
      if !ok {
          return "", errors.New("no version recorded for " + path)
      }
      return v, nil
  }
  ```

  Run: `go build ./... && go vet ./internal/doctor/`. Expected: clean.

- [ ] **Step 2: the version row** (spec §4.6, §7.6)

  In `doctor.go` `Run`, inside the per-kind loop, after the binary has
  been found on PATH (the `binPath` from `env.LookPath`) and before the
  role checks, add -- only when `known && h.MinVersion != ""`:

  ```go
  // A harness with a floor is held to it before its roles are checked:
  // below the floor, --agent has nothing to select and every role row
  // would be reporting a file the binary cannot load (spec §7.6).
  ver, verr := env.BinaryVersion(ctx, binPath)
  switch {
  case verr != nil:
      checks = append(checks, Check{
          Group: kind, Name: "version", Severity: SevWarn,
          Detail: fmt.Sprintf("could not read version: %v", verr), ProbeFailed: true,
      })
  default:
      atLeast, semErr := semverAtLeast(ver, h.MinVersion)
      switch {
      case semErr != nil:
          checks = append(checks, Check{
              Group: kind, Name: "version", Severity: SevWarn,
              Detail: fmt.Sprintf("unparseable version %q", ver),
          })
      case !atLeast:
          checks = append(checks, Check{
              Group: kind, Name: "version", Severity: SevFail,
              Detail: fmt.Sprintf("%s (below floor %s)", ver, h.MinVersion),
              Fix:    fmt.Sprintf("upgrade %s to >= %s", h.Binary, h.MinVersion),
          })
      default:
          checks = append(checks, Check{
              Group: kind, Name: "version", Severity: SevOK,
              Detail: fmt.Sprintf("%s (floor %s)", ver, h.MinVersion),
          })
      }
  }
  ```

  Mirror the herdr version block's shape (search `herdr.MinVersion` in
  the same file); reuse `semverAtLeast` as is.

  Tests, in `doctor_test.go` -- one fake env each, all with
  `lookPaths: {"agy": "/home/fuad/.local/bin/agy"}`, an installed
  `antigravity-cli` integration, and the three agy role files present
  (see step 4 for how to mark them present; you can write step 4's
  helper first):

  ```go
  func TestDoctorAgyVersionFloor(t *testing.T) {
      cases := []struct {
          name     string
          version  string
          err      error
          wantSev  Severity
          wantDet  string
          wantFix  string
          wantProb bool
      }{
          {name: "at floor", version: "1.1.6", wantSev: SevOK, wantDet: "1.1.6 (floor 1.1.6)"},
          {name: "above floor", version: "1.2.1", wantSev: SevOK, wantDet: "1.2.1 (floor 1.1.6)"},
          {name: "below floor", version: "1.1.5", wantSev: SevFail, wantDet: "1.1.5 (below floor 1.1.6)", wantFix: "upgrade agy to >= 1.1.6"},
          {name: "garbage", version: "garbage", wantSev: SevWarn, wantDet: `unparseable version "garbage"`},
          {name: "probe fails", err: errors.New("boom"), wantSev: SevWarn, wantDet: "could not read version: boom", wantProb: true},
      }
      for _, tc := range cases {
          t.Run(tc.name, func(t *testing.T) {
              env := agyEnv(t) // step 4's helper: paths, integration, three role files pinning inherit
              env.versions = map[string]string{"/home/fuad/.local/bin/agy": tc.version}
              env.versionErr = tc.err
              report := Run(context.Background(), env, []string{"agy"})
              c := findCheck(report, "agy", "version")
              if c == nil {
                  t.Fatal("agy version row not found")
              }
              if c.Severity != tc.wantSev || c.Detail != tc.wantDet || c.Fix != tc.wantFix || c.ProbeFailed != tc.wantProb {
                  t.Errorf("row = %+v, want sev %v detail %q fix %q probeFailed %v", *c, tc.wantSev, tc.wantDet, tc.wantFix, tc.wantProb)
              }
          })
      }
  }

  func TestDoctorNoVersionRowWithoutAFloor(t *testing.T) {
      for _, kind := range []string{"claude", "opencode"} {
          env := &fakeEnv{
              herdrVer:  "0.9.0",
              lookPaths: map[string]string{kind: "/usr/bin/" + kind},
              intStatus: map[string]herdr.IntegrationState{kind: {Installed: true}},
          }
          report := Run(context.Background(), env, []string{kind})
          if c := findCheck(report, kind, "version"); c != nil {
              t.Errorf("%s has no MinVersion, got version row %+v", kind, *c)
          }
      }
  }
  ```

  Run: `go test ./internal/doctor/ -run 'TestDoctorAgyVersionFloor|TestDoctorNoVersionRow'`. Expected: green.

- [ ] **Step 3: `roleCheck` warns when the pin differs from `ExpectModel`** (spec §4.5, §7.2)

  In `roleCheck`, after `frontmatterModel` is read, replace the trailing
  `detail = ...; return SevOK` with:

  ```go
  detail := homeRel
  // A read error is deliberately swallowed: the file exists, which is
  // what this row reports, and the model pin is a courtesy on top of
  // that -- except on a kind whose pin would override the launch line.
  if raw, err := env.ReadFile(fullPath); err == nil {
      if model := frontmatterModel(raw); model != "" {
          detail = fmt.Sprintf("%s (model: %s)", homeRel, model)
          if r.ExpectModel != "" && model != r.ExpectModel {
              return Check{
                  Group: kind, Name: r.Name, Severity: SevWarn,
                  Detail: fmt.Sprintf("%s -- pins a tier; the candidate's --model is ignored", detail),
                  Fix:    fmt.Sprintf("set model: %s in %s", r.ExpectModel, homeRel),
              }
          }
      }
  }
  return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
  ```

  A missing pin is not a warning: agy's default is `inherit`.

  Test:

  ```go
  func TestDoctorAgyRoleWarnsOnATierPin(t *testing.T) {
      env := agyEnv(t)
      env.versions = map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"}
      env.fileContents[env.homeDir+"/.gemini/config/agents/researcher.md"] = "---\nname: researcher\nmodel: pro\n---\nbody\n"
      report := Run(context.Background(), env, []string{"agy"})

      c := findCheck(report, "agy", "researcher")
      if c == nil {
          t.Fatal("agy researcher row not found")
      }
      if c.Severity != SevWarn {
          t.Errorf("severity = %v, want SevWarn", c.Severity)
      }
      if c.Detail != "~/.gemini/config/agents/researcher.md (model: pro) -- pins a tier; the candidate's --model is ignored" {
          t.Errorf("detail = %q", c.Detail)
      }
      if c.Fix != "set model: inherit in ~/.gemini/config/agents/researcher.md" {
          t.Errorf("fix = %q", c.Fix)
      }
      for _, name := range []string{"plan-executor", "reviewer"} {
          if c := findCheck(report, "agy", name); c == nil || c.Severity != SevOK {
              t.Errorf("%s row = %+v, want SevOK", name, c)
          }
      }
  }

  func TestDoctorClaudeRoleNeverWarnsOnAPin(t *testing.T) {
      env := &fakeEnv{
          herdrVer:  "0.9.0",
          homeDir:   "/home/u",
          lookPaths: map[string]string{"claude": "/usr/bin/claude"},
          intStatus: map[string]herdr.IntegrationState{"claude": {Installed: true}},
          existingFiles: map[string]bool{"/home/u/.claude/agents/plan-executor.md": true},
          fileContents:  map[string]string{"/home/u/.claude/agents/plan-executor.md": "---\nmodel: opus\n---\n"},
      }
      report := Run(context.Background(), env, []string{"claude"})
      c := findCheck(report, "claude", "plan-executor")
      if c == nil || c.Severity != SevOK || c.Detail != "~/.claude/agents/plan-executor.md (model: opus)" {
          t.Errorf("row = %+v, want SevOK with the pin reported", c)
      }
  }
  ```

  Check how existing role tests set `homeDir`, `existingFiles` and
  `fileContents` on `fakeEnv` (search `existingFiles[` in the file) and
  match those conventions exactly for path joining.

- [ ] **Step 4: agy goes through `roleCheck`; the preamble branch goes** (spec §4.6)

  In `Run`, delete the `case len(h.Roles) == 0:` arm and its comment
  block ("Role checks: one row per shipped role. A harness with no roles
  selects its role with a preamble…"). Replace the comment with: "Role
  checks: one row per shipped role. Every known kind has rows (#85)."

  Tests: delete `TestDoctorAgyRoleSelectedByPreamble`. Add the helper the
  earlier steps used, and a happy-path test:

  ```go
  // agyEnv is an agy machine in good order: binary on PATH, integration
  // installed, three role files present and pinning inherit. Tests
  // perturb one thing at a time from here.
  func agyEnv(t *testing.T) *fakeEnv {
      t.Helper()
      home := "/home/u"
      env := &fakeEnv{
          herdrVer:      "0.9.0",
          homeDir:       home,
          lookPaths:     map[string]string{"agy": "/home/fuad/.local/bin/agy"},
          intStatus:     map[string]herdr.IntegrationState{"antigravity-cli": {Installed: true, Detail: "current (v3)"}},
          existingFiles: map[string]bool{},
          fileContents:  map[string]string{},
          versions:      map[string]string{"/home/fuad/.local/bin/agy": "1.2.1"},
      }
      for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
          p := home + "/.gemini/config/agents/" + name + ".md"
          env.existingFiles[p] = true
          env.fileContents[p] = "---\nname: " + name + "\nmodel: inherit\n---\nbody\n"
      }
      return env
  }

  func TestDoctorAgyRolesAreCheckedLikeAnyKind(t *testing.T) {
      env := agyEnv(t)
      report := Run(context.Background(), env, []string{"agy"})
      for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
          c := findCheck(report, "agy", name)
          if c == nil {
              t.Fatalf("agy %s row not found", name)
          }
          want := "~/.gemini/config/agents/" + name + ".md (model: inherit)"
          if c.Severity != SevOK || c.Detail != want {
              t.Errorf("%s row = %+v, want SevOK %q", name, *c, want)
          }
      }
  }

  func TestDoctorAgyMissingRoleHasAFix(t *testing.T) {
      env := agyEnv(t)
      delete(env.existingFiles, "/home/u/.gemini/config/agents/reviewer.md")
      report := Run(context.Background(), env, []string{"agy"})
      c := findCheck(report, "agy", "reviewer")
      if c == nil || c.Severity != SevWarn || c.Detail != "missing: ~/.gemini/config/agents/reviewer.md" ||
          c.Fix != "relay agent print --kind agy --role reviewer > ~/.gemini/config/agents/reviewer.md" {
          t.Errorf("row = %+v", c)
      }
  }
  ```

  Adjust `agyEnv`'s `homeDir`/path conventions to whatever `fakeEnv.HomePath`
  actually does (read it) so the joined paths match `existingFiles` keys.

  Run: `go test ./internal/doctor/`. Expected: green, and
  `grep -n 'preamble' internal/doctor/` returns nothing.

- [ ] **Step 5: `relay agent print --kind agy`** (spec §4.10)

  In `cmd/relay/agent.go`:
  - Delete the `if *kind == "agy" { ... }` refusal block.
  - Every usage string becomes
    `usage: relay agent print --kind <agy|claude|opencode> [--role <plan-executor|researcher|reviewer>]`.
  - The unknown-kind message becomes
    `relay: unknown kind %q (known: agy, claude, opencode)`.

  In `cmd/relay/agent_test.go`: delete `TestAgentPrintAgyExits2`. Add,
  modelled on `TestAgentPrintClaudeByteIdentical`:

  ```go
  func TestAgentPrintAgyByteIdentical(t *testing.T) {
      for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
          expected, err := harness.AgentDoc(role, "agy")
          if err != nil {
              t.Fatalf("AgentDoc(%s, agy): %v", role, err)
          }
          stdout, stderr, runErr := captureOutput(t, func() error {
              return run([]string{"agent", "print", "--kind", "agy", "--role", role})
          })
          if runErr != nil {
              t.Fatalf("%s: unexpected error: %v", role, runErr)
          }
          if len(stderr) != 0 {
              t.Errorf("%s: expected empty stderr, got %q", role, string(stderr))
          }
          if !bytes.Equal(stdout, expected) {
              t.Errorf("%s: stdout not byte-identical to embedded agy doc", role)
          }
      }
  }
  ```

  Update the expected message in `TestAgentPrintUnknownKindExits2` if it
  pins the old "known with definitions: claude, opencode" text.

  Run: `go test ./cmd/relay/`. Expected: green.

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add internal/doctor cmd/relay
  git commit -m "feat(doctor): agy role rows, inherit-pin warning, version floor; agent print serves agy (#85)"
  ```

## Report

Include the `go test ./internal/doctor/ -v` summary, the `make check`
result, and `git diff --stat HEAD~1` (five files). If any `fakeEnv` path
convention differed from what this plan assumed, say what you matched it
to.
