# agy roles T1: the three agy definitions, `--agent` launch, and the end of the preamble (#85)

**Design spec:** `docs/specs/2026-09-11-agy-role-definitions-design.md` -- read §1, §3.1–3.6, §4.1–4.3, §4.7–4.9, §7, §8
**Issue:** #85
**Depends on:** nothing beyond `main` as of #86.

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
- This task touches `internal/harness`, `internal/store`, `internal/relay`
  and their tests. It does **not** touch `internal/doctor`, `cmd/relay`,
  `README.md` or `docs/specs` -- those are T2 and T3. If a change here
  breaks a build or test in `internal/doctor` or `cmd/relay`, fix the
  minimum that restores compilation and say exactly what in your report;
  do not redesign anything there.
- The agy definitions pin `model: inherit`, on all three, without
  exception (spec §7.2).
- The one-writer sentence `Exactly one agent writes to this working tree,
  and it is you.` appears verbatim in `plan-executor.agy.md`.
- Every exported symbol you add or change gets a doc comment explaining
  the rationale, in the house style (read `internal/harness/harness.go`
  for the tone).
- Commit when every step is done. Do not push, and do not open a PR.
- You are already in your own worktree on your own branch. Do not create
  another branch and do not switch branches.

---

## Task: agy definitions, table rows, `--agent` launch, preamble removal

**Files:**
- Create: `internal/harness/agents/plan-executor.agy.md`, `internal/harness/agents/researcher.agy.md`, `internal/harness/agents/reviewer.agy.md`
- Modify: `internal/harness/harness.go`, `internal/harness/harness_test.go`, `internal/harness/harness_role_test.go`, `internal/harness/agents_test.go`
- Modify: `internal/store/types.go`, `internal/store/store_test.go`
- Modify: `internal/relay/send.go`, `internal/relay/send_test.go`, `internal/relay/ask.go`, `internal/relay/ask_test.go`, `internal/relay/bind.go`, `internal/relay/bind_test.go`

**Interfaces produced** (spec §3.1–3.4), by exactly these names -- T2 and
T3 build on them:

```go
// internal/harness
type RoleSpec struct {
    Name       string
    Shape      RoleShape
    Definition string
}                                   // Preamble is gone

type Role struct {
    Name        string
    Path        string
    Doc         string
    ExpectModel string              // "" = any pin; "inherit" on every agy row
}

type Harness struct {
    Kind        string
    Binary      string
    Integration string
    Roles       []Role              // never empty for a known kind
    MinVersion  string              // "1.1.6" for agy, "" otherwise
}                                   // SelectsRoleByPreamble is gone

type Launch struct {
    Kind string
    Args []string
}                                   // Preamble is gone

// internal/store
// Binding.PreamblePending is gone; the JSON key preamble_pending is ignored on load.
```

