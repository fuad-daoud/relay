# Doctor first-run pass: policy refusals, honest summaries, selectable role rows (#165, #166)

> **For agentic workers:** execute the tasks in order; each ends green. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Spec:** none. The design was approved in chat on 2026-09-16 and is
recorded in the "Design" section below; the tasks argue from it.
**Issues:** #165, #166. Closes #165; closes #166 except its §5 `relay init`
proposal, which the planner files as a follow-up.
**Depends on:** nothing open.

**Goal:** `relay doctor` stops saying `relay can run` on a machine where the
first `relay add` would refuse, stops blaming harnesses when the only problem
is a missing `candidates.json`, stops warning about role definitions no
candidate on that harness would load, prints fix commands that work on a
clean machine, and the from-source plugin build stamps a version a human can
place against a release.

**Architecture:** One new pure function in `internal/relay`
(`RoleRefusals`) computed by the same `resolveCandidate` call `bind` and
`relay policy` make, so the three cannot disagree. `harness.RoleSpec` learns
which definitions a role needs installed (`Definitions`), and
`doctor.Run` gains a `WithDefinitions` option that limits role rows to them.
`cmd/relay/doctor.go` wires both in and owns the footer precedence:
failures › no candidates › no usable builder › builder would refuse › can
run. `scripts/plugin-build.sh` fetches tags before `git describe` and falls
back to `<manifest version>+<hash>`.

**Tech stack:** Go 1.22, POSIX sh. Verification is the `make check`
constituent set (runs `-race`, shellcheck, and `scripts/*_test.sh`).

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/doctor-first-run` | **the git worktree. Every source edit goes here.** It is your shell's cwd. Branch `relay/doctor-first-run`, cut from `main`. |
| `~/.local/state/relay/doctor-first-run` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

`pwd` is the worktree. Prefer paths relative to it.

## Resuming a round another builder started

The branch carries one `docs:` commit from the planner (this plan) and
may carry commits from an earlier builder on this same round. Before Task 1, run `git log --oneline main..HEAD`. A task whose
commit message is already there is **done**: run its verification step to
confirm it is green, tick it, and continue with the next task. Do not redo
it, do not amend it. An untracked or modified file from an unfinished task
is yours to finish or replace as that task's steps say.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and
do not invent an API that is not in the plan.

## Running commands

`make` is intercepted on this machine by an unrelated wrapper. Run the
constituents of `make check` directly, in this order, and say so in your
report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
sh scripts/check-plugin-version.sh
command -v shellcheck >/dev/null && shellcheck scripts/*.sh
for t in scripts/*_test.sh; do echo "==> $t"; sh "$t"; done
```

Do **not** run `herdr` or `make e2e` yourself. Tests under `cmd/relay` are
allowed only as pure-function tests of `renderReport`, `assembleDefinitions`,
`refusalChecks` and `bindPreflight` with the existing `stubDoctorEnv` -- none
may execute a subcommand, because CI runners have no `herdr` (CLAUDE.md).
Every other new test is a pure function in `internal/relay`,
`internal/doctor` or `internal/harness`, using the existing helpers
`candidateSet`, `orderOf`, `limit`, `testCandidatesJSON`
(`internal/relay/candidate_test.go`), `fakeEnv`, `newFakeEnvForKind`,
`findCheck`, `countChecksForGroup` (`internal/doctor/doctor_test.go`) and
`testSet`, `threeKinds` (`cmd/relay/doctor_test.go`).

## Design

Recorded here because there is no spec file. Section letters (§A..§E) are
referenced from the tasks.

**§A. Policy refusal.** For each role in `harness.RoleNames()` order with at
least one serving candidate, call `resolveCandidate(set, pol, gates, "",
role)`. `ErrAmbiguousCandidate` is a refusal with text `N candidates serve
<role> and no order is set`; `ErrAllGated` is a refusal with text `every
candidate serving <role> is gated`. These are the strings `relay policy`
prints today after `would refuse:`; after this plan both come from one
function. In doctor each refusal is a global `policy  warn` row. Detail is
the text plus a suffix naming the verb that refuses: for `builder`, ` --
add/bind without --builder would refuse`; for any other role, ` -- ask
--role <role> without --candidate would refuse`. The fix for the no-order
case is a single-line `policy.json` built from the actual serving tokens:
`write ~/.config/relay/policy.json, e.g. {"order":{"builder":["agy/test/m","claude/test/m"]}}`.
The fix for the gated case is `relay available <provider>` with the first
gated provider in serving order. A `builder` refusal also sets
`Report.BuilderRefusal` to its text, which the footer prints as
`<warnings>, <failures> -- relay cannot pick a builder: <text>.`

**§B. Zero candidates.** When no candidate is configured the `candidates`
row's fix becomes
`write ~/.config/relay/candidates.json, e.g. [{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`
and the footer is `<warnings>, <failures> -- no candidates configured; write
~/.config/relay/candidates.json first.` regardless of `UsableBuilder`.

**§C. Selectable definitions.** `harness.RoleSpec.Definitions` lists every
definition a harness must have installed to run the role: `builder` needs
`plan-executor` **and** `researcher`, because every shipped
`plan-executor.<kind>.md` dispatches its research sub-agents as the
`researcher` agent (`internal/harness/agents/plan-executor.claude.md:108`,
`.opencode.md:108`, `.agy.md:127`); `reviewer` needs `reviewer`;
`researcher` needs `researcher`. `Definitions[0] == Definition` always.
Doctor checks, per kind, the union of `Definitions` over the roles of every
candidate on that harness; a kind that reaches doctor only through an
existing binding gets `builder`'s list. Unselectable definitions get no row.
`bind`'s preflight always binds a builder, so it passes `builder`'s list.
(This corrects #166 §3, which assumed `researcher` is only needed by a
`researcher` candidate.)

