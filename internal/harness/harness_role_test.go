package harness

import "testing"

func TestRolesAreOrderedPlanExecutorFirst(t *testing.T) {
	for _, h := range All() {
		if len(h.Roles) == 0 {
			continue
		}
		if h.Roles[0].Name != "plan-executor" {
			t.Errorf("%s: first role = %q, want plan-executor -- the role relay's loop depends on is reported first",
				h.Kind, h.Roles[0].Name)
		}
	}
}

func TestEveryRoleIsFullyPopulated(t *testing.T) {
	for _, h := range All() {
		for i, r := range h.Roles {
			if r.Name == "" {
				t.Errorf("%s role %d: empty Name", h.Kind, i)
			}
			if r.Path == "" {
				t.Errorf("%s role %q: empty Path", h.Kind, r.Name)
			}
			if r.Doc == "" {
				t.Errorf("%s role %q: empty Doc", h.Kind, r.Name)
			}
		}
	}
}

func TestAgySelectsItsRoleByPreamble(t *testing.T) {
	h, ok := Lookup("agy")
	if !ok {
		t.Fatal("agy missing from the table")
	}
	if len(h.Roles) != 0 {
		t.Errorf("agy has no --agent flag, so it must carry no role files; got %+v", h.Roles)
	}
}

func TestClaudeAndOpencodeCarryBothRoles(t *testing.T) {
	for _, kind := range []string{"claude", "opencode"} {
		h, ok := Lookup(kind)
		if !ok {
			t.Fatalf("%s missing from the table", kind)
		}
		names := map[string]bool{}
		for _, r := range h.Roles {
			names[r.Name] = true
		}
		for _, want := range []string{"plan-executor", "researcher"} {
			if !names[want] {
				t.Errorf("%s missing role %q", kind, want)
			}
		}
	}
}
