package harness

import (
	"reflect"
	"testing"
)

// TestMissingDefinitions pins #238: a harness kind whose shipped role files
// are not (all) present on disk reports which are missing, by home-relative
// path, so a candidate for that kind can be gated before it is picked.
func TestMissingDefinitions(t *testing.T) {
	role, ok := RoleByName("builder")
	if !ok {
		t.Fatal("builder role not found")
	}

	t.Run("one definition missing", func(t *testing.T) {
		env := freshEnv()
		env.files["/home/u/.config/opencode/agents/plan-executor.md"] = []byte("---\n---\n")
		got := MissingDefinitions(env, "opencode", role.Definitions)
		want := []string{".config/opencode/agents/researcher.md"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MissingDefinitions = %v, want %v", got, want)
		}
	})

	t.Run("both present", func(t *testing.T) {
		env := freshEnv()
		env.files["/home/u/.config/opencode/agents/plan-executor.md"] = []byte("---\n---\n")
		env.files["/home/u/.config/opencode/agents/researcher.md"] = []byte("---\n---\n")
		got := MissingDefinitions(env, "opencode", role.Definitions)
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		env := freshEnv()
		got := MissingDefinitions(env, "nope", role.Definitions)
		if got != nil {
			t.Errorf("MissingDefinitions = %v, want nil", got)
		}
	})
}
