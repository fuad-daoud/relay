package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServeUsageOnNoArgs(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "usage: relay serve") {
		t.Errorf("expected usage on stderr, got %q", string(stderr))
	}
}

func TestServeGCWithoutAbandonedExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "gc"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--abandoned") {
		t.Errorf("expected mention of --abandoned on stderr, got %q", string(stderr))
	}
}

func TestServeUnbindWithoutOwnerExits2(t *testing.T) {
	stdout, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"serve", "unbind", "some-binding"})
	})

	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(string(stderr), "--owner") {
		t.Errorf("expected mention of --owner on stderr, got %q", string(stderr))
	}
}

func TestServeFlagDefaults(t *testing.T) {
	fs, sf := serveFlagSet()

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}

	if sf.listen != ":7777" {
		t.Errorf("default listen = %q, want :7777", sf.listen)
	}
	if sf.state != "" {
		t.Errorf("default state = %q, want empty", sf.state)
	}
	if sf.interval != 2*time.Second {
		t.Errorf("default interval = %v, want 2s", sf.interval)
	}
	if sf.insecureHTTP != false {
		t.Errorf("default insecureHTTP = %v, want false", sf.insecureHTTP)
	}
	if sf.maxBundleBytes != 512<<20 {
		t.Errorf("default maxBundleBytes = %d, want %d", sf.maxBundleBytes, 512<<20)
	}
}
