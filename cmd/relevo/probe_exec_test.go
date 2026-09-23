package main

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLineWriterSplitsLines checks that lineWriter emits one call per line,
// drops the newline (and a \r before it), and flushes a final unterminated
// line.
func TestLineWriterSplitsLines(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	var got []string
	lw := &lineWriter{onLine: func(line []byte) {
		got = append(got, string(line))
	}}
	if _, err := lw.Write([]byte("a\nb")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := lw.Write([]byte("c\r\nd\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := lw.Write([]byte("tail")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lw.Flush()
	want := []string{"a", "bc", "d", "tail"}
	if len(got) != len(want) {
		t.Fatalf("onLine got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("onLine got %q, want %q", got, want)
		}
	}
}

// TestLineExecDoesNotHangOnInheritedStdout checks that a background child
// holding stdout open cannot hang Run: WaitDelay bounds the copy, and the
// resulting ErrWaitDelay is treated as success.
func TestLineExecDoesNotHangOnInheritedStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var mu sync.Mutex
	var lines []string
	start := time.Now()
	err := lineExec{}.Run(ctx, t.TempDir(),
		[]string{"sh", "-c", "echo first; sleep 20 & exit 0"},
		func(line []byte) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, string(line))
		})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run must treat ErrWaitDelay as success; got %v", err)
	}
	if elapsed > 12*time.Second {
		t.Fatalf("Run took %v; it must return within 12s", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || lines[0] != "first" {
		t.Fatalf("onLine got %q, want [first]", lines)
	}
}

// TestLineExecNonZeroExitCarriesStderr checks that a non-zero exit is an
// error whose text carries the exit status and the stderr tail.
func TestLineExecNonZeroExitCarriesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := lineExec{}.Run(ctx, t.TempDir(),
		[]string{"sh", "-c", "echo oops 1>&2; exit 3"}, nil)
	if err == nil {
		t.Fatal("Run must return an error for exit status 3")
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("error %q must contain %q", err, "exit status 3")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Fatalf("error %q must contain %q", err, "oops")
	}
}