- [ ] **Step 1: the three definition files** (spec §3.6, §7.3, §7.4)

  Create `internal/harness/agents/researcher.agy.md`. Frontmatter exactly:

  ```
  ---
  name: researcher
  description: Read-only investigator dispatched by plan-executor to locate code, trace conventions, and answer questions about an existing codebase. Never edits anything.
  mainAgent: true
  subagent: true
  model: inherit
  commandExecutionPolicy: sandbox
  tools:
    - view_file
    - view_file_outline
    - view_code_item
    - grep_search
    - find_by_name
    - list_dir
    - run_command
    - command_status
    - read_terminal
  ---
  ```

  Body: copy the body of `researcher.claude.md` (everything after its
  closing `---`) with these substitutions, and no others:
  - Each SHOUTING-CAPS section label on its own line (`READ-ONLY, WITHOUT
    EXCEPTION`, `WHAT A GOOD REPORT LOOKS LIKE`, `WHEN YOU DO NOT FIND
    SOMETHING`, `CHOOSING THE MODEL`) becomes an H1: `# Read-only, without
    exception`, `# What a good report looks like`, `# When you do not find
    something`, `# The model line`. agy delimits system-prompt sections by
    H1, so the labels must be headings.
  - The opening paragraph ("You are a Researcher. …") gets an H1 above it:
    `# System Prompt`.
  - Replace the whole "CHOOSING THE MODEL" paragraph with:

    > `model: inherit` above is not an example, it is required. On agy the
    > `model` key is a tier (`inherit`, `flash`, `pro`) and a tier pinned
    > here overrides the `--model` relay passes on the launch line -- so a
    > pin other than `inherit` would run a model `relay status` does not
    > show. `relay doctor` warns when an installed copy pins anything else.
  - Append one final section, after the model section:

    > `# Why the tools list is short`
    >
    > The `tools:` allowlist above is every read-only tool agy exposes and
    > none of the writing ones. On this harness read-only is not a request
    > to you, it is a refusal by the harness: a write tool is not offered.
    > `run_command` is present under `commandExecutionPolicy: sandbox` so
    > `git diff` and `git log` work; a command that writes to the tree is
    > refused by the sandbox.

  Create `internal/harness/agents/reviewer.agy.md` the same way from
  `reviewer.claude.md`: identical frontmatter to the researcher's except
  `name: reviewer` and the `description:` line copied from
  `reviewer.claude.md`. Section labels become `# System Prompt` (above the
  opening paragraph), `# What a good findings file looks like`, `# Do not
  dispatch sub-agents`, `# Not the researcher role`, `# The model line`,
  and the same appended `# Why the tools list is short` section. The
  "CHOOSING THE MODEL" paragraph is replaced by the same `inherit` text as
  above.

  Create `internal/harness/agents/plan-executor.agy.md`. Frontmatter:

  ```
  ---
  name: plan-executor
  description: >-
    <the description block copied verbatim from plan-executor.claude.md,
     including its <example> blocks, same indentation>
  mainAgent: true
  subagent: false
  model: inherit
  commandExecutionPolicy: auto
  ---
  ```

  No `tools:` key: plan-executor is the one writer and keeps agy's default
  tool set. Body from `plan-executor.claude.md` with:
  - `# System Prompt` above the opening "You are a Plan Execution
    Specialist" paragraph; each caps label becomes an H1 in sentence case
    (`# Core mandate -- exact execution`, `# Workflow`, `# Research
    sub-agent prompting standards`, `# Handling problems without
    deviating`, `# Quality controls`, `# Output format`).
  - The sentence at claude line ~108–109, "Dispatch every research
    sub-agent as the `researcher` role -- pass subagent_type: researcher
    to the Agent tool." becomes "Dispatch every research sub-agent as the
    `researcher` agent -- call `invoke_subagent` with agent `researcher`."
    Keep the rest of that numbered item.
  - Append, as the last section:

    > `# Why this agent cannot be a sub-agent`
    >
    > `subagent: false` above means no agent can invoke a plan-executor
    > with `invoke_subagent`. A plan-executor dispatched by another
    > plan-executor is a second writer in one tree; relay forbids that in
    > prose on every harness and by configuration on this one.

  The one-writer sentence `Exactly one agent writes to this working tree,
  and it is you.` must survive the copy verbatim -- grep for it before
  moving on.

