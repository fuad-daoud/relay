package main

import (
	"strings"
	"testing"
)

// TestBindResumeWithoutNameIsRejected covers a flag shape that reads fine and
// silently does the wrong thing: --resume is a bool and the name comes from
// --name, so `relay bind --resume upjo` drops the positional and the binding
// lookup then fails on the empty name ("relay: : binding not found").
//
// The check runs before any runtime is built, so this test touches neither
// the state directory nor herdr.
func TestBindResumeWithoutNameIsRejected(t *testing.T) {
	err := run([]string{"bind", "--resume", "upjo"})
	if err == nil {
		t.Fatal("relay bind --resume with a positional name must be rejected")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}