**§D. mkdir prefix.** A missing role file's fix is
`mkdir -p ~/<dir> && relay agent print --kind <kind> --role <role> > ~/<path>`.
The drift fix (file exists, differs from shipped) is unchanged.

**§E. Version stamp.** `scripts/plugin-build.sh`: `git describe --tags
--dirty`; if that fails, `git fetch --tags --quiet` (errors ignored) and
try again; if that still fails, `<version from ./herdr-plugin.toml>+<short
hash>` (`devel+<hash>` when the manifest is unreadable); `devel` when
there is no git at all. `--always` is no longer used: a bare hash is what
#166 §4 complains about.

## Global constraints

- `RoleRefusals` and `FormatPolicy` both go through `refusalFromErr`;
  neither builds a refusal string any other way (§A).
- The two existing `FormatPolicy` tests that pin `would refuse:` lines
  (`TestFormatPolicyNoOrderTwoServeRefuses`, `TestFormatPolicyAllGated`) are
  not edited; they pin the refactor.
- `doctor.Run` with no `WithDefinitions` option keeps checking every shipped
  definition (existing `internal/doctor` tests are unchanged except the one
  fix-string assertion named in Task 3).
- Footer precedence in `renderReport` is exactly: failures › `NoCandidates`
  › `!UsableBuilder` › `BuilderRefusal != ""` › `relay can run`.
- `Check.Fix` stays a single line everywhere; no `\n` in any fix string.
- `relay policy` output for every existing test stays byte-identical.
- No new subcommand or flag. `relay init` is out of scope.
- `internal/relay/e2e_test.go`, `send.go`, `reconcile.go`, `deliver.go` are
  not edited.
- One commit per task, on the worktree's branch.

---

### Task 1: `harness.RoleSpec.Definitions`; `CanServe` requires all of them

**Files:**
- Modify: `internal/harness/harness.go` (`RoleSpec`, `roleTable`, `CanServe`)
- Modify: `internal/harness/harness_test.go`

**Interfaces:**
- Produces: `harness.RoleSpec.Definitions []string`. `builder` →
  `[]string{"plan-executor", "researcher"}`; `reviewer` →
  `[]string{"reviewer"}`; `researcher` → `[]string{"researcher"}`.
- `CanServe(role)` returns true only when `h.Role(d)` is found for every `d`
  in `Definitions` (today it checks `Definition` alone; every known kind
  ships all three, so no candidate file changes validity).

- [ ] **Step 1: Write the failing tests** in `internal/harness/harness_test.go`:

```go
func TestRoleDefinitionsIncludeDispatchTargets(t *testing.T) {
	want := map[string][]string{
		"builder":    {"plan-executor", "researcher"},
		"reviewer":   {"reviewer"},
		"researcher": {"researcher"},
	}
	for role, defs := range want {
		spec, ok := RoleByName(role)
		if !ok {
			t.Fatalf("RoleByName(%q) not found", role)
		}
		if !reflect.DeepEqual(spec.Definitions, defs) {
			t.Errorf("%s Definitions = %v, want %v", role, spec.Definitions, defs)
		}
		if spec.Definitions[0] != spec.Definition {
			t.Errorf("%s Definitions[0] = %q, want Definition %q", role, spec.Definitions[0], spec.Definition)
		}
	}
}

// The builder needs researcher installed because its own definition
// dispatches to it. Pin the reason, not just the table.
func TestPlanExecutorDispatchesResearcherOnEveryKind(t *testing.T) {
	for _, h := range All() {
		doc, err := AgentDoc("plan-executor", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", h.Kind, err)
		}
		if !strings.Contains(string(doc), "researcher") {
			t.Errorf("%s plan-executor does not mention researcher; Definitions for builder is wrong", h.Kind)
		}
	}
}

func TestCanServeRequiresEveryDefinition(t *testing.T) {
	h := Harness{Kind: "partial", Roles: []Role{{Name: "plan-executor"}}}
	if h.CanServe("builder") {
		t.Error("a harness shipping plan-executor but not researcher must not serve builder")
	}
	h.Roles = append(h.Roles, Role{Name: "researcher"})
	if !h.CanServe("builder") {
		t.Error("plan-executor + researcher must serve builder")
	}
	if h.CanServe("reviewer") {
		t.Error("no reviewer definition must not serve reviewer")
	}
}
```

`Harness.Kind` is the harness's kind string; `reflect` and `strings` are
already imported in this test file.

- [ ] **Step 2: Run to verify they fail** --
`go test -count=1 ./internal/harness -run 'TestRoleDefinitions|TestPlanExecutorDispatches|TestCanServeRequires'`:
compile error, `Definitions` undefined.

- [ ] **Step 3: Implement.** In `RoleSpec` add, after `Definition`:

```go
	// Definitions is every definition a harness must have installed to
	// run the role: Definition first, then what it dispatches to. The
	// builder's plan-executor sends its research sub-agents to
	// researcher, so a builder-only harness needs both; a consult role
	// needs only its own. Doctor checks these and nothing else (#166).
	Definitions []string
```

Set it on each `roleTable` entry per the Interfaces block. Rewrite
`CanServe`:

```go
func (h Harness) CanServe(role string) bool {
	spec, ok := RoleByName(role)
	if !ok {
		return false
	}
	for _, d := range spec.Definitions {
		if _, found := h.Role(d); !found {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/harness ./internal/candidate ./internal/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness/harness.go internal/harness/harness_test.go
git commit -m "feat(harness): a role names every definition it needs installed (#166)"
```

