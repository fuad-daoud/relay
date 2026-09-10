package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/harness"
)

func captureOutput(t *testing.T, fn func() error) (stdout []byte, stderr []byte, err error) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, errOut := os.Pipe()
	if errOut != nil {
		t.Fatalf("pipe: %v", errOut)
	}
	rErr, wErr, errPipe := os.Pipe()
	if errPipe != nil {
		t.Fatalf("pipe: %v", errPipe)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	runErr := fn()

	wOut.Close()
	wErr.Close()

	os.Stdout = origStdout
	os.Stderr = origStderr

	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)

	return outBytes, errBytes, runErr
}

func TestAgentPrintClaudeByteIdentical(t *testing.T) {
	expected, err := harness.AgentDoc("plan-executor", "claude")
	if err != nil {
		t.Fatalf("AgentDoc(claude): %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Errorf("stdout not byte-identical to embedded claude doc")
	}
}

func TestAgentPrintOpencodeByteIdentical(t *testing.T) {
	expected, err := harness.AgentDoc("plan-executor", "opencode")
	if err != nil {
		t.Fatalf("AgentDoc(opencode): %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "opencode"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Errorf("stdout not byte-identical to embedded opencode doc")
	}
}

func TestAgentPrintAgyExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "agy"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "preamble") {
		t.Errorf("expected stderr to explain preamble, got %q", string(stderr))
	}
}

func TestAgentPrintUnknownKindExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "unknown-kind"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "claude") || !strings.Contains(string(stderr), "opencode") {
		t.Errorf("expected stderr to name kinds that have definitions, got %q", string(stderr))
	}
}

func TestAgentPrintMissingKindExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

func TestAgentPrintDefaultsToPlanExecutor(t *testing.T) {
	expected, err := harness.AgentDoc("plan-executor", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Error("no --role must print the plan-executor definition unchanged")
	}
}

func TestAgentPrintSelectsResearcher(t *testing.T) {
	expected, err := harness.AgentDoc("researcher", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude", "--role", "researcher"})
	})

	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
	if !bytes.Equal(stdout, expected) {
		t.Error("--role researcher must print the researcher definition")
	}
}

func TestAgentPrintUnknownRoleExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"agent", "print", "--kind", "claude", "--role", "nosuch"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "researcher") {
		t.Errorf("stderr must name the roles the kind has, got %q", string(stderr))
	}
}
