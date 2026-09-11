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

func TestEveryKindCarriesAllThreeRoles(t *testing.T) {
	for _, kind := range []string{"agy", "claude", "opencode"} {
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