---

### Task 2: `doctor.WithDefinitions` limits role rows per kind

**Files:**
- Modify: `internal/doctor/doctor.go` (`runConfig`, new `WithDefinitions`, the role loop in `Run`)
- Modify: `internal/doctor/doctor_test.go`

**Interfaces:**
- Produces: `func WithDefinitions(defs map[string][]string) RunOption`.
  `defs[kind]` lists the definition names (`"plan-executor"`, ...) to check
  for that kind. A kind absent from the map, or a nil map, keeps every
  shipped definition.

- [ ] **Step 1: Write the failing tests** in `internal/doctor/doctor_test.go`:

```go
func TestDoctorChecksOnlyTheDefinitionsGiven(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	// No role files installed at all.
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"claude": {"plan-executor", "researcher"}}))

	for _, name := range []string{"plan-executor", "researcher"} {
		c := findCheck(report, "claude", name)
		if c == nil {
			t.Fatalf("no %s row for claude", name)
		}
		if c.Severity != SevWarn {
			t.Errorf("%s severity = %v, want warn (file missing)", name, c.Severity)
		}
	}
	if c := findCheck(report, "claude", "reviewer"); c != nil {
		t.Errorf("reviewer row present although no candidate on claude can select it: %+v", *c)
	}
}

func TestDoctorKindAbsentFromDefinitionsKeepsEveryRow(t *testing.T) {
	env := newFakeEnvForKind(t, "claude")
	env.existingFiles = map[string]bool{}

	report := Run(context.Background(), env, []string{"claude"},
		WithDefinitions(map[string][]string{"opencode": {"plan-executor"}}))

	for _, name := range []string{"plan-executor", "researcher", "reviewer"} {
		if findCheck(report, "claude", name) == nil {
			t.Errorf("no %s row for claude; a kind absent from the map must keep every shipped definition", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail** --
`go test -count=1 ./internal/doctor -run 'TestDoctorChecksOnlyTheDefinitionsGiven|TestDoctorKindAbsentFromDefinitions'`:
compile error, `WithDefinitions` undefined.

- [ ] **Step 3: Implement.** In `runConfig` add `definitions map[string][]string`.
After `WithAdopted`:

```go
// WithDefinitions limits the role rows for each kind to the named
// definitions: the ones some candidate on that harness would actually
// load (#166). A kind absent from the map keeps every shipped
// definition, which is what a caller with no candidate knowledge wants.
func WithDefinitions(defs map[string][]string) RunOption {
	return func(cfg *runConfig) {
		cfg.definitions = defs
	}
}

// wants reports whether the role row for definition name on kind is in
// scope under cfg.definitions.
func (cfg runConfig) wants(kind, name string) bool {
	defs, limited := cfg.definitions[kind]
	if !limited {
		return true
	}
	for _, d := range defs {
		if d == name {
			return true
		}
	}
	return false
}
```

In `Run`'s role loop (`default:` branch of the `switch` under `if !cfg.adopted`):

```go
			default:
				for _, r := range h.Roles {
					if !cfg.wants(kind, r.Name) {
						continue
					}
					checks = append(checks, roleCheck(env, kind, r))
				}
```

Update the comment above the switch from "one row per shipped role" to
"one row per shipped role in scope (see WithDefinitions)".

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/doctor`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat(doctor): WithDefinitions scopes role rows to what a candidate would load (#166)"
```

---

### Task 3: the missing-role fix line creates its directory

**Files:**
- Modify: `internal/doctor/doctor.go` (`roleCheck`, the `env.Stat(fullPath) != nil` branch)
- Modify: `internal/doctor/doctor_test.go` (`TestDoctorMissingRoleFileNamesTheRoleInTheFix`)

**Interfaces:**
- The missing-file `Fix` becomes exactly
  `mkdir -p <dir> && relay agent print --kind <kind> --role <role> > <homeRel>`
  where `<dir>` is `path.Dir(homeRel)` (`path`, not `filepath`: `r.Path`
  is a forward-slash home-relative path on every platform relay supports).

- [ ] **Step 1: Tighten the test.** In `TestDoctorMissingRoleFileNamesTheRoleInTheFix`
replace the `strings.Contains(c.Fix, "--role researcher")` assertion with:

```go
	want := "mkdir -p ~/.claude/agents && relay agent print --kind claude --role researcher > ~/.claude/agents/researcher.md"
	if c.Fix != want {
		t.Errorf("fix = %q, want %q", c.Fix, want)
	}
```

- [ ] **Step 2: Run to verify it fails** --
`go test -count=1 ./internal/doctor -run TestDoctorMissingRoleFileNamesTheRoleInTheFix`.
Expected: FAIL, fix lacks the `mkdir -p` prefix.

- [ ] **Step 3: Implement.** In `roleCheck`, the missing branch:

```go
	if env.Stat(fullPath) != nil {
		// The fix must work on a machine that has never run this harness
		// as a sub-agent host: none of the agents/ directories exist yet
		// (#166 §1), and a redirect into a missing directory fails.
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix: fmt.Sprintf("mkdir -p %s && relay agent print --kind %s --role %s > %s",
				path.Dir(homeRel), kind, r.Name, homeRel),
		}
	}
