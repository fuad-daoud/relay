package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testKV is a real t.TempDir() database, the medium the role manifest lives in
// from this round (P3b plan §7).
func testKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

type fakeInstallEnv struct {
	lookPaths   map[string]string
	home        string
	homeErr     error
	files       map[string][]byte
	readErr     error
	dirs        []string
	mkdirErr    error
	writes      map[string][]byte
	writeErrFor map[string]error

	// manifest is the role manifest (#371 §4.10): what LoadManifest returns
	// and what SaveManifest stores. manifestErr makes LoadManifest fail,
	// saveErr makes SaveManifest fail; saves counts the calls.
	manifest    map[string]string
	manifestErr error
	saveErr     error
	saves       int
}

func (e *fakeInstallEnv) LookPath(binary string) (string, error) {
	if p, ok := e.lookPaths[binary]; ok {
		return p, nil
	}
	return "", fmt.Errorf("binary not found: %s", binary)
}

func (e *fakeInstallEnv) HomePath(rel string) (string, error) {
	if e.homeErr != nil {
		return "", e.homeErr
	}
	return filepath.Join(e.home, rel), nil
}

func (e *fakeInstallEnv) ReadFile(path string) ([]byte, error) {
	if e.readErr != nil {
		return nil, e.readErr
	}
	if b, ok := e.files[path]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

func (e *fakeInstallEnv) MkdirAll(dir string) error {
	if e.mkdirErr != nil {
		return e.mkdirErr
	}
	e.dirs = append(e.dirs, dir)
	return nil
}

func (e *fakeInstallEnv) WriteFile(path string, data []byte) error {
	if err, ok := e.writeErrFor[path]; ok {
		return err
	}
	e.writes[path] = data
	e.files[path] = data
	return nil
}

func (e *fakeInstallEnv) LoadManifest() (map[string]string, error) {
	if e.manifestErr != nil {
		return nil, e.manifestErr
	}
	if e.manifest == nil {
		e.manifest = make(map[string]string)
	}
	return e.manifest, nil
}

func (e *fakeInstallEnv) SaveManifest(m map[string]string) error {
	e.saves++
	if e.saveErr != nil {
		return e.saveErr
	}
	e.manifest = m
	return nil
}

func freshEnv() *fakeInstallEnv {
	return &fakeInstallEnv{
		home:        "/home/u",
		lookPaths:   make(map[string]string),
		files:       make(map[string][]byte),
		writes:      make(map[string][]byte),
		writeErrFor: make(map[string]error),
		manifest:    make(map[string]string),
	}
}

func TestInstallFreshHomeWritesEveryRoleOfPathKinds(t *testing.T) {
	env := freshEnv()
	env.lookPaths["agy"] = "/bin/agy"
	env.lookPaths["claude"] = "/bin/claude"

	results, err := Install(env, InstallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hAgy, _ := Lookup("agy")
	hClaude, _ := Lookup("claude")
	expectedLen := len(hAgy.Roles) + len(hClaude.Roles)
	if len(results) != expectedLen {
		t.Fatalf("expected %d results, got %d", expectedLen, len(results))
	}

	idx := 0
	for _, r := range hAgy.Roles {
		res := results[idx]
		if res.Kind != "agy" || res.Role != r.Name {
			t.Errorf("result %d: want agy/%s, got %s/%s", idx, r.Name, res.Kind, res.Role)
		}
		if res.Outcome != OutcomeWrote {
			t.Errorf("result %d: want OutcomeWrote, got %v", idx, res.Outcome)
		}
		expectedDoc, err := AgentDoc(r.Name, "agy")
		if err != nil {
			t.Fatalf("unexpected AgentDoc error: %v", err)
		}
		fullPath := "/home/u/" + res.Path
		if !bytes.Equal(env.files[fullPath], expectedDoc) {
			t.Errorf("result %d (%s): file content mismatch with AgentDoc", idx, fullPath)
		}
		idx++
	}

	for _, r := range hClaude.Roles {
		res := results[idx]
		if res.Kind != "claude" || res.Role != r.Name {
			t.Errorf("result %d: want claude/%s, got %s/%s", idx, r.Name, res.Kind, res.Role)
		}
		if res.Outcome != OutcomeWrote {
			t.Errorf("result %d: want OutcomeWrote, got %v", idx, res.Outcome)
		}
		expectedDoc, err := AgentDoc(r.Name, "claude")
		if err != nil {
			t.Fatalf("unexpected AgentDoc error: %v", err)
		}
		fullPath := "/home/u/" + res.Path
		if !bytes.Equal(env.files[fullPath], expectedDoc) {
			t.Errorf("result %d (%s): file content mismatch with AgentDoc", idx, fullPath)
		}
		idx++
	}

	for _, res := range results {
		if res.Kind == "opencode" {
			t.Errorf("expected no opencode results, found: %+v", res)
		}
	}

	hasAgyDir := false
	hasClaudeDir := false
	for _, d := range env.dirs {
		if d == "/home/u/.gemini/config/agents" {
			hasAgyDir = true
		}
		if d == "/home/u/.claude/agents" {
			hasClaudeDir = true
		}
	}
	if !hasAgyDir {
		t.Errorf("expected dirs to contain /home/u/.gemini/config/agents, got %v", env.dirs)
	}
	if !hasClaudeDir {
		t.Errorf("expected dirs to contain /home/u/.claude/agents, got %v", env.dirs)
	}
}

func TestInstallKeepsIdenticalEvenWithForce(t *testing.T) {
	env := freshEnv()
	shipped, err := AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc error: %v", err)
	}
	path := "/home/u/.claude/agents/researcher.md"
	env.files[path] = append(append([]byte(nil), shipped...), []byte("\n\n")...)

	results, err := Install(env, InstallOptions{
		Kind:  "claude",
		Role:  "researcher",
		Force: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != OutcomeKeptIdentical {
		t.Errorf("expected OutcomeKeptIdentical, got %v", results[0].Outcome)
	}
	if len(env.writes) != 0 {
		t.Errorf("expected len(writes) == 0, got %d", len(env.writes))
	}
}

func TestInstallKeepsDifferingWithoutForce(t *testing.T) {
	env := freshEnv()
	path := "/home/u/.claude/agents/researcher.md"
	env.files[path] = []byte("---\nmodel: haiku\n---\nmine\n")

	results, err := Install(env, InstallOptions{
		Kind: "claude",
		Role: "researcher",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != OutcomeKeptDiffers {
		t.Errorf("expected OutcomeKeptDiffers, got %v", results[0].Outcome)
	}
	if len(env.writes) != 0 {
		t.Errorf("expected len(writes) == 0, got %d", len(env.writes))
	}
}

func TestInstallOverwritesDifferingWithForce(t *testing.T) {
	env := freshEnv()
	path := "/home/u/.claude/agents/researcher.md"
	env.files[path] = []byte("---\nmodel: haiku\n---\nmine\n")

	results, err := Install(env, InstallOptions{
		Kind:  "claude",
		Role:  "researcher",
		Force: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != OutcomeOverwrote {
		t.Errorf("expected OutcomeOverwrote, got %v", results[0].Outcome)
	}
	shipped, err := AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc error: %v", err)
	}
	if !bytes.Equal(env.files[path], shipped) {
		t.Errorf("expected files[%s] to equal shipped doc", path)
	}
}

func TestInstallDryRunTouchesNothing(t *testing.T) {
	env := freshEnv()
	claudePE, _ := AgentDoc("plan-executor", "claude")
	env.files["/home/u/.claude/agents/plan-executor.md"] = claudePE
	env.files["/home/u/.claude/agents/researcher.md"] = []byte("---\nmodel: haiku\n---\nmine\n")

	// reviewer and architect absent
	results, err := Install(env, InstallOptions{
		Kind:   "claude",
		DryRun: true,
		Force:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	expectedOutcomes := []InstallOutcome{
		OutcomeKeptIdentical,
		OutcomeWouldOverwrite,
		OutcomeWouldWrite,
		OutcomeWouldWrite,
	}
	for i, want := range expectedOutcomes {
		if results[i].Outcome != want {
			t.Errorf("result %d (%s): want outcome %v, got %v", i, results[i].Role, want, results[i].Outcome)
		}
	}
	if len(env.dirs) != 0 {
		t.Errorf("expected len(dirs) == 0, got %d", len(env.dirs))
	}
	if len(env.writes) != 0 {
		t.Errorf("expected len(writes) == 0, got %d", len(env.writes))
	}

	// Then the same without Force: researcher is OutcomeKeptDiffers
	env2 := freshEnv()
	env2.files["/home/u/.claude/agents/plan-executor.md"] = claudePE
	env2.files["/home/u/.claude/agents/researcher.md"] = []byte("---\nmodel: haiku\n---\nmine\n")
	results2, err := Install(env2, InstallOptions{
		Kind:   "claude",
		DryRun: true,
		Force:  false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results2) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results2))
	}
	if results2[1].Outcome != OutcomeKeptDiffers {
		t.Errorf("expected researcher OutcomeKeptDiffers without Force, got %v", results2[1].Outcome)
	}
}

func TestInstallNamedKindIgnoresPath(t *testing.T) {
	env := freshEnv() // empty lookPaths
	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	for i, res := range results {
		if res.Outcome != OutcomeWrote {
			t.Errorf("result %d: want OutcomeWrote, got %v", i, res.Outcome)
		}
	}
}

func TestInstallNoKindNoBinariesIsEmpty(t *testing.T) {
	env := freshEnv() // empty lookPaths
	results, err := Install(env, InstallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestInstallUnknownKind(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "nope"})
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error to contain 'nope', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestInstallUnknownRole(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "claude", Role: "nope"})
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("expected ErrUnknownRole, got %v", err)
	}
	if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "plan-executor") {
		t.Errorf("expected error to contain 'nope' and 'plan-executor', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}

	// Then with Kind: "" and empty lookPaths, Role: "nope": still ErrUnknownRole
	env2 := freshEnv()
	results2, err2 := Install(env2, InstallOptions{Kind: "", Role: "nope"})
	if !errors.Is(err2, ErrUnknownRole) {
		t.Fatalf("expected ErrUnknownRole, got %v", err2)
	}
	if results2 != nil {
		t.Errorf("expected nil results, got %v", results2)
	}
}

func TestInstallWriteFailureIsPerFile(t *testing.T) {
	env := freshEnv()
	env.writeErrFor["/home/u/.gemini/config/agents/researcher.md"] = errors.New("read-only")

	results, err := Install(env, InstallOptions{Kind: "agy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	for _, res := range results {
		if res.Role == "researcher" {
			if res.Outcome != OutcomeError {
				t.Errorf("researcher: want OutcomeError, got %v", res.Outcome)
			}
			if res.Err != "read-only" {
				t.Errorf("researcher: want Err 'read-only', got %q", res.Err)
			}
		} else {
			if res.Outcome != OutcomeWrote {
				t.Errorf("%s: want OutcomeWrote, got %v", res.Role, res.Outcome)
			}
		}
	}
}

func TestInstallHomeErrorAborts(t *testing.T) {
	env := freshEnv()
	env.homeErr = errors.New("no home")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no home") {
		t.Errorf("expected error to contain 'no home', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestInstallUnreadableCountsAsDiffers(t *testing.T) {
	env := freshEnv()
	env.readErr = errors.New("permission denied")

	results, err := Install(env, InstallOptions{
		Kind: "claude",
		Role: "reviewer",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != OutcomeKeptDiffers {
		t.Errorf("expected OutcomeKeptDiffers, got %v", results[0].Outcome)
	}

	// with Force: true, OutcomeOverwrote
	env2 := freshEnv()
	env2.readErr = errors.New("permission denied")
	results2, err2 := Install(env2, InstallOptions{
		Kind:  "claude",
		Role:  "reviewer",
		Force: true,
	})
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if len(results2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results2))
	}
	if results2[0].Outcome != OutcomeOverwrote {
		t.Errorf("expected OutcomeOverwrote, got %v", results2[0].Outcome)
	}
}

// olderArchitectDoc is the exact bytes of the architect.claude.md definition
// from commit 9b837e64 of this repository: an older shipped copy whose sha256
// scripts/agents-shipped.sh recorded under "architect.claude.md". It is what an
// existing install with no role manifest has on disk (#371 round 3 §7 step 3).
const olderArchitectDoc = `---
name: architect
description: >-
  Use this agent when you need to design the architecture for a new feature or
  system, including interface definitions, data structures, component contracts,
  file paths, and high-level pseudocode. This agent should be invoked before any
  implementation work begins. It is the planner half of a relay handoff: it
  produces the ordered implementation plan a builder executes, and never writes
  implementation code itself.
---

You are a seasoned Software Architect with 20 years of experience designing large-scale distributed systems. Your expertise spans domain-driven design, microservices architecture, interface design, and system decomposition. You think in terms of contracts, boundaries, and abstractions. You are meticulous about separation of concerns and believe that well-designed interfaces are the foundation of maintainable systems.

## Core Mission

Design the complete architecture for features or systems by producing interface definitions, data structures, component contracts, file paths, and high-level logic in pseudocode. You define WHAT gets built and HOW components connect — never HOW they are implemented internally.

## Strict Boundaries

**You WILL:**
- Define interfaces, type signatures, and method contracts
- Design data structures with field-level specifications
- Specify file paths and module organization
- Write high-level pseudocode describing component interactions and logic flow
- Define error types and error handling contracts
- Specify dependency injection requirements and constructor signatures
- Define event/message schemas if applicable
- Break the plan into strictly ordered, actionable implementation steps

**You WILL NOT:**
- Write actual implementation code in any programming language
- Include function bodies, algorithms, or concrete logic
- Make technology-specific implementation choices (e.g., specific libraries)
- Write database queries, ORM configurations, or SQL
- Implement business logic beyond pseudocode flow descriptions
- Write tests or test cases

## Output Structure

Every architectural plan must follow this exact structure:

### 1. System Overview
A concise paragraph describing the feature/system, its purpose, and how it fits into the broader application context.

### 2. File Structure
A tree representation of all new files and directories to be created, with brief annotations explaining each file's responsibility.

### 3. Data Structures & Type Definitions
For each data structure, provide:
- The type/interface name
- Every field with its type and a description of its purpose
- Validation constraints (required/optional, min/max, format requirements)
- Relationships to other data structures

### 4. Interface Definitions & Component Contracts
For each interface/contract, provide:
- The interface name and its single responsibility
- Every method signature including parameter types and return types
- Error types that each method may produce
- Preconditions and postconditions for each method
- Dependencies required by the implementing component

### 5. High-Level Pseudocode
Describe the logical flow of the system using structured pseudocode. Focus on:
- Orchestration between components
- Decision points and branching logic
- Data transformation pipelines
- Error handling flows
- State transitions

### 6. Error Handling Strategy
Define:
- Error categories and their hierarchy
- Which errors are recoverable vs. non-recoverable
- Error propagation contracts between layers
- Logging and observability requirements

### 7. Ordered Implementation Steps
Break the plan into strictly ordered, actionable steps. Each step must:
- Have a clear, single deliverable
- List dependencies on previous steps
- Reference the specific interfaces/data structures being implemented
- Be small enough to be completed in a single focused session
- Include verification criteria (how to know the step is done correctly)

## Quality Standards

- Every public interface must have a clearly stated single responsibility
- Data structures must be complete — no placeholder fields or TODO types
- All error states must be explicitly modeled
- File paths must follow the project's existing conventions (check CLAUDE.md or project structure for guidance)
- Pseudocode must be detailed enough that a developer could implement from it without ambiguity
- Steps must be ordered such that dependencies are always built before dependents

## Self-Verification Checklist

Before finalizing any architectural plan, verify:
1. Are all interfaces defined with complete method signatures?
2. Are all data structures fully specified with types and constraints?
3. Does the file structure cover every artifact mentioned?
4. Are error states explicitly enumerated?
5. Is the implementation order correct (dependencies first)?
6. Have I avoided writing any implementation code?
7. Could a developer unfamiliar with the system implement this plan?

If any answer is "no," revise before presenting the plan.
`

// TestInstallRecognizesOlderShippedBlob is §7 step 3's first row: a definition
// whose bytes are an older shipped blob, with no manifest at all, is refreshed
// -- the upgrade path an existing install needs (#371 round 3 §1). Every
// install that predates round 2 has no manifest, so this row is the whole point.
//
// Mutation: drop the ShippedBefore clause from installOne's third case and this
// test fails -- OutcomeKeptDiffers instead of OutcomeUpdated.
func TestInstallRecognizesOlderShippedBlob(t *testing.T) {
	const path = ".claude/agents/architect.md"
	const full = "/home/u/.claude/agents/architect.md"
	if !ShippedBefore("architect.claude.md", docSHA([]byte(olderArchitectDoc))) {
		t.Fatal("the fixture sha is not in agents/shipped.sha256; regenerate the fixture")
	}
	shipped, err := AgentDoc("architect", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	env := freshEnv()
	env.files[full] = []byte(olderArchitectDoc)
	// No manifest: a machine that predates round 2 entirely.

	results, err := Install(env, InstallOptions{Kind: "claude", Role: "architect"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeUpdated {
		t.Fatalf("results = %+v, want one OutcomeUpdated", results)
	}
	if !bytes.Equal(env.files[full], shipped) {
		t.Error("the older shipped copy was not refreshed with the shipped definition")
	}
	if got := env.manifest[path]; got != docSHA(shipped) {
		t.Errorf("manifest[%s] = %q, want the shipped sha", path, got)
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}

	// The same decision under DryRun reports without writing.
	dry := freshEnv()
	dry.files[full] = []byte(olderArchitectDoc)
	dryResults, err := Install(dry, InstallOptions{Kind: "claude", Role: "architect", DryRun: true})
	if err != nil {
		t.Fatalf("Install(DryRun): %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Outcome != OutcomeWouldUpdate {
		t.Fatalf("DryRun results = %+v, want one OutcomeWouldUpdate", dryResults)
	}
	if len(dry.writes) != 0 {
		t.Errorf("DryRun wrote %v", dry.writes)
	}
}

// TestInstallKeepsAnArbitraryEdit is §7 step 3's second row: bytes that are no
// version relevo ever shipped are the user's own, and stay.
func TestInstallKeepsAnArbitraryEdit(t *testing.T) {
	const full = "/home/u/.claude/agents/architect.md"
	env := freshEnv()
	env.files[full] = []byte("---\nname: architect\n---\nmy own architect\n")

	results, err := Install(env, InstallOptions{Kind: "claude", Role: "architect"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeKeptDiffers {
		t.Fatalf("results = %+v, want one OutcomeKeptDiffers", results)
	}
	if len(env.writes) != 0 {
		t.Errorf("Install wrote %v, want no write", env.writes)
	}
}

// TestInstallManifestDecisions covers §5's decision table row by row, over the
// role manifest (#371 §4.10): what lands, and what the manifest records.
func TestInstallManifestDecisions(t *testing.T) {
	shipped, err := AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}
	const path = ".claude/agents/researcher.md"
	const full = "/home/u/.claude/agents/researcher.md"
	// An older relevo release wrote these bytes and recorded their sha.
	older := []byte("---\nmodel: haiku\n---\nan older relevo copy\n")
	// The user's own edit: never in the manifest under this sha.
	edited := []byte("---\nmodel: sonnet\n---\nthe user's own copy\n")
	// Identical under DocEqual's trailing-whitespace rule, not byte-identical.
	identical := append(append([]byte(nil), shipped...), []byte("\n\n")...)

	tests := []struct {
		name         string
		existing     []byte // nil = the file is absent
		manifest     map[string]string
		force        bool
		dryRun       bool
		wantOutcome  InstallOutcome
		wantManifest map[string]string
		wantWrote    bool
	}{
		{
			name:         "missing file is written and recorded",
			wantOutcome:  OutcomeWrote,
			wantManifest: map[string]string{path: docSHA(shipped)},
			wantWrote:    true,
		},
		{
			name:         "missing file under DryRun only reports",
			dryRun:       true,
			wantOutcome:  OutcomeWouldWrite,
			wantManifest: map[string]string{},
		},
		{
			name:         "identical records the bytes on disk",
			existing:     identical,
			wantOutcome:  OutcomeKeptIdentical,
			wantManifest: map[string]string{path: docSHA(identical)},
		},
		{
			name:         "differing but unchanged since relevo wrote it is updated",
			existing:     older,
			manifest:     map[string]string{path: docSHA(older)},
			wantOutcome:  OutcomeUpdated,
			wantManifest: map[string]string{path: docSHA(shipped)},
			wantWrote:    true,
		},
		{
			name:         "differing but unchanged since relevo wrote it under DryRun only reports",
			existing:     older,
			manifest:     map[string]string{path: docSHA(older)},
			dryRun:       true,
			wantOutcome:  OutcomeWouldUpdate,
			wantManifest: map[string]string{path: docSHA(older)},
		},
		{
			name:         "edited by the user is kept",
			existing:     edited,
			manifest:     map[string]string{path: docSHA(older)},
			wantOutcome:  OutcomeKeptDiffers,
			wantManifest: map[string]string{path: docSHA(older)},
		},
		{
			name:         "a path not in the manifest is kept",
			existing:     edited,
			wantOutcome:  OutcomeKeptDiffers,
			wantManifest: map[string]string{},
		},
		{
			name:         "edited by the user with Force is overwritten and recorded",
			existing:     edited,
			manifest:     map[string]string{path: docSHA(older)},
			force:        true,
			wantOutcome:  OutcomeOverwrote,
			wantManifest: map[string]string{path: docSHA(shipped)},
			wantWrote:    true,
		},
		{
			name:         "edited by the user with Force under DryRun only reports",
			existing:     edited,
			manifest:     map[string]string{path: docSHA(older)},
			force:        true,
			dryRun:       true,
			wantOutcome:  OutcomeWouldOverwrite,
			wantManifest: map[string]string{path: docSHA(older)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := freshEnv()
			if tc.existing != nil {
				env.files[full] = tc.existing
			}
			env.manifest = tc.manifest

			results, err := Install(env, InstallOptions{
				Kind:   "claude",
				Role:   "researcher",
				Force:  tc.force,
				DryRun: tc.dryRun,
			})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			if got := results[0].Outcome; got != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", got, tc.wantOutcome)
			}
			if !reflect.DeepEqual(env.manifest, tc.wantManifest) {
				t.Errorf("manifest = %v, want %v", env.manifest, tc.wantManifest)
			}
			if tc.wantWrote && !bytes.Equal(env.files[full], shipped) {
				t.Errorf("file on disk = %q, want the shipped definition", env.files[full])
			}
			if tc.dryRun {
				if len(env.writes) != 0 {
					t.Errorf("DryRun wrote %v", env.writes)
				}
				if env.saves != 0 {
					t.Errorf("DryRun saved the manifest %d times, want 0", env.saves)
				}
			}
		})
	}
}

// TestInstallUpdatesDefinitionUnchangedSinceRelevoWroteIt is §5's third row on
// its own: the bytes on disk are exactly what relevo last wrote, so a newer
// shipped definition refreshes them and records the new sha.
//
// Mutation: make the `manifest[path] == docSHA(existing)` row always false and
// this test fails -- OutcomeKeptDiffers instead of OutcomeUpdated.
func TestInstallUpdatesDefinitionUnchangedSinceRelevoWroteIt(t *testing.T) {
	shipped, err := AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}
	const path = ".claude/agents/researcher.md"
	const full = "/home/u/.claude/agents/researcher.md"
	older := []byte("---\nmodel: haiku\n---\nan older relevo copy\n")

	env := freshEnv()
	env.files[full] = older
	env.manifest = map[string]string{path: docSHA(older)}

	results, err := Install(env, InstallOptions{Kind: "claude", Role: "researcher"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeUpdated {
		t.Fatalf("results = %+v, want one OutcomeUpdated", results)
	}
	if !bytes.Equal(env.files[full], shipped) {
		t.Errorf("file = %q, want the shipped definition refreshed", env.files[full])
	}
	if got := env.manifest[path]; got != docSHA(shipped) {
		t.Errorf("manifest[%s] = %q, want the shipped sha", path, got)
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}
}

// TestInstallSavesManifestOnce pins §4's rule: one save per Install call, with
// every written path recorded, never one save per file.
func TestInstallSavesManifestOnce(t *testing.T) {
	env := freshEnv()

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}
	if len(env.manifest) != 4 {
		t.Errorf("manifest = %v, want one entry per written definition", env.manifest)
	}
	for _, r := range results {
		shipped, err := AgentDoc(r.Role, "claude")
		if err != nil {
			t.Fatalf("AgentDoc(%s): %v", r.Role, err)
		}
		if got := env.manifest[r.Path]; got != docSHA(shipped) {
			t.Errorf("manifest[%s] = %q, want the shipped sha", r.Path, got)
		}
	}
}

// TestInstallReportsManifestLoadErrorOnce pins §3: a malformed manifest is an
// error Install reports once, and the definitions still land -- the manifest is
// treated as empty rather than blocking the install.
func TestInstallReportsManifestLoadErrorOnce(t *testing.T) {
	env := freshEnv()
	env.manifestErr = errors.New("decode role manifest: unexpected end of JSON input")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("Install = nil error, want the manifest error reported")
	}
	if !strings.Contains(err.Error(), "decode role manifest") {
		t.Errorf("Install error = %v, want it to name the manifest", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4: a corrupt manifest must not block the install", len(results))
	}
	for _, r := range results {
		if r.Outcome != OutcomeWrote {
			t.Errorf("%s/%s outcome = %v, want %v", r.Kind, r.Role, r.Outcome, OutcomeWrote)
		}
	}
}

// TestInstallReportsManifestSaveError keeps the results when only the record of
// them failed: the definitions landed, and the caller reports the error once.
func TestInstallReportsManifestSaveError(t *testing.T) {
	env := freshEnv()
	env.saveErr = errors.New("read-only file system")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("Install = nil error, want the manifest save error reported")
	}
	if !strings.Contains(err.Error(), "read-only file system") {
		t.Errorf("Install error = %v, want the save error", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
}

// TestReadWriteManifestRoundTrip pins the manifest record itself (#371 §3; P3b
// plan §4.4): missing is empty, a write is one kv row, and malformed JSON in a
// legacy file is an error.
func TestReadWriteManifestRoundTrip(t *testing.T) {
	kv := testKV(t)
	legacyPath := filepath.Join(t.TempDir(), "agents-manifest.json")

	empty, err := ReadManifest(kv, legacyPath)
	if err != nil {
		t.Fatalf("ReadManifest(missing): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ReadManifest(missing) = %v, want an empty map", empty)
	}

	want := map[string]string{
		".claude/agents/plan-executor.md": "0123456789abcdef",
		".claude/agents/researcher.md":    "fedcba9876543210",
	}
	if err := WriteManifest(kv, want); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(kv, "")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadManifest = %v, want %v", got, want)
	}
	if _, ok, err := kv.KVGet("agents-manifest"); err != nil || !ok {
		t.Errorf("KVGet(agents-manifest) = (_, %v, %v), want the row", ok, err)
	}

	// A malformed legacy file is refused by the import, which names the path.
	badPath := filepath.Join(t.TempDir(), "agents-manifest.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := ReadManifest(testKV(t), badPath); err == nil {
		t.Error("ReadManifest(malformed) = nil error, want an error")
	}
}

func TestOSInstallEnvWriteFileReplacesAndLeavesNoTempFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := OSInstallEnv()

	dir := filepath.Join(home, "agents")
	if err := env.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "researcher.md")

	if err := env.WriteFile(path, []byte("one")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := env.WriteFile(path, []byte("two")); err != nil {
		t.Fatalf("WriteFile (replace): %v", err)
	}

	data, err := env.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "two" {
		t.Errorf("content = %q, want %q", data, "two")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "researcher.md" {
		t.Errorf("directory holds %v, want only researcher.md (no temp file left behind)", entries)
	}
}

func TestDocEqual(t *testing.T) {
	shipped, err := AgentDoc("plan-executor", "claude")
	if err != nil {
		t.Fatalf("AgentDoc error: %v", err)
	}

	tests := []struct {
		name      string
		shipped   []byte
		installed []byte
		want      bool
	}{
		{
			name:      "equal bytes",
			shipped:   shipped,
			installed: shipped,
			want:      true,
		},
		{
			name:      "shipped vs shipped plus trailing whitespace",
			shipped:   shipped,
			installed: append(append([]byte(nil), shipped...), []byte("\n \t\r\n")...),
			want:      true,
		},
		{
			name:      "leading space difference",
			shipped:   shipped,
			installed: append([]byte(" "), shipped...),
			want:      false,
		},
		{
			name:      "changed model line",
			shipped:   []byte("---\nmodel: sonnet\n---\nbody\n"),
			installed: []byte("---\nmodel: haiku\n---\nbody\n"),
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DocEqual(tc.shipped, tc.installed)
			if got != tc.want {
				t.Errorf("DocEqual() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstallResultLine(t *testing.T) {
	tests := []struct {
		res  InstallResult
		want string
	}{
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeWrote},
			want: "wrote  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeKeptDiffers},
			want: "kept (differs; --force to overwrite)  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeUpdated},
			want: "updated (unchanged since relevo wrote it)  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeWouldUpdate},
			want: "would update  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Path: ".gemini/config/agents/reviewer.md", Outcome: OutcomeError, Err: "read-only"},
			want: "error  ~/.gemini/config/agents/reviewer.md: read-only",
		},
	}
	for _, tc := range tests {
		got := tc.res.Line()
		if got != tc.want {
			t.Errorf("Line() = %q, want %q", got, tc.want)
		}
	}
}

func TestOSInstallEnvRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := OSInstallEnv()

	p, err := env.HomePath("a/b.md")
	if err != nil {
		t.Fatalf("unexpected HomePath error: %v", err)
	}
	expectedHomePath := filepath.Join(home, "a/b.md")
	if p != expectedHomePath {
		t.Errorf("HomePath = %q, want %q", p, expectedHomePath)
	}

	_, err = env.ReadFile(p)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}

	if err := env.MkdirAll(filepath.Dir(p)); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := env.WriteFile(p, []byte("x")); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	data, err := env.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("ReadFile = %q, want %q", string(data), "x")
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode perm = %o, want 0644", info.Mode().Perm())
	}

	if _, err := env.LookPath("definitely-not-a-binary-relevo-test"); err == nil {
		t.Error("expected error for non-existent binary, got nil")
	}
}
