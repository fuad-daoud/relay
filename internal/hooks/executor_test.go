package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSExecutorExecute_EnvAndLogging(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test_env.sh")
	scriptContent := `#!/bin/sh
echo "EVENT=$RELAY_EVENT"
echo "BINDING=$RELAY_BINDING"
echo "STATE=$RELAY_STATE"
echo "OLD_STATE=$RELAY_OLD_STATE"
echo "ROUND=$RELAY_ROUND"
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	logPath := filepath.Join(tmpDir, "test.log")
	executor := NewOSExecutor(logPath)

	event := Event{
		Type:      EventStateChanged,
		BindingID: "bind-abc",
		State:     "working",
		OldState:  "idle",
		Round:     3,
		Timestamp: time.Now().UTC(),
	}

	err := executor.Execute(context.Background(), scriptPath, event)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(logData)

	expectedStrings := []string{
		"EVENT=state_changed",
		"BINDING=bind-abc",
		"STATE=working",
		"OLD_STATE=idle",
		"ROUND=3",
	}
	for _, expected := range expectedStrings {
		if !strings.Contains(content, expected) {
			t.Errorf("log output missing %q, got:\n%s", expected, content)
		}
	}
}

func TestOSExecutorExecute_FailureLogging(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fail_script.sh")
	scriptContent := `#!/bin/sh
exit 2
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	logPath := filepath.Join(tmpDir, "fail.log")
	executor := NewOSExecutor(logPath)

	event := Event{
		Type:      EventStateChanged,
		BindingID: "bind-xyz",
		State:     "error",
		OldState:  "working",
		Round:     1,
		Timestamp: time.Now().UTC(),
	}

	err := executor.Execute(context.Background(), scriptPath, event)
	if err == nil {
		t.Fatal("Execute expected error, got nil")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(logData)

	if !strings.Contains(content, "hook execution failed for") {
		t.Errorf("log output missing failure message prefix, got:\n%s", content)
	}
	if !strings.Contains(content, "exit status 2") {
		t.Errorf("log output missing exit status 2, got:\n%s", content)
	}
}

func TestOSExecutorExecute_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "sleep_script.sh")
	scriptContent := `#!/bin/sh
sleep 10
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	logPath := filepath.Join(tmpDir, "timeout.log")
	executor := NewOSExecutor(logPath)

	event := Event{
		Type:      EventRoundStarted,
		BindingID: "bind-timeout",
		Round:     1,
		Timestamp: time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := executor.Execute(ctx, scriptPath, event)
	if err == nil {
		t.Fatal("Execute expected timeout error, got nil")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(logData)
	if !strings.Contains(content, "hook execution failed for") {
		t.Errorf("log output missing failure message prefix, got:\n%s", content)
	}
}