```

Add `"path"` to the imports. Leave the drift fix (further down, same
function) as it is: the directory exists when the file does.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/doctor ./cmd/relay`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "fix(doctor): the missing-role fix creates the agents directory first (#166)"
```

---

### Task 4: `cmd/relay` computes the per-kind definitions; bind's preflight checks builder's

**Files:**
- Modify: `cmd/relay/doctor.go` (new `assembleDefinitions`, new `builderDefinitions`; `cmdDoctor` and `bindPreflight` pass `WithDefinitions`)
- Modify: `cmd/relay/doctor_test.go`

**Interfaces:**
- Produces: `func assembleDefinitions(set *candidate.Set, kinds []string) map[string][]string`.
  For every candidate in `set`, for every role it serves,
  `harness.RoleByName(role).Definitions` is unioned into
  `out[c.Harness]`. Every kind in `kinds` with no entry afterwards (a kind
  known only through a binding) gets `builderDefinitions()`. Each list is
  deduplicated and sorted. A nil `set` is fine.
- Produces: `func builderDefinitions() []string` returning a fresh copy of
  `harness.RoleByName("builder").Definitions`.
- `cmdDoctor` calls `doctor.Run(ctx, env, kinds, doctor.WithDefinitions(assembleDefinitions(rt.Candidates, kinds)))`.
- `bindPreflight` calls `doctor.Run(ctx, env, []string{kind}, doctor.WithAdopted(adopted), doctor.WithDefinitions(map[string][]string{kind: builderDefinitions()}))`.

- [ ] **Step 1: Write the failing tests** in `cmd/relay/doctor_test.go`:

```go
func TestAssembleDefinitionsFollowsCandidateRoles(t *testing.T) {
	set := testSet(t, `[
	  {"harness":"agy","provider":"t","model":"m","roles":["builder"]},
	  {"harness":"claude","provider":"t","model":"m","roles":["reviewer"]},
	  {"harness":"claude","provider":"t","model":"n","roles":["builder"]}
	]`)

	got := assembleDefinitions(set, []string{"agy", "claude", "opencode"})

	want := map[string][]string{
		"agy":      {"plan-executor", "researcher"},
		"claude":   {"plan-executor", "researcher", "reviewer"},
		"opencode": {"plan-executor", "researcher"}, // binding-only kind: builder's set
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions = %v, want %v", got, want)
	}
}

func TestAssembleDefinitionsNilSet(t *testing.T) {
	got := assembleDefinitions(nil, []string{"claude"})
	want := map[string][]string{"claude": {"plan-executor", "researcher"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions(nil) = %v, want %v", got, want)
	}
}
```

And a preflight test beside the existing `TestBindPreflight*` tests, using
the file's `stubDoctorEnv` (give it no role files so every checked
definition is a warning):

```go
func TestBindPreflightChecksOnlyBuilderDefinitions(t *testing.T) {
	env := &stubDoctorEnv{
		ver:       herdr.MinVersion,
		daemonRun: true,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
		intStates: map[string]herdr.IntegrationState{"claude": {Installed: true, Detail: "current"}},
		statErr:   os.ErrNotExist, // no role file exists
	}
	lines := bindPreflight(context.Background(), env, "claude", false)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "plan-executor") || !strings.Contains(joined, "researcher") {
		t.Errorf("preflight must warn about the builder's definitions, got:\n%s", joined)
	}
	if strings.Contains(joined, "reviewer") {
		t.Errorf("preflight must not warn about reviewer on a bind, got:\n%s", joined)
	}
}
```

`stubDoctorEnv` is defined further down in the same file; `statErr` makes
every `Stat` fail, so each checked definition becomes a warning line.

- [ ] **Step 2: Run to verify they fail** --
`go test -count=1 ./cmd/relay -run 'TestAssembleDefinitions|TestBindPreflightChecksOnlyBuilderDefinitions'`:
compile error, `assembleDefinitions` undefined.

- [ ] **Step 3: Implement** in `cmd/relay/doctor.go`, after `assembleKinds`:

```go
// builderDefinitions is what a builder needs installed on its harness:
// the plan-executor and the researcher it dispatches to (#166 §3).
func builderDefinitions() []string {
	spec, _ := harness.RoleByName("builder")
	return append([]string(nil), spec.Definitions...)
}

// assembleDefinitions is doctor's per-kind role scope: the definitions
// some candidate on that harness would load, given its roles. A kind in
// kinds that no candidate names reached doctor through a binding, and a
// binding is always a builder.
func assembleDefinitions(set *candidate.Set, kinds []string) map[string][]string {
	seen := make(map[string]map[string]bool)
	add := func(kind string, defs []string) {
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		for _, d := range defs {
			seen[kind][d] = true
		}
	}
	if set != nil {
		for _, ref := range set.Refs() {
			parsed, _ := candidate.ParseRef(ref)
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			for _, role := range c.Roles {
				if spec, ok := harness.RoleByName(role); ok {
					add(c.Harness, spec.Definitions)
				}
			}
		}
	}
	for _, kind := range kinds {
		if seen[kind] == nil {
			add(kind, builderDefinitions())
		}
	}
	out := make(map[string][]string, len(seen))
	for kind, defs := range seen {
		list := make([]string, 0, len(defs))
		for d := range defs {
			list = append(list, d)
		}
		sort.Strings(list)
		out[kind] = list
	}
	return out
}
```

Import `github.com/fuad-daoud/relay/internal/harness`. In `cmdDoctor`
change the `doctor.Run` call to pass
`doctor.WithDefinitions(assembleDefinitions(rt.Candidates, kinds))`. In
`bindPreflight` add
`doctor.WithDefinitions(map[string][]string{kind: builderDefinitions()})`
to its `doctor.Run` call.

- [ ] **Step 4: Run** `go test -race -count=1 ./cmd/relay ./internal/doctor`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/relay/doctor.go cmd/relay/doctor_test.go
git commit -m "feat(relay): doctor and bind check only the definitions a candidate would load (#166)"
```

