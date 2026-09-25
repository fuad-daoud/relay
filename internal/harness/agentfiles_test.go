package harness

import (
	"testing"
)

// The fake is install_test.go's fakeInstallEnv (freshEnv): the same package's
// tests share it.

const agentFilesAgent = "reviewer"

func TestAgentFilesStates(t *testing.T) {
	agyPath := "/home/u/.gemini/config/agents/reviewer.md"

	t.Run("identical file gives up to date", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"
		shipped, err := AgentDoc(agentFilesAgent, "agy")
		if err != nil {
			t.Fatalf("AgentDoc: %v", err)
		}
		env.files[agyPath] = shipped

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1: %+v", len(got), got)
		}
		if got[0].Kind != "agy" || got[0].Path != agyPath || got[0].State != FileUpToDate {
			t.Errorf("entry = %+v, want agy %s %s", got[0], agyPath, FileUpToDate)
		}
	})

	t.Run("manifest-matching old copy gives stale", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"
		old := []byte("an older relevo definition\n")
		env.files[agyPath] = old
		env.manifest[".gemini/config/agents/reviewer.md"] = docSHA(old)

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 || got[0].State != FileStale {
			t.Fatalf("entries = %+v, want one %s", got, FileStale)
		}
	})

	t.Run("hand-edited file gives your edit, with its model pin read", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"
		env.files[agyPath] = []byte("---\nname: reviewer\nmodel: haiku\n---\n\nbody\n")

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 || got[0].State != FileEdited {
			t.Fatalf("entries = %+v, want one %s", got, FileEdited)
		}
		if got[0].Model != "haiku" {
			t.Errorf("Model = %q, want haiku", got[0].Model)
		}
	})

	t.Run("absent file gives missing", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 || got[0].State != FileMissing {
			t.Fatalf("entries = %+v, want one %s", got, FileMissing)
		}
		if got[0].Model != "" {
			t.Errorf("Model = %q, want empty", got[0].Model)
		}
	})

	t.Run("a kind whose binary is not on PATH is absent from the result", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["claude"] = "/bin/claude"
		shipped, err := AgentDoc(agentFilesAgent, "claude")
		if err != nil {
			t.Fatalf("AgentDoc: %v", err)
		}
		env.files["/home/u/.claude/agents/reviewer.md"] = shipped

		got, err := AgentFiles(env, agentFilesAgent)
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if len(got) != 1 || got[0].Kind != "claude" {
			t.Fatalf("entries = %+v, want only claude", got)
		}
	})

	t.Run("a custom agent name gives nil", func(t *testing.T) {
		env := freshEnv()
		env.lookPaths["agy"] = "/bin/agy"

		got, err := AgentFiles(env, "my-own-agent")
		if err != nil {
			t.Fatalf("AgentFiles: %v", err)
		}
		if got != nil {
			t.Fatalf("entries = %+v, want nil", got)
		}
	})
}

func TestResetAgentFileOverwritesAndSettles(t *testing.T) {
	env := freshEnv()
	env.lookPaths["agy"] = "/bin/agy"
	agyPath := "/home/u/.gemini/config/agents/reviewer.md"
	env.files[agyPath] = []byte("my own edit\n")

	res, err := ResetAgentFile(env, "agy", agentFilesAgent)
	if err != nil {
		t.Fatalf("ResetAgentFile: %v", err)
	}
	if res.Outcome != OutcomeOverwrote {
		t.Fatalf("Outcome = %v, want %v", res.Outcome, OutcomeOverwrote)
	}

	shipped, err := AgentDoc(agentFilesAgent, "agy")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}
	if !DocEqual(shipped, env.files[agyPath]) {
		t.Errorf("file on disk is not the shipped copy after reset")
	}

	got, err := AgentFiles(env, agentFilesAgent)
	if err != nil {
		t.Fatalf("AgentFiles: %v", err)
	}
	if len(got) != 1 || got[0].State != FileUpToDate {
		t.Fatalf("entries = %+v, want one %s", got, FileUpToDate)
	}
}
