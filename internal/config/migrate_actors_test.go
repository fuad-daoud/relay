package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// seedSections stores each section's raw body, in Sections order, so a
// migration test starts from a config with revisions behind it (so the
// migration does not also record a baseline).
func seedSections(t *testing.T, s *Store, sections map[Section]string) {
	t.Helper()
	for _, sec := range Sections {
		body, ok := sections[sec]
		if !ok {
			continue
		}
		if _, err := s.As("test", "seed "+string(sec)).Put(sec, []byte(body)); err != nil {
			t.Fatalf("Put(%s): %v", sec, err)
		}
	}
}

// assertEquivalent applies round 2's equivalence rule: for every role name and
// every kind, the registry after the migration must give the same Spec, the
// same Ranked tokens in order, and the same RoleTier as the registry before
// it. Ranked positions are deliberately not compared: a legacy unlisted entry
// (Position 0) becomes an ordinary position.
func assertEquivalent(t *testing.T, before, after *roles.Registry) {
	t.Helper()
	if !reflect.DeepEqual(before.Names(), after.Names()) {
		t.Fatalf("names = %v, want %v", after.Names(), before.Names())
	}
	for _, name := range before.Names() {
		for _, h := range harness.All() {
			bs, berr := before.Spec(name, h.Kind)
			as, aerr := after.Spec(name, h.Kind)
			if (berr == nil) != (aerr == nil) {
				t.Errorf("%s/%s: Spec error before = %v, after = %v", name, h.Kind, berr, aerr)
				continue
			}
			if berr == nil && !reflect.DeepEqual(bs, as) {
				t.Errorf("%s/%s: Spec before = %+v, after = %+v", name, h.Kind, bs, as)
			}
		}

		br, _ := before.Role(name)
		ar, _ := after.Role(name)
		if bt, at := rankedTokens(br.Ranked), rankedTokens(ar.Ranked); !reflect.DeepEqual(bt, at) {
			t.Errorf("%s: Ranked before = %v, after = %v", name, bt, at)
		}

		btier, bok := before.RoleTier(name)
		atier, aok := after.RoleTier(name)
		if bok != aok || btier != atier {
			t.Errorf("%s: RoleTier before = %q/%v, after = %q/%v", name, btier, bok, atier, aok)
		}
	}
}

// rankedTokens returns a ranked list's canonical tokens, in order.
func rankedTokens(ranked []roles.Ranked) []string {
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.Token)
	}
	return out
}

// TestMigrateLegacyEquivalent pins the equivalence rule for the legacy
// fixtures: the registry Load builds after migrating the candidates and policy
// they describe is the one it built before.
func TestMigrateLegacyEquivalent(t *testing.T) {
	candBody := readFixture(t, "legacy-candidates.json")
	polBody := readFixture(t, "legacy-policy.json")

	set, _, err := candidate.Parse("candidates.json", candBody)
	if err != nil {
		t.Fatalf("candidate.Parse: %v", err)
	}
	pol, _, err := policy.Parse("policy.json", polBody)
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	before, err := roles.Build(nil, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: string(candBody),
		Policy:     string(polBody),
	})

	migrated, err := s.MigrateToActors()
	if err != nil {
		t.Fatalf("MigrateToActors: %v", err)
	}
	if !migrated {
		t.Fatal("MigrateToActors = false, want true for the legacy fixtures")
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertEquivalent(t, before, L.Registry)
}