---

### Task 5: `relay.RoleRefusals`; `FormatPolicy` prints refusals through it

**Files:**
- Modify: `internal/relay/policy_view.go` (new `RoleRefusal`, `RoleRefusals`, `refusalFromErr`; `FormatPolicy`'s `switch` on `err` replaced)
- Modify: `internal/relay/policy_view_test.go`

**Interfaces:**
- Produces:

```go
// RoleRefusal is one role for which an omitted candidate would be refused
// right now, computed by the same resolveCandidate call bind makes.
type RoleRefusal struct {
	Role    string
	Text    string   // "3 candidates serve builder and no order is set" | "every candidate serving builder is gated"
	NoOrder bool     // true for the ambiguous case, false for the all-gated case
	Serving []string // tokens serving the role, in Set.ForRole order
	Gated   []string // providers with a gate on a serving token, deduplicated, in Serving order; nil when NoOrder
}

// RoleRefusals lists, in harness.RoleNames() order, every role with at
// least one serving candidate that resolveCandidate would refuse with no
// token named. It is the one source of doctor's policy rows and relay
// policy's "would refuse" lines.
func RoleRefusals(set *candidate.Set, pol policy.Policy, gates []ledger.Gate) []RoleRefusal
```

- Unexported `refusalFromErr(role string, serving []candidate.Candidate, gates []ledger.Gate, err error) (RoleRefusal, bool)`:
  `errors.Is(err, ErrAmbiguousCandidate)` → NoOrder refusal;
  `errors.Is(err, ErrAllGated)` → gated refusal with `Gated` built from
  serving tokens where `len(skipsFor(gates, tok)) > 0`; any other `err`
  (including nil) → `false`.
- `FormatPolicy`'s `switch { case errors.Is(err, ErrAmbiguousCandidate): ...; case errors.Is(err, ErrAllGated): ... }`
  becomes `if r, ok := refusalFromErr(role, serving, gates, err); ok { sb.WriteString("  would refuse: " + r.Text + "\n") }`.
  Output is byte-identical.

- [ ] **Step 1: Write the failing tests** in `internal/relay/policy_view_test.go`:

```go
func TestRoleRefusalsNoOrder(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)

	got := RoleRefusals(set, policy.Policy{}, nil)

	want := []RoleRefusal{{
		Role:    "builder",
		Text:    "3 candidates serve builder and no order is set",
		NoOrder: true,
		Serving: []string{testAgyRef, testClaudeRef, testOpencodeRef},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoleRefusals = %+v, want %+v", got, want)
	}
}

func TestRoleRefusalsNoneWhenOrderedOrSole(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)

	if got := RoleRefusals(set, pol, nil); len(got) != 0 {
		t.Errorf("RoleRefusals with order = %+v, want none (reviewer is sole, researcher unserved)", got)
	}
}

func TestRoleRefusalsAllGated(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testAgyRef, testClaudeRef, testOpencodeRef)
	gates := []ledger.Gate{limit(testAgyRef), limit(testClaudeRef), limit(testOpencodeRef)}

	got := RoleRefusals(set, pol, gates)

	want := []RoleRefusal{
		{Role: "builder", Text: "every candidate serving builder is gated",
			Serving: []string{testAgyRef, testClaudeRef, testOpencodeRef}, Gated: []string{"test"}},
		{Role: "reviewer", Text: "every candidate serving reviewer is gated",
			Serving: []string{testClaudeRef}, Gated: []string{"test"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoleRefusals = %+v, want %+v", got, want)
	}
}

func TestRoleRefusalsEmptySet(t *testing.T) {
	if got := RoleRefusals(nil, policy.Policy{}, nil); got != nil {
		t.Errorf("RoleRefusals(nil) = %+v, want nil", got)
	}
}
```

`testClaudeRef` and `testOpencodeRef` are defined beside `testAgyRef` in
`candidate_test.go`; every provider in `testCandidatesJSON` is `test`, hence
`Gated: []string{"test"}`. Add `reflect` to the imports.

- [ ] **Step 2: Run to verify they fail** --
`go test -count=1 ./internal/relay -run TestRoleRefusals`: compile error.

- [ ] **Step 3: Implement** in `policy_view.go`, after `PolicyWarnings`:

```go
// RoleRefusal is one role for which an omitted candidate would be refused
// right now, computed by the same resolveCandidate call bind makes.
type RoleRefusal struct {
	Role    string
	Text    string   // the words after "would refuse:" in relay policy
	NoOrder bool     // true for the ambiguous case, false for the all-gated case
	Serving []string // tokens serving the role, in Set.ForRole order
	Gated   []string // providers with a gate on a serving token, deduplicated, in Serving order; nil when NoOrder
}

// RoleRefusals lists, in harness.RoleNames() order, every role with at
// least one serving candidate that resolveCandidate would refuse with no
// token named. It is the one source of doctor's policy rows and relay
// policy's "would refuse" lines (#165).
func RoleRefusals(set *candidate.Set, pol policy.Policy, gates []ledger.Gate) []RoleRefusal {
	if set == nil || set.Len() == 0 {
		return nil
	}
	var out []RoleRefusal
	for _, role := range harness.RoleNames() {
		serving := set.ForRole(role)
		if len(serving) == 0 {
			continue
		}
		_, err := resolveCandidate(set, pol, gates, "", role)
		if r, ok := refusalFromErr(role, serving, gates, err); ok {
			out = append(out, r)
		}
	}
	return out
}

// refusalFromErr maps resolveCandidate's error for role to a RoleRefusal.
// FormatPolicy and RoleRefusals both go through here, so relay policy and
// relay doctor print the same words for the same state.
func refusalFromErr(role string, serving []candidate.Candidate, gates []ledger.Gate, err error) (RoleRefusal, bool) {
	tokens := make([]string, 0, len(serving))
	for _, c := range serving {
		tokens = append(tokens, c.Ref().String())
	}
	switch {
	case errors.Is(err, ErrAmbiguousCandidate):
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("%d candidates serve %s and no order is set", len(serving), role),
			NoOrder: true,
			Serving: tokens,
		}, true
	case errors.Is(err, ErrAllGated):
		var providers []string
		for _, c := range serving {
			if len(skipsFor(gates, c.Ref().String())) > 0 {
				providers = append(providers, c.Ref().Provider)
			}
		}
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("every candidate serving %s is gated", role),
			Serving: tokens,
			Gated:   uniqStrings(providers),
		}, true
	}
	return RoleRefusal{}, false
}
```

`uniqStrings` (`candidate.go`) preserves first-seen order and returns nil
for empty input, which is what the test's `Gated: nil` on a `NoOrder`
refusal relies on -- `Gated` is simply not set in that branch.

Then replace the `switch` in `FormatPolicy` as the Interfaces block says.

- [ ] **Step 4: Run** `go test -race -count=1 ./internal/relay -run 'TestRoleRefusals|TestFormatPolicy|TestPolicyWarnings'`. Expected: PASS, and the two pinned `FormatPolicy` refusal tests are untouched and green.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/policy_view.go internal/relay/policy_view_test.go
git commit -m "feat(relay): RoleRefusals is the one source of would-refuse text (#165)"
```

---

### Task 6: doctor's policy rows, candidates example, and the footer precedence

**Files:**
- Modify: `internal/doctor/doctor.go` (`Report`: two new fields)
- Modify: `cmd/relay/doctor.go` (`cmdDoctor`, new `refusalChecks`, new `policyExample`, `renderReport` footer, the `candidates` row fix)
- Modify: `cmd/relay/doctor_test.go`

**Interfaces:**
- `doctor.Report` gains, after `UsableBuilder`:

```go
	// NoCandidates and BuilderRefusal are verdict inputs the caller sets
	// from configuration Run does not see (candidates.json, policy.json,
	// the ledger). Run leaves them zero. The footer reads them in the
	// order failures, NoCandidates, !UsableBuilder, BuilderRefusal.
	NoCandidates   bool
	BuilderRefusal string // RoleRefusal.Text for builder, "" when bind would pick
```

- Produces `func refusalChecks(refusals []relay.RoleRefusal) []doctor.Check`:
  one global `policy` warn row per refusal. Detail: `r.Text + " -- add/bind without --builder would refuse"`
  when `r.Role == "builder"`, else `r.Text + " -- ask --role " + r.Role + " without --candidate would refuse"`.
  Fix: `NoOrder` → `"write ~/.config/relay/policy.json, e.g. " + policyExample(r.Role, r.Serving)`;
  gated → `"relay available " + r.Gated[0]` (a gated refusal always has at
  least one gated provider; guard with `len(r.Gated) > 0` and fall back to
  `"relay available <provider>"` literally rather than panic).
- Produces `func policyExample(role string, serving []string) string`:
  `json.Marshal(map[string]map[string][]string{"order": {role: serving}})`
  → `{"order":{"builder":["agy/test/m","claude/test/m"]}}`.
- `cmdDoctor`: after the existing `policyChecks` append,
  `refusals := relay.RoleRefusals(rt.Candidates, rt.Policy, relay.Gates(rt))`;
  `rep.Checks = append(rep.Checks, refusalChecks(refusals)...)`; for the
  first refusal with `Role == "builder"`, `rep.BuilderRefusal = r.Text`.
  In the existing `rt.Candidates.Len() == 0` block also set
  `rep.NoCandidates = true` and change the row's fix to
  `write ~/.config/relay/candidates.json, e.g. [{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`.
- `renderReport` footer, `failCount == 0` branch, in this order:
  - `rep.NoCandidates` → `"%s, %s -- no candidates configured; write ~/.config/relay/candidates.json first.\n"`
  - `!rep.UsableBuilder` → existing text, unchanged
  - `rep.BuilderRefusal != ""` → `"%s, %s -- relay cannot pick a builder: %s.\n"` with the refusal text
  - else → existing `relay can run.`

- [ ] **Step 1: Write the failing tests** in `cmd/relay/doctor_test.go`:

```go
func TestRefusalChecks(t *testing.T) {
	refusals := []relay.RoleRefusal{
		{Role: "builder", Text: "3 candidates serve builder and no order is set", NoOrder: true,
			Serving: []string{"agy/test/m", "claude/test/m", "opencode/test/m"}},
		{Role: "reviewer", Text: "every candidate serving reviewer is gated",
			Serving: []string{"claude/test/m"}, Gated: []string{"test"}},
	}

	checks := refusalChecks(refusals)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}
	for i, c := range checks {
		if c.Group != "" || c.Name != "policy" || c.Severity != doctor.SevWarn {
			t.Errorf("check %d = %+v", i, c)
		}
	}
	if want := "3 candidates serve builder and no order is set -- add/bind without --builder would refuse"; checks[0].Detail != want {
		t.Errorf("builder Detail = %q, want %q", checks[0].Detail, want)
	}
	if want := `write ~/.config/relay/policy.json, e.g. {"order":{"builder":["agy/test/m","claude/test/m","opencode/test/m"]}}`; checks[0].Fix != want {
		t.Errorf("builder Fix = %q, want %q", checks[0].Fix, want)
	}
	if want := "every candidate serving reviewer is gated -- ask --role reviewer without --candidate would refuse"; checks[1].Detail != want {
		t.Errorf("reviewer Detail = %q, want %q", checks[1].Detail, want)
	}
	if want := "relay available test"; checks[1].Fix != want {
		t.Errorf("reviewer Fix = %q, want %q", checks[1].Fix, want)
	}
	if got := refusalChecks(nil); len(got) != 0 {
		t.Errorf("refusalChecks(nil) = %+v, want empty", got)
	}
}

func TestRenderReportFooterPrecedence(t *testing.T) {
	healthy := []doctor.Check{
		{Name: "herdr", Severity: doctor.SevOK, Detail: "0.9.0 (floor 0.8.2)"},
		{Name: "daemon", Severity: doctor.SevOK, Detail: "running"},
		{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
		{Group: "claude", Name: "integration", Severity: doctor.SevOK, Detail: "current"},
	}
	cases := []struct {
		name string
		rep  doctor.Report
		want string
	}{
		{"refusal beats can run",
			doctor.Report{Checks: healthy, UsableBuilder: true, BuilderRefusal: "3 candidates serve builder and no order is set"},
			"0 warnings, 0 failures -- relay cannot pick a builder: 3 candidates serve builder and no order is set."},
		{"no usable builder beats refusal",
			doctor.Report{Checks: healthy, UsableBuilder: false, BuilderRefusal: "3 candidates serve builder and no order is set"},
			"could not establish a usable builder"},
		{"no candidates beats no usable builder",
			doctor.Report{Checks: healthy, UsableBuilder: false, NoCandidates: true},
			"0 warnings, 0 failures -- no candidates configured; write ~/.config/relay/candidates.json first."},
		{"a failure beats everything",
			doctor.Report{Checks: append(healthy, doctor.Check{Name: "herdr", Severity: doctor.SevFail, Detail: "x"}), NoCandidates: true, BuilderRefusal: "y"},
			"Fix the failure above."},
		{"clean machine can run",
			doctor.Report{Checks: healthy, UsableBuilder: true},
			"0 warnings, 0 failures -- relay can run."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderReport(&buf, tc.rep)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("footer missing %q in:\n%s", tc.want, buf.String())
			}
		})
	}
}

func TestPolicyExample(t *testing.T) {
	got := policyExample("builder", []string{"agy/test/m", "claude/test/m"})
	want := `{"order":{"builder":["agy/test/m","claude/test/m"]}}`
	if got != want {
		t.Errorf("policyExample = %s, want %s", got, want)
	}
}
```

In the "a failure beats everything" case, copy `healthy` before appending
if the shared slice's capacity makes the append alias (build it with
`append([]doctor.Check(nil), healthy...)` first).

- [ ] **Step 2: Run to verify they fail** --
`go test -count=1 ./cmd/relay -run 'TestRefusalChecks|TestRenderReportFooterPrecedence|TestPolicyExample'`:
compile error.

- [ ] **Step 3: Implement** per the Interfaces block. `refusalChecks` goes
next to `policyChecks` with this comment:

```go
// refusalChecks turns the roles an omitted candidate would be refused for
// into doctor rows. Warnings, not failures: the machine is fine, the
// configuration is not (#165). The fix is a literal policy.json built
// from the tokens that serve the role, so it can be pasted as is.
```

`policyExample` uses `encoding/json`; `json.Marshal` cannot fail on a map
of strings, so ignore its error with a comment saying why.

- [ ] **Step 4: Run** `go test -race -count=1 ./cmd/relay ./internal/doctor`. Expected: PASS, including the existing `TestRenderReportVerdict`.

- [ ] **Step 5: Mutation check, then revert.** Temporarily delete the
`rep.BuilderRefusal != ""` branch in `renderReport` and run
`go test -count=1 ./cmd/relay -run TestRenderReportFooterPrecedence`.
Expected: FAIL on "refusal beats can run". Restore the branch and confirm
PASS. Say in the report that this was done.

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/doctor.go cmd/relay/doctor.go cmd/relay/doctor_test.go
git commit -m "feat(relay): doctor warns when bind would refuse and stops blaming harnesses for no candidates (#165, #166)"
```

---

### Task 7: README first-run steps

**Files:**
- Modify: `README.md` ("First run on a clean machine" steps 4-7; "Policy" paragraph)

**Interfaces:** none (prose).

- [ ] **Step 1: Edit step 4.** Prefix each harness's block with one
`mkdir -p` line and keep the redirects:

```
   # agy
   mkdir -p ~/.gemini/config/agents
   relay agent print --kind agy --role plan-executor > ~/.gemini/config/agents/plan-executor.md
   relay agent print --kind agy --role researcher    > ~/.gemini/config/agents/researcher.md

   # claude
   mkdir -p ~/.claude/agents
   relay agent print --kind claude --role plan-executor > ~/.claude/agents/plan-executor.md
   relay agent print --kind claude --role researcher    > ~/.claude/agents/researcher.md

   # opencode
   mkdir -p ~/.config/opencode/agents
   relay agent print --kind opencode --role plan-executor > ~/.config/opencode/agents/plan-executor.md
   relay agent print --kind opencode --role researcher    > ~/.config/opencode/agents/researcher.md
```

- [ ] **Step 2: Insert a policy step** after step 5 (candidates) and
renumber the rest (doctor becomes 7, daemon 8):

```
6. If more than one candidate serves `builder`, write `~/.config/relay/policy.json`
   with the order to try them in (see [Policy](#policy)); `relay policy` shows
   what relay would pick and says `would refuse` until you do. `relay doctor`
   warns about this too and prints a starter file built from your candidates.
```

- [ ] **Step 3: Edit the Policy paragraph.** The sentence ending
"`relay policy` and `relay doctor` warn about it." gets a sentence after it:
"Both also say when several candidates serve a role and no order is set, since
an omitted `--builder` refuses in that state."

- [ ] **Step 4: Verify** the `relay doctor` step still says `0 failures`
and nothing in the README references the old step numbers (grep for
`step 6`, `step 7`; there should be none). Run `gofmt -l .` is irrelevant
here; nothing else to run.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: first run creates the agents directories and writes policy.json (#165, #166)"
```

---

### Task 8: `plugin-build.sh` stamps a version a human can place

**Files:**
- Modify: `scripts/plugin-build.sh` (the `version=` line)
- Modify: `scripts/plugin-build_test.sh` (two new cases)

**Interfaces:**
- `version` resolution per §E. The build's final echo stays
  `plugin-build: installed relay $version`.

- [ ] **Step 1: Add the failing test cases** to `scripts/plugin-build_test.sh`,
before the final `[ "$fail" -eq 0 ]` line. Both clone the repo without tags
the way herdr's plugin clone arrives:

```sh
# 3. A tagless clone with a reachable remote fetches tags and describes.
git clone -q --no-tags "$root" "$work/withremote"
mkdir -p "$work/withremote/from-source"
if (cd "$work/withremote/from-source" && sh ../scripts/plugin-build.sh >/dev/null 2>&1); then
	v=$("$work/withremote/from-source/relay" version); v=${v#relay }
	case "$v" in
		v[0-9]*) ;;
		*) echo "FAIL: tagless clone with remote printed '$v', want a tag-relative describe"; fail=1 ;;
	esac
else
	echo "FAIL: build in tagless clone with remote exited non-zero"; fail=1
fi

# 4. No tags and no remote: manifest version plus the short hash, never a bare hash.
git clone -q --no-tags "$root" "$work/noremote"
(cd "$work/noremote" && git remote remove origin)
mkdir -p "$work/noremote/from-source"
if (cd "$work/noremote/from-source" && sh ../scripts/plugin-build.sh >/dev/null 2>&1); then
	v=$("$work/noremote/from-source/relay" version); v=${v#relay }
	case "$v" in
		[0-9]*.[0-9]*.[0-9]*+[0-9a-f]*) ;;
		*) echo "FAIL: tagless clone without remote printed '$v', want <manifest>+<hash>"; fail=1 ;;
	esac
