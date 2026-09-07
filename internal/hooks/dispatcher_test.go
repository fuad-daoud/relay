package hooks

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var _ Dispatcher = (*LocalDispatcher)(nil)

type executionCall struct {
	ctx        context.Context
	scriptPath string
	event      Event
}

type mockExecutor struct {
	mu    sync.Mutex
	calls []executionCall
	ch    chan executionCall
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		ch: make(chan executionCall, 10),
	}
}

func (m *mockExecutor) Execute(ctx context.Context, scriptPath string, event Event) error {
	call := executionCall{
		ctx:        ctx,
		scriptPath: scriptPath,
		event:      event,
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()

	select {
	case m.ch <- call:
	default:
	}
	return nil
}

func (m *mockExecutor) getCalls() []executionCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]executionCall, len(m.calls))
	copy(copied, m.calls)
	return copied
}

func (m *mockExecutor) waitCall(t *testing.T, timeout time.Duration) executionCall {
	t.Helper()
	select {
	case call := <-m.ch:
		return call
	case <-time.After(timeout):
		t.Fatal("timed out waiting for executor call")
		return executionCall{}
	}
}

func (m *mockExecutor) assertNoCalls(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case call := <-m.ch:
		t.Fatalf("unexpected call to Executor: %+v", call)
	case <-time.After(wait):
	}
}

func TestLocalDispatcher_Dispatch_Routing(t *testing.T) {
	hooksDir := t.TempDir()
	stateChangedDir := filepath.Join(hooksDir, "state_changed.d")
	roundStartedDir := filepath.Join(hooksDir, "round_started.d")

	if err := os.MkdirAll(stateChangedDir, 0755); err != nil {
		t.Fatalf("failed to create state_changed.d: %v", err)
	}
	if err := os.MkdirAll(roundStartedDir, 0755); err != nil {
		t.Fatalf("failed to create round_started.d: %v", err)
	}

	execScript := filepath.Join(stateChangedDir, "hook.sh")
	if err := os.WriteFile(execScript, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write executable script: %v", err)
	}
	if err := os.Chmod(execScript, 0755); err != nil {
		t.Fatalf("failed to chmod executable script: %v", err)
	}

	nonExecFile := filepath.Join(stateChangedDir, "readme.txt")
	if err := os.WriteFile(nonExecFile, []byte("ignored\n"), 0644); err != nil {
		t.Fatalf("failed to write non-executable file: %v", err)
	}
	if err := os.Chmod(nonExecFile, 0644); err != nil {
		t.Fatalf("failed to chmod non-executable file: %v", err)
	}

	subDir := filepath.Join(stateChangedDir, "nested_dir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create nested directory: %v", err)
	}

	roundScript := filepath.Join(roundStartedDir, "round_hook.sh")
	if err := os.WriteFile(roundScript, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("failed to write round started script: %v", err)
	}
	if err := os.Chmod(roundScript, 0755); err != nil {
		t.Fatalf("failed to chmod round started script: %v", err)
	}

	mock := newMockExecutor()
	dispatcher := NewLocalDispatcher(Config{HooksDir: hooksDir}, mock)

	now := time.Now()
	event := Event{
		Type:      EventStateChanged,
		BindingID: "binding-xyz",
		State:     "active",
		OldState:  "idle",
		Round:     5,
		Timestamp: now,
	}

	dispatcher.Dispatch(context.Background(), event)

	call := mock.waitCall(t, 2*time.Second)
	if call.scriptPath != execScript {
		t.Errorf("scriptPath = %q, want %q", call.scriptPath, execScript)
	}
	if call.event.Type != event.Type {
		t.Errorf("event.Type = %v, want %v", call.event.Type, event.Type)
	}
	if call.event.BindingID != event.BindingID {
		t.Errorf("event.BindingID = %q, want %q", call.event.BindingID, event.BindingID)
	}
	if call.event.State != event.State {
		t.Errorf("event.State = %q, want %q", call.event.State, event.State)
	}
	if call.event.OldState != event.OldState {
		t.Errorf("event.OldState = %q, want %q", call.event.OldState, event.OldState)
	}
	if call.event.Round != event.Round {
		t.Errorf("event.Round = %d, want %d", call.event.Round, event.Round)
	}
	if !call.event.Timestamp.Equal(event.Timestamp) {
		t.Errorf("event.Timestamp = %v, want %v", call.event.Timestamp, event.Timestamp)
	}

	mock.assertNoCalls(t, 100*time.Millisecond)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 call, got %d", len(calls))
	}
}

func TestLocalDispatcher_Dispatch_GracefulAbsence(t *testing.T) {
	t.Run("HooksDir does not exist", func(t *testing.T) {
		mock := newMockExecutor()
		nonExistentDir := filepath.Join(t.TempDir(), "nonexistent")
		dispatcher := NewLocalDispatcher(Config{HooksDir: nonExistentDir}, mock)

		dispatcher.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})

	t.Run("Subdirectory for event does not exist", func(t *testing.T) {
		mock := newMockExecutor()
		hooksDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(hooksDir, "round_started.d"), 0755); err != nil {
			t.Fatalf("failed to create round_started.d: %v", err)
		}
		dispatcher := NewLocalDispatcher(Config{HooksDir: hooksDir}, mock)

		dispatcher.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})
}

func TestLocalDispatcher_Dispatch_NilSafety(t *testing.T) {
	t.Run("nil LocalDispatcher receiver", func(t *testing.T) {
		var d *LocalDispatcher
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
	})

	t.Run("nil Executor", func(t *testing.T) {
		d := NewLocalDispatcher(Config{HooksDir: t.TempDir()}, nil)
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
	})

	t.Run("empty HooksDir", func(t *testing.T) {
		mock := newMockExecutor()
		d := NewLocalDispatcher(Config{HooksDir: ""}, mock)
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})
}