- [ ] **Step 2: table rows and `Role.ExpectModel`, `Harness.MinVersion`** (spec §3.2, §3.3)

  In `internal/harness/harness.go`:

  Add to `Role`:

  ```go
  // ExpectModel is the only model pin doctor accepts in an installed copy
  // without warning; "" means any pin is fine. It is "inherit" on every
  // agy row because agy's model key is a tier that would override the
  // --model relay passes on the launch line (spec §7.2).
  ExpectModel string
  ```

  Add to `Harness`, and delete `SelectsRoleByPreamble` and its comment:

  ```go
  // MinVersion is the semver floor doctor holds the binary to; "" means
  // unchecked. agy's floor is the release that added Markdown agent
  // definitions, without which --agent has nothing to select.
  MinVersion string
  ```

  Rewrite the `Roles` comment: "Roles are the definitions relay ships for
  this kind, ordered with plan-executor first so doctor reports the role
  relay's loop depends on before the rest. Never empty for a known kind:
  every kind relay runs selects its role with --agent."

  Replace the agy entry:

  ```go
  "agy": {
      Kind:        "agy",
      Binary:      "agy",
      Integration: "antigravity-cli",
      MinVersion:  "1.1.6",
      Roles: []Role{
          {Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
          {Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
          {Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
      },
  },
  ```

  In `CanServe`, delete the `if h.SelectsRoleByPreamble { return true }`
  branch. The remaining body is the table lookup.

  Tests:
  - `harness_test.go` `TestTableExactValues`: the agy expected value
    becomes the entry above. claude and opencode entries gain nothing
    (`ExpectModel` and `MinVersion` are zero).
  - `harness_test.go` `TestCanServe`: delete the two
    `SelectsRoleByPreamble` assertion blocks after the matrix loop. The
    matrix itself is unchanged -- agy still serves all three roles.
  - `harness_role_test.go`: delete `TestAgySelectsItsRoleByPreamble`.
    Rename `TestClaudeAndOpencodeCarryBothRoles` to
    `TestEveryKindCarriesAllThreeRoles` and iterate
    `[]string{"agy", "claude", "opencode"}`. Add to the same file:

    ```go
    func TestAgyRowsExpectInherit(t *testing.T) {
        h, _ := Lookup("agy")
        if h.MinVersion != "1.1.6" {
            t.Errorf("agy MinVersion = %q, want 1.1.6", h.MinVersion)
        }
        for _, r := range h.Roles {
            if r.ExpectModel != "inherit" {
                t.Errorf("agy role %q ExpectModel = %q, want inherit", r.Name, r.ExpectModel)
            }
        }
        for _, kind := range []string{"claude", "opencode"} {
            h, _ := Lookup(kind)
            if h.MinVersion != "" {
                t.Errorf("%s MinVersion = %q, want empty", kind, h.MinVersion)
            }
            for _, r := range h.Roles {
                if r.ExpectModel != "" {
                    t.Errorf("%s role %q ExpectModel = %q, want empty", kind, r.Name, r.ExpectModel)
                }
            }
        }
    }
    ```
  - `agents_test.go`: remove the `{role: "plan-executor", kind: "agy"}`
    case from `TestAgentDocRejectsUnknownPairs` (it now resolves).
    `TestPlanExecutorDefinitionsForbidWritingSubAgents` iterates
    `[]string{"claude", "opencode", "agy"}`. Add:

    ```go
    // agy enforces what the other kinds only say: a tools allowlist with
    // no write tool, and subagent: false on the one writer (spec §7.3, §7.4).
    func TestAgyDefinitionsFrontmatter(t *testing.T) {
        forbidden := regexp.MustCompile(`(?m)^\s*-\s*(write_to_file|replace_file_content|create_file|delete_file|notebook_edit|invoke_subagent|send_command_input)\s*$`)
        for _, role := range []string{"plan-executor", "researcher", "reviewer"} {
            doc, err := AgentDoc(role, "agy")
            if err != nil {
                t.Fatalf("AgentDoc(%s, agy): %v", role, err)
            }
            fm := frontmatter(t, doc)
            if !strings.Contains(fm, "\nname: "+role+"\n") {
                t.Errorf("%s: frontmatter must carry name: %s", role, role)
            }
            if !strings.Contains(fm, "\nmodel: inherit\n") {
                t.Errorf("%s: frontmatter must pin model: inherit", role)
            }
            switch role {
            case "plan-executor":
                if !strings.Contains(fm, "\nsubagent: false\n") {
                    t.Errorf("plan-executor must be subagent: false")
                }
                if strings.Contains(fm, "\ntools:") {
                    t.Errorf("plan-executor must not restrict tools; it is the writer")
                }
            default:
                if !strings.Contains(fm, "\ntools:\n") {
                    t.Errorf("%s must carry a tools allowlist", role)
                }
                if m := forbidden.FindString(fm); m != "" {
                    t.Errorf("%s allowlist contains a writing tool: %q", role, strings.TrimSpace(m))
                }
            }
        }
    }

    // frontmatter returns the text between the first two --- fences,
    // with a leading newline so callers can match "\nkey: value\n".
    func frontmatter(t *testing.T, doc []byte) string {
        t.Helper()
        s := string(doc)
        if !strings.HasPrefix(s, "---\n") {
            t.Fatal("definition does not open with a --- fence")
        }
        rest := s[len("---\n"):]
        end := strings.Index(rest, "\n---\n")
        if end < 0 {
            t.Fatal("definition frontmatter never closes")
        }
        return "\n" + rest[:end] + "\n"
    }
    ```

  Run: `go test ./internal/harness/`. Expected: green. Then confirm the
  mutations bite: temporarily add `    - write_to_file` to the
  researcher's `tools:` list and run `go test ./internal/harness/ -run
  TestAgyDefinitionsFrontmatter` -- it must fail; revert. Temporarily
  delete the one-writer sentence from `plan-executor.agy.md` and run
  `-run TestPlanExecutorDefinitionsForbidWritingSubAgents` -- it must
  fail; revert.

- [ ] **Step 3: `Launch` renders `--agent` for agy; `Launch.Preamble` and `RoleSpec.Preamble` go** (spec §3.1, §3.4, §4.1)

  In `harness.go`:
  - Delete `Preamble` from `RoleSpec` and the three `Preamble:` lines in
    `roleTable`.
  - Delete `Preamble` from `Launch`, and the `var preamble string` /
    `Preamble: preamble` lines in `Launch()`.
  - The `case "agy":` branch becomes
    `base = []string{"--model", model, "--agent", role.Definition}`.
  - Rewrite `Launch`'s doc comment: "Launch renders the command-line
    arguments needed to run the given role on this harness. Relay renders
    the argv because model and role are fields (candidates spec §1 point
    2): with a verbatim args list in config, the model would be a label
    relay could not check against what it launched. Every kind selects its
    role with --agent; there is no other mechanism (#85)."

  `harness_test.go` `TestLaunch`: delete the `wantPreamble` field from the
  table struct, every `wantPreamble:` line, and the `got.Preamble`
  assertion. The two agy cases become:

  ```go
  {name: "agy builder", kind: "agy", provider: "prov", model: "m/x", extra: nil, role: builder,
      wantArgs: []string{"--model", "m/x", "--agent", "plan-executor"}},
  {name: "agy builder with extra", kind: "agy", provider: "prov", model: "m/x",
      extra: []string{"--dangerously-skip-permissions"}, role: builder,
      wantArgs: []string{"--model", "m/x", "--agent", "plan-executor", "--dangerously-skip-permissions"}},
  ```

  and add one more:

  ```go
  {name: "agy researcher", kind: "agy", provider: "prov", model: "m/x", extra: nil, role: researcher,
      wantArgs: []string{"--model", "m/x", "--agent", "researcher"}},
  ```

  (`researcher, ok := RoleByName("researcher")` beside the existing
  `builder`/`reviewer` lookups.)

  Run: `go build ./...`. Expected: failures in `internal/relay` only
  (`send.go`, `ask.go` reference `l.Preamble`). Step 4 fixes them.

- [ ] **Step 4: `send.go` and `ask.go` stop consulting a preamble** (spec §4.7, §4.8, §7.5)

  `internal/relay/send.go`: `composePrompt` becomes

  ```go
  // composePrompt renders the builder prompt for this round. Nothing is
  // prepended on any round: every kind selects its role with --agent at
  // launch (#85), so there is no first-prompt courtesy left to pay.
  func composePrompt(b store.Binding, planPath, reportPath string) string {
      return fmt.Sprintf(builderPrompt, b.Round, planPath, reportPath)
  }
  ```

  Update its one call site (drop the `rt` argument and the error check).
  Remove the now-unused imports (`candidate`, `harness` -- check with `go
  vet`).

  `internal/relay/ask.go`: the block

  ```go
  text := l.Preamble
  if text != "" {
      text += "\n\n"
  }
  text += fmt.Sprintf(consultPrompt, consult.AskPath, consult.FindingsPath)
  ```

  becomes `text := fmt.Sprintf(consultPrompt, consult.AskPath, consult.FindingsPath)`.

  Tests:
  - `send_test.go`: delete `TestSendIncludesPreambleOnFirstRoundOnly` and
    `TestSendWithoutPreambleWhenCandidateWasRemoved`. Replace them with:

    ```go
    // The role is selected with --agent at launch (#85); the plan prompt is
    // the plan prompt, on round 1 as on every other.
    func TestSendPromptCarriesNoPreamble(t *testing.T) {
        f := &fakeHerdr{}
        rt, _ := seedBound(t, f) // an agy candidate, the kind that used to get one
        src := writePlan(t, "x")
        if _, err := Send(context.Background(), rt, "webshop", src); err != nil {
            t.Fatalf("round 1 Send: %v", err)
        }
        if len(f.prompts) != 1 {
            t.Fatalf("got %d prompts, want 1", len(f.prompts))
        }
        if strings.Contains(f.prompts[0].Text, "Activate your") {
            t.Errorf("round 1 prompt must not carry a preamble:\n%s", f.prompts[0].Text)
        }
        if !strings.HasPrefix(f.prompts[0].Text, "Round 1 from the planner.") {
            t.Errorf("prompt must start with the builder prompt template:\n%s", f.prompts[0].Text)
        }
    }
    ```
    `TestSendIncludesPreambleWhenPending` and
    `TestSendFailedPromptLeavesPreamblePending` are deleted in step 5.
  - `ask_test.go`: replace `TestAskPrependsThePreambleForAPreambleHarness`
    with:

    ```go
    // An agy consult is launched with --agent (#85); its first prompt is the
    // consult prompt and nothing else.
    func TestAskSendsTheConsultPromptAlone(t *testing.T) {
        f := &fakeHerdr{}
        rt, _ := seedForAsk(t, f)
        rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"t","model":"m","roles":["reviewer"]}]`)
        q := writeQuestion(t, "review this")
        _, err := Ask(context.Background(), rt, AskOptions{
            Role: "reviewer", File: q, Name: "webshop", PlannerPane: "w2:p3",
        })
        if err != nil {
            t.Fatalf("Ask: %v", err)
        }
        if len(f.prompts) != 1 {
            t.Fatalf("got %d prompts, want 1", len(f.prompts))
        }
        if strings.Contains(f.prompts[0].Text, "Activate your") {
            t.Errorf("consult prompt must not carry a preamble:\n%s", f.prompts[0].Text)
        }
    }
    ```

    Also assert, in the same test, that the agent was started with
    `--agent reviewer`: `fakeHerdr.starts` (`internal/relay/fake_test.go`)
    records every `StartAgent` call as a `startCall{Name, Kind, Pane,
    Args}`; require `len(f.starts) == 1` and that `f.starts[0].Args`
    contains the adjacent pair `"--agent", "reviewer"`.

  Run: `go test ./internal/relay/`. Expected: only the four
  `PreamblePending` tests and the bind assertions fail. Step 5.

