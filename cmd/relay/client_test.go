package main

import (
	"errors"
	"strings"
	"testing"
)

// TestClientUsageOnNoArgs pins the no-args usage line; CI has no herdr
// binary, so this test must never reach cmdClient's callers that do (it
// doesn't -- `relay client` alone dispatches nothing).
func TestClientUsageOnNoArgs(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"client"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage: relay client") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

// TestClientAddServerFlagExclusivityExits2 pins that --fingerprint and --ca
// refuse each other before anything is saved or any server is contacted:
// the exclusivity check runs before ValidateEntry and before any network
// call, so this never reaches herdr or the wire either.
func TestClientAddServerFlagExclusivityExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"client", "add-server", "zen", "https://zen:7777", "--fingerprint", "sha256:aa", "--ca", "system"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "mutually exclusive") {
		t.Errorf("expected the mutual-exclusion message on stderr, got %q", string(stderr))
	}
}