// TestMigrateFileModeEquivalent pins the equivalence rule for a roles file: a
// custom writer on a custom claude definition becomes a native agent, and a
// reader stays the shipped one.
func TestMigrateFileModeEquivalent(t *testing.T) {
	candBody := `[
	  {"harness":"claude","provider":"test","model":"a"},
	  {"harness":"claude","provider":"test","model":"b"},
	  {"harness":"opencode","provider":"test","model":"m"}
	]`
	rolesBody := `{
	  "designer": {
	    "shape": "writer",
	    "definitions": {"claude": {"agent": "my-exec", "requires": ["my-scout"]}},
	    "candidates": ["a"]
	  },
	  "reviewer": {"candidates": ["b"]}
	}`
	polBody := `{"max_tier":"yolo"}`

	set, _, err := candidate.Parse("candidates.json", []byte(candBody))
	if err != nil {
		t.Fatalf("candidate.Parse: %v", err)
	}
	pol, _, err := policy.Parse("policy.json", []byte(polBody))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	rf, _, err := roles.Parse("roles.json", []byte(rolesBody))
	if err != nil {
		t.Fatalf("roles.Parse: %v", err)
	}
	before, err := roles.Build(rf, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: candBody,
		Policy:     polBody,
		Roles:      rolesBody,
	})

	migrated, err := s.MigrateToActors()
	if err != nil {
		t.Fatalf("MigrateToActors: %v", err)
	}
	if !migrated {
		t.Fatal("MigrateToActors = false, want true for a roles file")
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertEquivalent(t, before, L.Registry)

	// The custom writer's native agent carries its claude definition.
	if got := L.Actors["designer"].Agent; got != "my-exec" {
		t.Errorf("designer agent = %q, want my-exec", got)
	}
	if _, ok := L.Agents["my-exec"]; !ok {
		t.Errorf("Agents = %v, want the native my-exec", L.Agents)
	}
}

// TestMigrateNamesCandidates pins the candidate rewrite: a resolvable token
// becomes its candidate's short name, and one that does not resolve is kept as
// written, with a note on the migration revision.
func TestMigrateNamesCandidates(t *testing.T) {
	candBody := `[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`
	rolesBody := `{"builder":{"candidates":["claude/anthropic/sonnet","claude/anthropic/ghost"]}}`

	s := openStore(t)
	seedSections(t, s, map[Section]string{Candidates: candBody, Roles: rolesBody})

	if migrated, err := s.MigrateToActors(); err != nil || !migrated {
		t.Fatalf("MigrateToActors = %v/%v, want true/nil", migrated, err)
	}

	L, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, e := range L.Actors["builder"].Candidates {
		got = append(got, e.Candidate)
	}
	want := []string{"sonnet", "claude/anthropic/ghost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("builder candidates = %v, want %v", got, want)
	}

	rows, err := s.Log(1)
	if err != nil || len(rows) == 0 {
		t.Fatalf("Log(1) = %v/%v", rows, err)
	}
	if !strings.Contains(rows[0].Message, `actor builder: candidate "claude/anthropic/ghost" is not configured; kept`) {
		t.Errorf("migration message = %q, want the kept-candidate note", rows[0].Message)
	}
}

// TestMigrateStripsLegacyKeys pins step 3: policy.order and policy.tier are
// gone, and so are each candidate's roles and tier, while every other key and
// its value survive.
func TestMigrateStripsLegacyKeys(t *testing.T) {
	candBody := `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tier":"yolo","extra":"keep-me"}]`
	polBody := `{"order":{"builder":["claude/p/m"]},"tier":{"builder":"yolo"},"max_switches":2,"max_tier":"yolo","extra_key":"keep"}`

	s := openStore(t)
	seedSections(t, s, map[Section]string{Candidates: candBody, Policy: polBody})

	if migrated, err := s.MigrateToActors(); err != nil || !migrated {
		t.Fatalf("MigrateToActors = %v/%v, want true/nil", migrated, err)
	}

	storedCand, ok, err := s.Body(Candidates)
	if err != nil || !ok {
		t.Fatalf("Body(candidates) = ok=%v err=%v", ok, err)
	}
	for _, gone := range []string{`"roles"`, `"tier"`} {
		if strings.Contains(string(storedCand), gone) {
			t.Errorf("candidates still carry %s:\n%s", gone, storedCand)
		}
	}
	if !strings.Contains(string(storedCand), `"extra": "keep-me"`) {
		t.Errorf("candidates lost the extra key:\n%s", storedCand)
	}

	storedPol, ok, err := s.Body(Policy)
	if err != nil || !ok {
		t.Fatalf("Body(policy) = ok=%v err=%v", ok, err)
	}
	for _, gone := range []string{`"order"`, `"tier"`} {
		if strings.Contains(string(storedPol), gone) {
			t.Errorf("policy still carries %s:\n%s", gone, storedPol)
		}
	}
	for _, kept := range []string{`"max_switches": 2`, `"max_tier": "yolo"`, `"extra_key": "keep"`} {
		if !strings.Contains(string(storedPol), kept) {
			t.Errorf("policy lost %s:\n%s", kept, storedPol)
		}
	}
}