else
	echo "FAIL: build in tagless clone without remote exited non-zero"; fail=1
fi
```

`relay version` prints `relay <version>`, so strip the prefix before each
`case`: `v=${v#relay }`.

- [ ] **Step 2: Run to verify it fails** -- `sh scripts/plugin-build_test.sh`.
Expected: case 3 or 4 FAILs (today `--always` yields a bare hash in both).

- [ ] **Step 3: Implement.** Replace the `version=$(git describe ...)` line with:

```sh
# Stamp a version a human can place against a release. herdr's plugin
# clone has no tags, so a bare `git describe --always` printed a lone hash
# (#166 §4): fetch tags when none are reachable, and if there are still
# none, fall back to the manifest's version plus the hash.
if ! version=$(git describe --tags --dirty 2>/dev/null); then
	git fetch --tags --quiet 2>/dev/null || true
	if ! version=$(git describe --tags --dirty 2>/dev/null); then
		if hash=$(git rev-parse --short HEAD 2>/dev/null); then
			manifest=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$plugin_root/herdr-plugin.toml" 2>/dev/null)
			version="${manifest:-devel}+$hash"
		else
			version=devel
		fi
	fi
fi
```

This runs after `cd "$repo_root"`, and `$plugin_root` is the from-source
directory, whose `herdr-plugin.toml` carries the same `version` as the
root manifest (`scripts/check-plugin-version.sh` enforces that).

- [ ] **Step 4: Run** `sh scripts/plugin-build_test.sh` and `shellcheck scripts/plugin-build.sh scripts/plugin-build_test.sh`. Expected: `plugin-build: ok`, no shellcheck findings.

- [ ] **Step 5: Commit**

```bash
git add scripts/plugin-build.sh scripts/plugin-build_test.sh
git commit -m "fix(plugin): from-source build stamps a tag-relative or manifest+hash version (#166)"
```

---

### Task 9: full verification and the report

- [ ] **Step 1: Run the whole `make check` set** from "Running commands",
in order. Every line must pass. Paste the last line of each into the
report.

- [ ] **Step 2: Scope check.** `git diff --stat main..HEAD` must list only:
`internal/harness/harness.go`, `internal/harness/harness_test.go`,
`internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`,
`cmd/relay/doctor.go`, `cmd/relay/doctor_test.go`,
`internal/relay/policy_view.go`, `internal/relay/policy_view_test.go`,
`README.md`, `scripts/plugin-build.sh`, `scripts/plugin-build_test.sh`,
and `docs/plans/2026-09-16-doctor-first-run.md` (this plan; the planner
committed it on the branch before the round started, so it is already
there). Anything else is a plan violation: say so in the report rather than
reverting silently.

- [ ] **Step 3: Write the report** to the drop directory as relay's
prompt named it, with: the commit list (`git log --oneline main..HEAD`),
the check output tails, the Task 6 Step 5 mutation result, and any step
where you stopped. Then create the completion marker as the prompt named
it.