- [ ] **Step 5: `Binding.PreamblePending` goes** (spec §3.5, §4.9, §5.3)

  `internal/store/types.go`: delete the `PreamblePending` field and its
  comment. Add, where it was, a one-line comment: `// preamble_pending
  (pre-#85) is ignored on load: the role is selected at launch now.`

  `internal/relay/send.go`: delete `b.PreamblePending = false`.
  `internal/relay/bind.go`: delete `b.PreamblePending = true` in the rebind
  branch; in the doc comment's postcondition list, delete "PreamblePending
  is true,".

  Tests:
  - `store_test.go`: add beside `TestLoadIgnoresLegacyBuilderAlias`:

    ```go
    // A binding written before #85 carries preamble_pending; the decoder
    // drops it, and nothing reads it: the role is selected at launch now.
    func TestLoadIgnoresLegacyPreamblePending(t *testing.T) {
        s := New(t.TempDir())
        dir := s.Dir("old")
        if err := os.MkdirAll(dir, 0o755); err != nil {
            t.Fatalf("mkdir: %v", err)
        }
        body := `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"preamble_pending":true,"round":3,"state":"active","round_cap":20,"round_timeout_ms":1800000}`
        if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(body), 0o644); err != nil {
            t.Fatalf("write bind.json: %v", err)
        }
        got, err := s.Load("old")
        if err != nil {
            t.Fatalf("Load: %v", err)
        }
        if got.Round != 3 {
            t.Errorf("got.Round = %d, want 3", got.Round)
        }
        if err := s.Save(got); err != nil {
            t.Fatalf("Save: %v", err)
        }
        raw, err := os.ReadFile(filepath.Join(dir, "bind.json"))
        if err != nil {
            t.Fatalf("read back: %v", err)
        }
        if strings.Contains(string(raw), "preamble_pending") {
            t.Errorf("round-trip must drop preamble_pending, got:\n%s", raw)
        }
    }
    ```
  - `send_test.go`: delete `TestSendIncludesPreambleWhenPending` and
    `TestSendFailedPromptLeavesPreamblePending`.
  - `bind_test.go`: delete every `PreamblePending` assertion (lines near
    471, 501, 536, 634, 1093 at the time of writing -- grep). Where a
    multi-field condition includes `!saved.PreamblePending ||`, remove
    only that clause. The test at ~1093 ("a replacement builder must get
    the preamble") loses its final `if` block only; the pane assertion
    above it stays.

  Run: `grep -rn 'Preamble' --include='*.go' .` Expected: exactly one
  match, the test name `TestDoctorAgyRoleSelectedByPreamble` in
  `internal/doctor/doctor_test.go` (T2 replaces it; that test still passes
  after this task because doctor's `len(h.Roles) == 0` branch is merely
  dead, not removed -- if it fails, stop and report rather than editing
  doctor). Then `go test ./...`. Expected: green.

- [ ] **Step 6: `make check`, commit**

  ```bash
  git add internal/harness internal/store internal/relay
  git commit -m "feat(harness): agy role definitions, --agent launch, preamble removed (#85)"
  ```

## Report

Include the `go test ./internal/harness/ -v` summary, the two mutation
results from step 2 (which test failed, one line each), the `make check`
result, and `git diff --stat HEAD~1`. If `internal/doctor` or `cmd/relay`
needed any edit to compile, name the file and the line.