// TestMigrateOneRevision pins the write: the migration adds exactly one
// revision, its source is "migration", and the roles section is deleted.
func TestMigrateOneRevision(t *testing.T) {
	candBody := `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`
	rolesBody := `{"builder":{"candidates":["claude/p/m"]}}`

	s := openStore(t)
	seedSections(t, s, map[Section]string{Candidates: candBody, Roles: rolesBody})

	before, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if migrated, err := s.MigrateToActors(); err != nil || !migrated {
		t.Fatalf("MigrateToActors = %v/%v, want true/nil", migrated, err)
	}
	after, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}

	if len(after) != len(before)+1 {
		t.Fatalf("revisions %d -> %d, want exactly one more", len(before), len(after))
	}
	newest := after[0]
	if newest.Source != "migration" {
		t.Errorf("newest source = %q, want migration", newest.Source)
	}
	if newest.Message != "roles → actors" {
		t.Errorf("newest message = %q, want %q", newest.Message, "roles → actors")
	}
	if has, err := s.Has(Roles); err != nil || has {
		t.Errorf("Has(roles) = %v/%v, want false", has, err)
	}
}

// TestMigrateIdempotent pins the second call: with actors stored, there is
// nothing left to do.
func TestMigrateIdempotent(t *testing.T) {
	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`,
	})

	if migrated, err := s.MigrateToActors(); err != nil || !migrated {
		t.Fatalf("first MigrateToActors = %v/%v, want true/nil", migrated, err)
	}
	migrated, err := s.MigrateToActors()
	if err != nil {
		t.Fatalf("second MigrateToActors: %v", err)
	}
	if migrated {
		t.Error("second MigrateToActors = true, want false")
	}
}

// TestMigrateNothingToDo pins the fresh store: nothing to migrate, nothing
// written.
func TestMigrateNothingToDo(t *testing.T) {
	s := openStore(t)

	migrated, err := s.MigrateToActors()
	if err != nil {
		t.Fatalf("MigrateToActors: %v", err)
	}
	if migrated {
		t.Error("MigrateToActors = true, want false for a fresh store")
	}
	if has, err := s.Has(Actors); err != nil || has {
		t.Errorf("Has(actors) = %v/%v, want false", has, err)
	}
}

// TestRollbackThenRemigrate pins R4's round trip: rolling back to a
// pre-migration revision restores the old keys and removes actors, and the
// next MigrateToActors writes them again as a new revision.
func TestRollbackThenRemigrate(t *testing.T) {
	s := openStore(t)
	seedSections(t, s, map[Section]string{
		Candidates: `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tier":"yolo"}]`,
		Policy:     `{"order":{"builder":["claude/p/m"]},"max_tier":"yolo"}`,
	})

	if migrated, err := s.MigrateToActors(); err != nil || !migrated {
		t.Fatalf("first MigrateToActors = %v/%v, want true/nil", migrated, err)
	}
	rows, err := s.Log(1)
	if err != nil || len(rows) == 0 {
		t.Fatalf("Log(1) = %v/%v", rows, err)
	}
	migRev := rows[0].Rev

	// The revision before the migration is the pre-actors state.
	if _, err := s.As("test", "roll back").Rollback(migRev - 1); err != nil {
		t.Fatalf("Rollback(%d): %v", migRev-1, err)
	}
	if has, err := s.Has(Actors); err != nil || has {
		t.Fatalf("Has(actors) after rollback = %v/%v, want false", has, err)
	}
	cand, _, err := s.Body(Candidates)
	if err != nil || !strings.Contains(string(cand), `"roles"`) {
		t.Fatalf("candidates after rollback = %s (err %v), want the roles key back", cand, err)
	}

	migrated, err := s.MigrateToActors()
	if err != nil || !migrated {
		t.Fatalf("MigrateToActors after rollback = %v/%v, want true/nil", migrated, err)
	}
	newest, err := s.Log(1)
	if err != nil || len(newest) == 0 {
		t.Fatalf("Log(1) = %v/%v", newest, err)
	}
	if newest[0].Source != "migration" {
		t.Errorf("newest source = %q, want migration", newest[0].Source)
	}
}

// readFixture reads a file under internal/roles/testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "roles", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}
