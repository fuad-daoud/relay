package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnsureCandidateNamesWritesOnce pins A1 §3.8's migration: the first run
// writes a derived name into every element that lacks one, keeping every key
// and the element order; the second run returns false and writes nothing.
func TestEnsureCandidateNamesWritesOnce(t *testing.T) {
	s := openStore(t)

	body := `[
	  {"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"colour":"red"},
	  {"harness":"agy","provider":"google","model":"gemini-3.8-flash-high","roles":["builder"]}
	]`
	if _, err := s.Put(Candidates, []byte(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}

	changed, err := s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("EnsureCandidateNames: %v", err)
	}
	if !changed {
		t.Fatal("EnsureCandidateNames = false, want true on the first run")
	}

	stored, ok, err := s.Body(Candidates)
	if err != nil || !ok {
		t.Fatalf("Body: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(stored), `"colour": "red"`) {
		t.Errorf("stored body lost the unknown key: %s", stored)
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(stored, &rows); err != nil {
		t.Fatalf("decode stored body: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	// The element order is the file's, and each element got its derived name.
	wantNames := []string{"sonnet", "gemini-3.8-flash-high"}
	for i, want := range wantNames {
		var name, model string
		if err := json.Unmarshal(rows[i]["name"], &name); err != nil {
			t.Fatalf("row %d name: %v", i, err)
		}
		if name != want {
			t.Errorf("row %d name = %q, want %q", i, name, want)
		}
		if err := json.Unmarshal(rows[i]["model"], &model); err != nil {
			t.Fatalf("row %d model: %v", i, err)
		}
		if model == "" {
			t.Errorf("row %d lost its model", i)
		}
	}

	after, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Error("the version did not change on the write")
	}

	changed, err = s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("second EnsureCandidateNames: %v", err)
	}
	if changed {
		t.Error("second EnsureCandidateNames = true, want false")
	}
	again, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if again != after {
		t.Errorf("the second run bumped the version: %d -> %d", after, again)
	}
}

// TestEnsureCandidateNamesAbsentSection pins the no-op case: a store with no
// candidates section has nothing to name.
func TestEnsureCandidateNamesAbsentSection(t *testing.T) {
	s := openStore(t)

	changed, err := s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("EnsureCandidateNames: %v", err)
	}
	if changed {
		t.Error("EnsureCandidateNames = true, want false with no candidates section")
	}

	if _, ok, err := s.Body(Candidates); err != nil || ok {
		t.Errorf("a section was written: ok=%v err=%v", ok, err)
	}
}
